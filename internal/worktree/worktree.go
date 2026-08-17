// Package worktree provides a primitive for materializing a recorded commit OID
// into a throwaway Git checkout. Callers must call Cleanup or Close when done.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidSHA  = errors.New("invalid SHA")
	ErrSHANotFound = errors.New("SHA not found")
)

// Worktree is a disposable Git checkout materialized at a specific commit OID.
// It is a linked worktree for ordinary repositories and an independent clone
// when submodules require private Git metadata. Dir is always in the system
// temp area outside the repository. Callers must call Cleanup or Close when done.
type Worktree struct {
	// Dir is the absolute path to the worktree directory (system temp, outside the repo).
	Dir string
	// Submodules maps each populated, root-relative submodule path to its
	// resolved HEAD OID. It is empty when the commit contains no gitlinks.
	Submodules  map[string]string
	root        string // repo root, passed to git -C
	resolvedOID string // the full OID used at checkout time
	mode        checkoutMode
	removed     bool // idempotency guard for Cleanup/Close
}

type checkoutMode uint8

const (
	checkoutLinkedWorktree checkoutMode = iota
	checkoutIndependentClone
)

// ValidateSubmodules fails when a non-legacy recorded map does not identify
// this checkout exactly. Empty maps predate submodule recording and are accepted.
func (w *Worktree) ValidateSubmodules(recorded map[string]string) error {
	if len(recorded) == 0 {
		return nil
	}
	if !maps.Equal(recorded, w.Submodules) {
		return fmt.Errorf("recorded submodules do not match populated checkout")
	}
	return nil
}

