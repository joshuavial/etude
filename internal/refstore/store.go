package refstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	runsNS     = "refs/etude/runs/"
	evalsNS    = "refs/etude/evals/"
	retrosNS   = "refs/etude/retros/"
	defaultGit = "git"
)

var (
	ErrEmptyTree   = errors.New("empty tree")
	ErrInvalidPath = errors.New("invalid path")
	ErrInvalidRef  = errors.New("invalid ref")
	ErrNotFound    = errors.New("not found")
	ErrRefExists   = errors.New("ref exists")
	ErrStaleRef    = errors.New("stale ref")
)

type Store struct {
	RepoDir string
	GitPath string
}

type WriteOptions struct {
	ExpectedOld string
	Message     string
}

func New(repoDir string) Store {
	return Store{RepoDir: repoDir, GitPath: defaultGit}
}

func (s Store) WriteCommit(ctx context.Context, ref string, files map[string][]byte, opts WriteOptions) (string, error) {
	if err := s.validateRef(ctx, ref); err != nil {
		return "", err
	}
	if err := s.rejectSymbolicRef(ctx, ref); err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", ErrEmptyTree
	}

	if opts.ExpectedOld != "" {
		if err := validateOID(opts.ExpectedOld); err != nil {
			return "", fmt.Errorf("%w: expected old commit: %v", ErrInvalidRef, err)
		}
		if err := s.ensureCommit(ctx, opts.ExpectedOld); err != nil {
			return "", err
		}
	} else if _, err := s.Resolve(ctx, ref); err == nil {
		return "", fmt.Errorf("%w: %s", ErrRefExists, ref)
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}

	tree, err := s.writeTree(ctx, files)
	if err != nil {
		return "", err
	}

	commit, err := s.commitTree(ctx, ref, tree, opts)
	if err != nil {
		return "", err
	}

	// empty old means "ref must not already exist" per git-update-ref(1) — works
	// for both SHA-1 and SHA-256 repos, unlike a fixed-width zero string.
	old := ""
	staleErr := ErrRefExists
	if opts.ExpectedOld != "" {
		old = opts.ExpectedOld
		staleErr = ErrStaleRef
	}
	// --no-deref is the TOCTOU backstop: even if ref becomes symbolic after
	// rejectSymbolicRef, update-ref must never follow it outside refs/etude.
	if _, err := s.git(ctx, nil, nil, "update-ref", "--no-deref", ref, commit, old); err != nil {
		// Objects created before a failed ref update are intentionally left for
		// normal git gc or the future etude GC command.
		return "", fmt.Errorf("%w: update %s: %v", staleErr, ref, err)
	}

	return commit, nil
}

func (s Store) Resolve(ctx context.Context, ref string) (string, error) {
	if err := s.validateRef(ctx, ref); err != nil {
		return "", err
	}
	if err := s.rejectSymbolicRef(ctx, ref); err != nil {
		return "", err
	}
	out, err := s.git(ctx, nil, nil, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return strings.TrimSpace(out), nil
}

func (s Store) ReadFile(ctx context.Context, ref, filePath string) ([]byte, error) {
	if err := s.validateRef(ctx, ref); err != nil {
		return nil, err
	}
	if err := validateFilePath(filePath); err != nil {
		return nil, err
	}
	commit, err := s.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	out, err := s.gitBytes(ctx, nil, nil, "show", commit+":"+filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s:%s", ErrNotFound, ref, filePath)
	}
	return out, nil
}

func (s Store) ReadCommitFile(ctx context.Context, commit, filePath string) ([]byte, error) {
	if err := validateOID(commit); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrInvalidRef, err)
	}
	if err := validateFilePath(filePath); err != nil {
		return nil, err
	}
	if err := s.ensureCommit(ctx, commit); err != nil {
		return nil, err
	}
	out, err := s.gitBytes(ctx, nil, nil, "show", commit+":"+filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s:%s", ErrNotFound, commit, filePath)
	}
	return out, nil
}

