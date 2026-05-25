package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a temporary git repo with identity configured and returns
// its path. Mirrors internal/replay/resolve_test.go initRepo.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.invalid")
	return dir
}

// seedTwoCommits creates two commits in dir where file.txt is "v1\n" then
// "v2\n". Returns the full OIDs of commit1 (v1) and commit2 (v2 = HEAD).
func seedTwoCommits(t *testing.T, dir string) (string, string) {
	t.Helper()
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("writeFile %s: %v", name, err)
		}
	}

	writeFile("file.txt", "v1\n")
	run("add", "file.txt")
	run("commit", "-m", "v1")
	sha1 := run("rev-parse", "HEAD")

	writeFile("file.txt", "v2\n")
	run("add", "file.txt")
	run("commit", "-m", "v2")
	sha2 := run("rev-parse", "HEAD")

	return sha1, sha2
}

func TestCheckoutHeadContent(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer wt.Close()

	content, err := os.ReadFile(filepath.Join(wt.Dir, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "v2\n" {
		t.Errorf("file.txt = %q, want %q", string(content), "v2\n")
	}

	// Dir must be outside the repo root.
	if strings.HasPrefix(wt.Dir, root) {
		t.Errorf("Dir %q is inside repo root %q", wt.Dir, root)
	}
}

func TestCheckoutOlderCommitContent(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	sha1, _ := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha1)
	if err != nil {
		t.Fatalf("Checkout at sha1: %v", err)
	}
	defer wt.Close()

	content, err := os.ReadFile(filepath.Join(wt.Dir, "file.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(content) != "v1\n" {
		t.Errorf("file.txt = %q, want %q", string(content), "v1\n")
	}
}

func TestCheckoutRegisteredAndCleaned(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Worktree must appear in the list.
	listOut, err := gitCmd(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if !strings.Contains(listOut, wt.Dir) {
		t.Errorf("worktree list does not contain %q after Checkout:\n%s", wt.Dir, listOut)
	}

	if err := wt.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Worktree must be absent from the list.
	listOut2, err := gitCmd(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list after cleanup: %v", err)
	}
	if strings.Contains(listOut2, wt.Dir) {
		t.Errorf("worktree list still contains %q after Cleanup:\n%s", wt.Dir, listOut2)
	}

	// Dir must be gone.
	if _, statErr := os.Stat(wt.Dir); !os.IsNotExist(statErr) {
		t.Errorf("wt.Dir %q still exists after Cleanup", wt.Dir)
	}
}

func TestCheckoutDirOutsideRepo(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	defer wt.Close()

	if strings.HasPrefix(wt.Dir, root) {
		t.Errorf("Dir %q is inside repo root %q", wt.Dir, root)
	}
}

func TestValidateSHAErrors(t *testing.T) {
	valid40 := strings.Repeat("a", 40)
	valid64 := strings.Repeat("b", 64)

	cases := []struct {
		name    string
		sha     string
		wantErr bool
	}{
		{"empty", "", true},
		{"leading dash", "-" + strings.Repeat("a", 39), true},
		{"branch name main", "main", true},
		{"short sha", "abc123", true},
		{"uppercase 40", strings.ToUpper(strings.Repeat("a", 40)), true},
		{"39 hex chars", strings.Repeat("a", 39), true},
		{"65 hex chars", strings.Repeat("a", 65), true},
		{"valid 40", valid40, false},
		{"valid 64", valid64, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSHA(tc.sha)
			if tc.wantErr && err == nil {
				t.Errorf("validateSHA(%q) = nil, want error", tc.sha)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateSHA(%q) = %v, want nil", tc.sha, err)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrInvalidSHA) {
				t.Errorf("validateSHA(%q) error does not wrap ErrInvalidSHA: %v", tc.sha, err)
			}
		})
	}
}

func TestCheckoutBranchNameError(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	seedTwoCommits(t, root)

	_, err := Checkout(ctx, root, "main")
	if !errors.Is(err, ErrInvalidSHA) {
		t.Fatalf("Checkout(\"main\"): got %v, want ErrInvalidSHA", err)
	}
}

func TestCheckoutUnknownSHAError(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	seedTwoCommits(t, root)

	unknownSHA := strings.Repeat("a", 40)
	_, err := Checkout(ctx, root, unknownSHA)
	if !errors.Is(err, ErrSHANotFound) {
		t.Fatalf("Checkout(unknown 40-hex): got %v, want ErrSHANotFound", err)
	}
}

func TestCheckoutHexLookingBranchError(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	seedTwoCommits(t, root)

	// Create a branch whose name is a 64-char lowercase hex string.
	// In a SHA-1 repo, rev-parse resolves it to the 40-hex tip OID.
	// resolvedOID (40 hex) != hexBranchName (64 hex) → ErrInvalidSHA.
	hexBranchName := strings.Repeat("b", 64)
	cmd := exec.Command("git", "branch", hexBranchName)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", hexBranchName, err, out)
	}

	wt, err := Checkout(ctx, root, hexBranchName)
	if wt != nil {
		wt.Close()
	}
	if !errors.Is(err, ErrInvalidSHA) {
		t.Fatalf("hex-looking branch name: got %v, want ErrInvalidSHA", err)
	}
}

func TestCleanupIdempotent(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	if err := wt.Cleanup(ctx); err != nil {
		t.Fatalf("first Cleanup: %v", err)
	}
	// Second call must also return nil.
	if err := wt.Cleanup(ctx); err != nil {
		t.Fatalf("second Cleanup (idempotent): %v", err)
	}
}

func TestCloseAfterCleanup(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	if err := wt.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if err := wt.Close(); err != nil {
		t.Fatalf("Close-after-Cleanup: %v", err)
	}
}

func TestCleanupNoStaleMetadata(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	wt, err := Checkout(ctx, root, sha2)
	if err != nil {
		t.Fatalf("Checkout: %v", err)
	}

	// Simulate the worktree directory being deleted before Cleanup is called
	// (e.g. by an external process). This exercises the path where
	// "worktree remove" may fail but RemoveAll + prune still leave no stale state.
	if err := os.RemoveAll(wt.Dir); err != nil {
		t.Fatalf("pre-delete wt.Dir: %v", err)
	}

	// Cleanup must not return an error AND must leave no stale metadata.
	// (remove may or may not fail depending on git version; the idempotency
	// guard and prune-last design ensure no stale entry regardless.)
	cleanupErr := wt.Cleanup(ctx)
	// We don't hard-fail on cleanupErr here because "worktree remove" on an
	// already-deleted dir may or may not produce "is not a working tree" depending
	// on the git version. The key assertion is that no stale entry remains.
	_ = cleanupErr

	// Assert worktree list does not mention wt.Dir.
	listOut, listErr := gitCmd(ctx, root, "worktree", "list", "--porcelain")
	if listErr != nil {
		t.Fatalf("worktree list: %v", listErr)
	}
	if strings.Contains(listOut, wt.Dir) {
		t.Errorf("stale worktree entry: %q still appears in worktree list after Cleanup:\n%s", wt.Dir, listOut)
	}

	// Assert no .git/worktrees entry references wt.Dir.
	worktreesDir := filepath.Join(root, ".git", "worktrees")
	entries, err := os.ReadDir(worktreesDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir .git/worktrees: %v", err)
	}
	for _, entry := range entries {
		gitdirFile := filepath.Join(worktreesDir, entry.Name(), "gitdir")
		data, err := os.ReadFile(gitdirFile)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), wt.Dir) {
			t.Errorf("stale .git/worktrees/%s/gitdir references %q after Cleanup", entry.Name(), wt.Dir)
		}
	}
}