// Checkout materializes commit sha in a new throwaway worktree and returns it.
// root is the repo root (passed to git -C). sha must be a full 40- or 64-char
// lowercase hex commit OID; branch names, short SHAs, and mutable refs are
// rejected with ErrInvalidSHA. Returns ErrSHANotFound if sha is not present in
// the repository.
func Checkout(ctx context.Context, root, sha string) (*Worktree, error) {
	if err := validateSHA(sha); err != nil {
		return nil, err
	}

	// Resolve the OID via rev-parse to confirm it exists as a commit.
	out, err := gitCmd(ctx, root, "rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSHANotFound, sha)
	}
	resolvedOID := strings.TrimSpace(out)

	// CRITICAL invariant: the input must BE the commit OID, not merely resolve to
	// one. A hex-looking branch or tag whose name matches one hash width but
	// resolves to a different-width OID (or a same-name moving ref) is rejected
	// here. Legitimate recorded git_sha values are always direct commit OIDs, so
	// equality always holds for valid replay inputs.
	if resolvedOID != sha {
		return nil, fmt.Errorf("%w: resolved OID %s != input %s", ErrInvalidSHA, resolvedOID, sha)
	}

	gitlinks, err := commitGitlinks(ctx, root, resolvedOID)
	if err != nil {
		return nil, fmt.Errorf("inspect commit gitlinks: %w", err)
	}
	if len(gitlinks) > 0 {
		return checkoutWithSubmodules(ctx, root, resolvedOID, gitlinks)
	}

	dir, err := os.MkdirTemp("", "etude-worktree-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	// Add the worktree. Pass resolvedOID (never raw sha) and use -- to prevent
	// the OID from being misinterpreted as a flag or option.
	_, addErr := gitCmd(ctx, root, "worktree", "add", "--detach", "--", dir, resolvedOID)
	if addErr != nil {
		os.RemoveAll(dir)
		// Best-effort prune to clear any partial registration.
		_, _ = gitCmd(ctx, root, "worktree", "prune")
		return nil, fmt.Errorf("worktree add: %w", addErr)
	}

	return &Worktree{
		Dir:         dir,
		root:        root,
		resolvedOID: resolvedOID,
		mode:        checkoutLinkedWorktree,
	}, nil
}

func commitGitlinks(ctx context.Context, root, oid string) (map[string]string, error) {
	out, err := gitCmd(ctx, root, "ls-tree", "-r", "--full-tree", "-z", oid)
	if err != nil {
		return nil, err
	}
	gitlinks := make(map[string]string)
	for _, entry := range strings.Split(out, "\x00") {
		if !strings.HasPrefix(entry, "160000 ") {
			continue
		}
		metadata, gitlinkPath, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || gitlinkPath == "" {
			return nil, fmt.Errorf("unexpected gitlink tree entry")
		}
		gitlinks[gitlinkPath] = fields[2]
	}
	return gitlinks, nil
}

func checkoutWithSubmodules(ctx context.Context, root, resolvedOID string, topLevelGitlinks map[string]string) (_ *Worktree, returnErr error) {
	if _, err := gitCmd(ctx, root, "cat-file", "-e", resolvedOID+":.gitmodules"); err != nil {
		return nil, fmt.Errorf("commit contains gitlinks but no valid .gitmodules: %w", err)
	}

	dir, err := os.MkdirTemp("", "etude-worktree-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	if _, err := gitCmd(ctx, root, "clone", "--local", "--no-checkout", "--", root, dir); err != nil {
		return nil, fmt.Errorf("clone submodule worktree: %w", err)
	}

	remoteURL, err := effectiveRemoteURL(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("resolve source remote: %w", err)
	}
	if remoteURL != "" {
		if _, err := gitCmd(ctx, dir, "remote", "set-url", "origin", remoteURL); err != nil {
			return nil, fmt.Errorf("preserve source remote: %w", err)
		}
	}

	objectsDir, err := commonObjectsDir(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("resolve source object store: %w", err)
	}
	alternatesPath := filepath.Join(dir, ".git", "objects", "info", "alternates")
	if err := os.MkdirAll(filepath.Dir(alternatesPath), 0o755); err != nil {
		return nil, fmt.Errorf("create alternates directory: %w", err)
	}
	if err := os.WriteFile(alternatesPath, []byte(objectsDir+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("record source object alternate: %w", err)
	}

	for _, setting := range [][2]string{
		{"submodule.alternateLocation", "superproject"},
		{"submodule.alternateErrorStrategy", "info"},
	} {
		if _, err := gitCmd(ctx, dir, "config", setting[0], setting[1]); err != nil {
			return nil, fmt.Errorf("configure %s: %w", setting[0], err)
		}
	}
	if _, err := gitCmd(ctx, dir, "checkout", "--detach", "--no-recurse-submodules", resolvedOID); err != nil {
		return nil, fmt.Errorf("checkout superproject: %w", err)
	}
	updateArgs := []string{"-c", "submodule.active=:(glob)**", "submodule", "update", "--init", "--recursive", "--checkout", "--"}
	paths := make([]string, 0, len(topLevelGitlinks))
	for gitlinkPath := range topLevelGitlinks {
		paths = append(paths, ":(literal)"+gitlinkPath)
	}
	sort.Strings(paths)
	updateArgs = append(updateArgs, paths...)
	if _, err := gitCmd(ctx, dir, updateArgs...); err != nil {
		return nil, fmt.Errorf("populate submodules: %w", err)
	}

	submodules, err := populatedSubmodules(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("enumerate submodules: %w", err)
	}
	if err := validatePopulatedGitlinks(ctx, dir, topLevelGitlinks, submodules); err != nil {
		return nil, fmt.Errorf("validate populated submodules: %w", err)
	}
	return &Worktree{
		Dir:         dir,
		Submodules:  submodules,
		root:        root,
		resolvedOID: resolvedOID,
		mode:        checkoutIndependentClone,
	}, nil
}

func validatePopulatedGitlinks(ctx context.Context, root string, rootGitlinks, populated map[string]string) error {
	check := func(prefix string, gitlinks map[string]string) error {
		for relativePath, wantOID := range gitlinks {
			fullPath := relativePath
			if prefix != "" {
				fullPath = path.Join(prefix, relativePath)
			}
			if gotOID := populated[fullPath]; gotOID != wantOID {
				return fmt.Errorf("gitlink %q resolved to %q, want %q", fullPath, gotOID, wantOID)
			}
		}
		return nil
	}
	if err := check("", rootGitlinks); err != nil {
		return err
	}
	for modulePath, moduleOID := range populated {
		gitlinks, err := commitGitlinks(ctx, filepath.Join(root, filepath.FromSlash(modulePath)), moduleOID)
		if err != nil {
			return fmt.Errorf("inspect %q gitlinks: %w", modulePath, err)
		}
		if err := check(modulePath, gitlinks); err != nil {
			return err
		}
	}
	return nil
}

func effectiveRemoteURL(ctx context.Context, root string) (string, error) {
	remote := "origin"
	if branch, err := gitCmd(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		if configured, err := gitCmd(ctx, root, "config", "--get", "branch."+strings.TrimSpace(branch)+".remote"); err == nil {
			remote = strings.TrimSpace(configured)
		}
	}
	if remote == "." {
		return filepath.Abs(root)
	}
	url, err := gitCmd(ctx, root, "remote", "get-url", remote)
	if err != nil {
		if remote == "origin" {
			return "", nil
		}
		return "", err
	}
	url = strings.TrimSpace(url)
	if isRelativeLocalURL(url) {
		return filepath.Abs(filepath.Join(root, url))
	}
	return url, nil
}

func isRelativeLocalURL(url string) bool {
	if url == "" || filepath.IsAbs(url) || strings.HasPrefix(url, "~") || strings.Contains(url, "://") {
		return false
	}
	colon, slash := strings.IndexByte(url, ':'), strings.IndexByte(url, '/')
	return colon < 0 || (slash >= 0 && colon > slash)
}

func commonObjectsDir(ctx context.Context, root string) (string, error) {
	commonDir, err := gitCmd(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	return filepath.Abs(filepath.Join(commonDir, "objects"))
}

func populatedSubmodules(ctx context.Context, root string) (map[string]string, error) {
	out, err := gitCmd(ctx, root, "submodule", "foreach", "--quiet", "--recursive",
		`printf '%s\000%s\000' "$displaypath" "$(git rev-parse HEAD)"`)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(out, "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("unexpected NUL-delimited submodule output")
	}
	result := make(map[string]string, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		path, oid := fields[i], fields[i+1]
		if path == "" || oid == "" {
			return nil, fmt.Errorf("empty submodule path or OID")
		}
		if !utf8.ValidString(path) {
			return nil, fmt.Errorf("submodule path is not valid UTF-8")
		}
		result[path] = oid
	}
	return result, nil
}

// Cleanup removes the worktree and de-registers it from the repository.
// It is idempotent: calling it multiple times returns nil after the first
// successful removal. On a non-idempotency remove failure, Cleanup still runs
// os.RemoveAll and git worktree prune (for no-leak) and returns the underlying
// error so callers can log it.
func (w *Worktree) Cleanup(ctx context.Context) error {
	if w.removed {
		return nil
	}
	if w.mode == checkoutIndependentClone {
		if err := os.RemoveAll(w.Dir); err != nil {
			return err
		}
		w.removed = true
		return nil
	}

	_, removeErr := gitCmd(ctx, w.root, "worktree", "remove", "--force", w.Dir)
	if removeErr != nil {
		// Check for the idempotency case: a second remove emits "is not a working
		// tree" at rc=128. Treat that as already-removed and fall through.
		if !strings.Contains(removeErr.Error(), "is not a working tree") {
			// Non-idempotency failure. Still run RemoveAll + prune for no-leak,
			// then return the error.
			os.RemoveAll(w.Dir)
			_, _ = gitCmd(ctx, w.root, "worktree", "prune")
			w.removed = true
			return removeErr
		}
		// Already-removed: fall through to RemoveAll + prune.
	}

	// RemoveAll is a no-op for a missing path, so always call it.
	os.RemoveAll(w.Dir)

	// Prune runs LAST so it reaps any stale .git/worktrees entry including the
	// case where remove failed but the filesystem dir was already deleted.
	_, _ = gitCmd(ctx, w.root, "worktree", "prune")

	w.removed = true
	return nil
}

// Close implements io.Closer by calling Cleanup(context.Background()).
// It provides no cancellation and is appropriate for defer statements.
// Idempotent: Close-after-explicit-Cleanup is a no-op returning nil.
func (w *Worktree) Close() error {
	return w.Cleanup(context.Background())
}

// gitCmd runs git with the given args inside root, capturing stdout and stderr
// separately. The environment extends os.Environ() with strict git settings to
// prevent interactive prompts, optional locks, and locale-dependent output.
// Errors are wrapped with the joined args and trimmed stderr for diagnostics.
func gitCmd(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
		"LANG=C",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// validateSHA requires sha to be a full 40- or 64-character lowercase hex
// string, mirroring refstore.validateOID. It rejects empty strings, leading
// dashes (option-injection guard), wrong lengths, and non-hex characters.
func validateSHA(sha string) error {
	if sha == "" {
		return fmt.Errorf("%w: empty", ErrInvalidSHA)
	}
	if strings.HasPrefix(sha, "-") {
		return fmt.Errorf("%w: must not start with '-'", ErrInvalidSHA)
	}
	if len(sha) != 40 && len(sha) != 64 {
		return fmt.Errorf("%w: want 40 or 64 hex characters, got %d", ErrInvalidSHA, len(sha))
	}
	for _, r := range sha {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("%w: want lowercase hex", ErrInvalidSHA)
		}
	}
	return nil
}