func (s Store) List(ctx context.Context, prefix string) ([]string, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	if err := s.validateRef(ctx, strings.TrimSuffix(prefix, "/")+"/placeholder"); err != nil {
		return nil, err
	}
	out, err := s.git(ctx, nil, nil, "for-each-ref", "--format=%(refname)", "--sort=refname", prefix)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	refs := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && s.rejectSymbolicRef(ctx, line) == nil {
			refs = append(refs, line)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

func (s Store) writeTree(ctx context.Context, files map[string][]byte) (string, error) {
	index, err := os.CreateTemp("", "etude-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		os.Remove(indexPath)
		return "", err
	}
	defer os.Remove(indexPath)

	env := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := s.git(ctx, env, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}

	paths := make([]string, 0, len(files))
	for filePath := range files {
		if err := validateFilePath(filePath); err != nil {
			return "", err
		}
		paths = append(paths, filePath)
	}
	sort.Strings(paths)

	for _, filePath := range paths {
		oid, err := s.git(ctx, nil, bytes.NewReader(files[filePath]), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		if _, err := s.git(ctx, env, nil, "update-index", "--add", "--cacheinfo", "100644", strings.TrimSpace(oid), filePath); err != nil {
			return "", err
		}
	}

	tree, err := s.git(ctx, env, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(tree), nil
}

func (s Store) commitTree(ctx context.Context, ref, tree string, opts WriteOptions) (string, error) {
	message := opts.Message
	if strings.TrimSpace(message) == "" {
		message = "etude storage commit for " + ref
	}
	args := []string{"commit-tree", tree}
	if opts.ExpectedOld != "" {
		args = append(args, "-p", opts.ExpectedOld)
	}
	args = append(args, "-m", message)

	out, err := s.git(ctx, s.commitEnv(ctx), nil, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (s Store) commitEnv(ctx context.Context) []string {
	env := make([]string, 0, 6)
	if os.Getenv("GIT_AUTHOR_DATE") == "" {
		env = append(env, "GIT_AUTHOR_DATE=1970-01-01T00:00:00Z")
	}
	if os.Getenv("GIT_COMMITTER_DATE") == "" {
		env = append(env, "GIT_COMMITTER_DATE=1970-01-01T00:00:00Z")
	}
	if os.Getenv("GIT_AUTHOR_NAME") != "" && os.Getenv("GIT_AUTHOR_EMAIL") != "" &&
		os.Getenv("GIT_COMMITTER_NAME") != "" && os.Getenv("GIT_COMMITTER_EMAIL") != "" {
		return env
	}
	if s.hasUserIdentity(ctx) {
		return env
	}
	if os.Getenv("GIT_AUTHOR_NAME") == "" {
		env = append(env, "GIT_AUTHOR_NAME=etude")
	}
	if os.Getenv("GIT_AUTHOR_EMAIL") == "" {
		env = append(env, "GIT_AUTHOR_EMAIL=etude@example.invalid")
	}
	if os.Getenv("GIT_COMMITTER_NAME") == "" {
		env = append(env, "GIT_COMMITTER_NAME=etude")
	}
	if os.Getenv("GIT_COMMITTER_EMAIL") == "" {
		env = append(env, "GIT_COMMITTER_EMAIL=etude@example.invalid")
	}
	return env
}

func (s Store) hasUserIdentity(ctx context.Context) bool {
	name, nameErr := s.git(ctx, nil, nil, "config", "user.name")
	email, emailErr := s.git(ctx, nil, nil, "config", "user.email")
	return nameErr == nil && emailErr == nil && strings.TrimSpace(name) != "" && strings.TrimSpace(email) != ""
}

func (s Store) ensureCommit(ctx context.Context, oid string) error {
	if _, err := s.git(ctx, nil, nil, "cat-file", "-e", oid+"^{commit}"); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, oid)
	}
	return nil
}

// DeleteRef deletes the named ref using git update-ref -d --no-deref.
// It validates the ref namespace (refs/etude/runs, refs/etude/evals, or
// refs/etude/retros) and rejects symbolic refs before issuing the delete,
// then errors if the ref does not exist. Git objects are left for normal git gc.
func (s Store) DeleteRef(ctx context.Context, ref string) error {
	if err := s.validateRef(ctx, ref); err != nil {
		return err
	}
	if err := s.rejectSymbolicRef(ctx, ref); err != nil {
		return err
	}
	if _, err := s.Resolve(ctx, ref); err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	if _, err := s.git(ctx, nil, nil, "update-ref", "-d", "--no-deref", ref); err != nil {
		return fmt.Errorf("delete ref %s: %w", ref, err)
	}
	return nil
}

func (s Store) validateRef(ctx context.Context, ref string) error {
	if !(strings.HasPrefix(ref, runsNS) || strings.HasPrefix(ref, evalsNS) || strings.HasPrefix(ref, retrosNS)) {
		return fmt.Errorf("%w: %s", ErrInvalidRef, ref)
	}
	if ref == runsNS || ref == evalsNS || ref == retrosNS {
		return fmt.Errorf("%w: %s", ErrInvalidRef, ref)
	}
	if strings.Contains(ref, " ") || strings.Contains(ref, "\\") {
		return fmt.Errorf("%w: %s", ErrInvalidRef, ref)
	}
	if _, err := s.git(ctx, nil, nil, "check-ref-format", ref); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRef, ref)
	}
	return nil
}

func (s Store) rejectSymbolicRef(ctx context.Context, ref string) error {
	target, err := s.git(ctx, nil, nil, "symbolic-ref", "-q", ref)
	if err == nil {
		return fmt.Errorf("%w: symbolic ref %s -> %s", ErrInvalidRef, ref, strings.TrimSpace(target))
	}
	return nil
}

func validatePrefix(prefix string) error {
	if prefix != strings.TrimSuffix(runsNS, "/") && prefix != strings.TrimSuffix(evalsNS, "/") && prefix != strings.TrimSuffix(retrosNS, "/") &&
		!strings.HasPrefix(prefix, runsNS) && !strings.HasPrefix(prefix, evalsNS) && !strings.HasPrefix(prefix, retrosNS) {
		return fmt.Errorf("%w: %s", ErrInvalidRef, prefix)
	}
	if strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("%w: %s", ErrInvalidRef, prefix)
	}
	return nil
}

func validateFilePath(filePath string) error {
	if filePath == "" || !utf8.ValidString(filePath) {
		return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
	}
	if path.IsAbs(filePath) || path.Clean(filePath) != filePath || strings.HasPrefix(filePath, "../") || strings.Contains(filePath, "/../") {
		return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
	}
	if filePath == "." || filePath == ".." || filePath == ".git" || strings.HasPrefix(filePath, ".git/") {
		return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
	}
	if strings.ContainsAny(filePath, "\\:,") {
		return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
	}
	// NUL and every other control character are rejected by the IsControl scan.
	for _, r := range filePath {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
		}
	}
	for _, segment := range strings.Split(filePath, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return fmt.Errorf("%w: %s", ErrInvalidPath, filePath)
		}
	}
	return nil
}

func validateOID(oid string) error {
	if len(oid) != 40 && len(oid) != 64 {
		return fmt.Errorf("want 40 or 64 hex characters")
	}
	for _, r := range oid {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("want lowercase hex")
		}
	}
	return nil
}

func (s Store) git(ctx context.Context, extraEnv []string, stdin *bytes.Reader, args ...string) (string, error) {
	out, err := s.gitBytes(ctx, extraEnv, stdin, args...)
	return string(out), err
}

func (s Store) gitBytes(ctx context.Context, extraEnv []string, stdin *bytes.Reader, args ...string) ([]byte, error) {
	gitPath := s.GitPath
	if gitPath == "" {
		gitPath = defaultGit
	}
	cmd := exec.CommandContext(ctx, gitPath, args...)
	if s.RepoDir != "" {
		cmd.Dir = s.RepoDir
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderrBuf.String()))
	}
	return stdoutBuf.Bytes(), nil
}