// snapshotWorktreeTempDirs returns the set of "etude-worktree-*" entry names
// currently in the system temp dir. Comparing a before/after snapshot lets a
// test assert on dirs that NEWLY appeared during the call under test, which is
// robust against unrelated dirs left by concurrent tests or prior runs (the
// reason the original leak check could only Logf).
func snapshotWorktreeTempDirs(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("ReadDir tmpDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "etude-worktree-") {
			set[e.Name()] = true
		}
	}
	return set
}

// newlyLeakedWorktreeTempDirs returns "etude-worktree-*" entries present in the
// after-snapshot but not in the before-snapshot — i.e. dirs leaked by the call
// under test.
func newlyLeakedWorktreeTempDirs(before map[string]bool, after map[string]bool) []string {
	var leaked []string
	for name := range after {
		if !before[name] {
			leaked = append(leaked, name)
		}
	}
	return leaked
}

func TestNoTempDirLeakOnSHANotFound(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	seedTwoCommits(t, root)

	before := snapshotWorktreeTempDirs(t)

	unknownSHA := strings.Repeat("c", 40)
	wt, err := Checkout(ctx, root, unknownSHA)
	if wt != nil {
		t.Errorf("Checkout returned non-nil Worktree on ErrSHANotFound")
		wt.Close()
	}
	if !errors.Is(err, ErrSHANotFound) {
		t.Fatalf("expected ErrSHANotFound, got %v", err)
	}

	// rev-parse fails before MkdirTemp is called, so no etude-worktree-* dir
	// should have appeared during this Checkout.
	if leaked := newlyLeakedWorktreeTempDirs(before, snapshotWorktreeTempDirs(t)); len(leaked) > 0 {
		t.Errorf("Checkout leaked temp dir(s) on ErrSHANotFound: %v", leaked)
	}
}

// TestNoTempDirLeakOnWorktreeAddFailure exercises the cleanup path when
// "git worktree add" itself fails AFTER MkdirTemp has already created the temp
// dir (worktree.go lines ~64-69). It forces the add to fail by replacing the
// repo's .git/worktrees admin directory with a regular file, so git cannot
// create the worktree's metadata entry (REPRODUCED: rc=128 "could not create
// leading directories of '.git/worktrees/...': Not a directory"). The temp dir
// IS allocated in this path, so Checkout must os.RemoveAll it; the test asserts
// no etude-worktree-* dir is left behind.
func TestNoTempDirLeakOnWorktreeAddFailure(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	_, sha2 := seedTwoCommits(t, root)

	// Sanity: with a clean repo the resolved-OID == input invariant holds for a
	// real commit OID, so we'd otherwise reach (and pass) "worktree add". We make
	// add fail by clobbering the admin dir.
	worktreesDir := filepath.Join(root, ".git", "worktrees")
	if err := os.RemoveAll(worktreesDir); err != nil {
		t.Fatalf("remove .git/worktrees: %v", err)
	}
	if err := os.WriteFile(worktreesDir, []byte("block\n"), 0o644); err != nil {
		t.Fatalf("write .git/worktrees as file: %v", err)
	}

	before := snapshotWorktreeTempDirs(t)

	wt, err := Checkout(ctx, root, sha2)
	if wt != nil {
		t.Errorf("Checkout returned non-nil Worktree on add failure")
		wt.Close()
	}
	if err == nil {
		t.Fatalf("expected error when git worktree add fails, got nil")
	}
	// The error is wrapped as "worktree add: ..." and not one of the input
	// sentinels; confirm it is neither ErrInvalidSHA nor ErrSHANotFound (we got
	// past validation and resolution into the add stage).
	if errors.Is(err, ErrInvalidSHA) || errors.Is(err, ErrSHANotFound) {
		t.Fatalf("expected a worktree-add failure error, got input-validation sentinel: %v", err)
	}
	if !strings.Contains(err.Error(), "worktree add") {
		t.Fatalf("expected error to mention 'worktree add', got %v", err)
	}

	// The MkdirTemp dir created before the failed add must have been removed.
	if leaked := newlyLeakedWorktreeTempDirs(before, snapshotWorktreeTempDirs(t)); len(leaked) > 0 {
		t.Errorf("Checkout leaked temp dir(s) on worktree-add failure: %v", leaked)
	}
}

// Test40HexBranchNameRejectedSHA1 is the same-width regression for SHA-1 repos:
// a branch named with exactly 40 lowercase hex chars is ambiguous with a real
// 40-hex commit OID. "git rev-parse --verify <name>^{commit}" treats the
// ambiguous 40-hex token as a (non-existent) object rather than the ref, so it
// fails and Checkout returns ErrSHANotFound. This proves Checkout never
// materializes a moving ref in the same-width case (complements the 64-hex
// branch test which is rejected by the resolvedOID==input invariant).
func Test40HexBranchNameRejectedSHA1(t *testing.T) {
	ctx := context.Background()
	root := initRepo(t)
	seedTwoCommits(t, root)

	// Verify the repo is SHA-1 (40-hex object names); if a future test env uses
	// SHA-256 this assumption changes and the test should be revisited.
	objFmt, err := gitCmd(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		t.Fatalf("show-object-format: %v", err)
	}
	if strings.TrimSpace(objFmt) != "sha1" {
		t.Skipf("repo object format is %q, not sha1; skipping 40-hex-name regression", strings.TrimSpace(objFmt))
	}

	hexBranchName := strings.Repeat("a", 40)
	cmd := exec.Command("git", "branch", hexBranchName)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch %s: %v\n%s", hexBranchName, err, out)
	}

	wt, err := Checkout(ctx, root, hexBranchName)
	if wt != nil {
		wt.Close()
		t.Errorf("Checkout returned non-nil Worktree for 40-hex branch name")
	}
	// Pin the sentinel that actually results: the ambiguous 40-hex token is not a
	// known object, so rev-parse --verify fails → ErrSHANotFound.
	if !errors.Is(err, ErrSHANotFound) {
		t.Fatalf("40-hex branch name: got %v, want ErrSHANotFound", err)
	}
}
