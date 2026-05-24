package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupBareRemote creates a bare git repo, adds it as "origin" to repo,
// and pushes the initial branch so clones have a resolvable HEAD.
func setupBareRemote(t *testing.T, repo string) string {
	t.Helper()
	bare := t.TempDir()
	gitCapture(t, bare, "init", "--bare")
	gitCapture(t, repo, "remote", "add", "origin", bare)
	// Push the initial branch so clones get a resolvable HEAD.
	gitCapture(t, repo, "push", "origin", "HEAD")
	return bare
}

// capture1Stage runs capture to seed a run stage in the repo.
func capture1Stage(t *testing.T, repo, runID, stage, filename, content string) {
	t.Helper()
	writeFile(t, repo, filename, content)
	chdir(t, repo)
	_, stderr, err := execute("capture", stage, "--run", runID, "--output", "output="+filename)
	if err != nil {
		t.Fatalf("capture %s/%s failed: %v\nstderr: %s", runID, stage, err, stderr)
	}
}

// resolveRef returns the resolved OID for a ref in a git dir, or "" if not found.
func resolveRef(t *testing.T, gitDir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", gitDir, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refExists returns true if the ref resolves in the given git directory.
func refExists(t *testing.T, gitDir, ref string) bool {
	t.Helper()
	return resolveRef(t, gitDir, ref) != ""
}

// cloneFrom clones srcURL into a new tempdir, configures user, and returns the path.
func cloneFrom(t *testing.T, srcURL string) string {
	t.Helper()
	dir := t.TempDir()
	gitCapture(t, dir, "clone", srcURL, ".")
	gitCapture(t, dir, "config", "user.name", "Test User")
	gitCapture(t, dir, "config", "user.email", "test@example.invalid")
	return dir
}

// TestSyncHelp checks that sync is registered as a subcommand.
func TestSyncHelp(t *testing.T) {
	stdout, stderr, err := execute("sync", "--help")
	if err != nil {
		t.Fatalf("sync --help returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "sync") {
		t.Fatalf("sync --help output does not mention 'sync':\n%s", stdout)
	}
}

// TestSyncNotAGitRepo checks clean error outside a repo.
func TestSyncNotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync returned nil error in non-repo dir")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "not a git repository") {
		t.Fatalf("error %q does not mention 'not a git repository'", combined)
	}
}

// TestSyncInvalidRemoteName verifies validation fires before any git call.
// Run in a non-repo dir to confirm git is never invoked.
func TestSyncInvalidRemoteName(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	_, stderr, err := execute("sync", "--remote", "bad name")
	if err == nil {
		t.Fatal("sync --remote 'bad name' returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "invalid remote name") {
		t.Fatalf("expected 'invalid remote name', got %q", combined)
	}
	if strings.Contains(combined, "not a git repository") {
		t.Fatalf("validation must precede git check, got %q", combined)
	}
}

// TestSyncMissingRemote verifies that sync errors when the remote doesn't exist.
func TestSyncMissingRemote(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	// Default origin missing.
	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with no origin returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "origin") {
		t.Fatalf("error %q does not mention 'origin'", combined)
	}

	// Explicit --remote nope.
	_, stderr, err = execute("sync", "--remote", "nope")
	if err == nil {
		t.Fatal("sync --remote nope returned nil error")
	}
	combined = err.Error() + " " + stderr
	if !strings.Contains(combined, "nope") {
		t.Fatalf("error %q does not mention 'nope'", combined)
	}
}

// TestSyncEmptyNamespace: valid bare origin, no refs/etude/* → prints skip message, exit 0.
func TestSyncEmptyNamespace(t *testing.T) {
	repo := initCaptureRepo(t)
	setupBareRemote(t, repo)
	chdir(t, repo)

	stdout, stderr, err := execute("sync")
	if err != nil {
		t.Fatalf("sync (empty namespace) returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "no local refs/etude/* to push") {
		t.Fatalf("stdout = %q, want 'no local refs/etude/* to push'", stdout)
	}
}

// TestSyncPushRoundTrip: capture in A → sync → ref appears in bare remote.
func TestSyncPushRoundTrip(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)

	stdout, stderr, err := execute("sync")
	if err != nil {
		t.Fatalf("sync push round-trip error: %v\nstderr: %s", err, stderr)
	}

	if !refExists(t, bare, "refs/etude/runs/run-1") {
		t.Fatal("refs/etude/runs/run-1 not found in bare remote after sync")
	}
	if !strings.Contains(stdout, "pushed") {
		t.Fatalf("stdout %q does not mention 'pushed'", stdout)
	}
}

// TestSyncFetchIntoClone: capture in A, sync A→bare, clone B, sync B → ref resolves in B.
func TestSyncFetchIntoClone(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync A→bare error: %v\nstderr: %s", err, stderr)
	}

	repoB := cloneFrom(t, bare)
	chdir(t, repoB)

	stdout, stderr, err := execute("sync")
	if err != nil {
		t.Fatalf("sync fetch into B error: %v\nstderr: %s", err, stderr)
	}

	if !refExists(t, repoB, "refs/etude/runs/run-1") {
		t.Fatalf("refs/etude/runs/run-1 not found in B after sync; stdout=%q", stdout)
	}
}

// TestSyncFullRoundTrip: A→bare→B, both directions.
func TestSyncFullRoundTrip(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan A")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync A→bare error: %v\nstderr: %s", err, stderr)
	}

	repoB := cloneFrom(t, bare)
	chdir(t, repoB)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync bare→B fetch error: %v\nstderr: %s", err, stderr)
	}
	if !refExists(t, repoB, "refs/etude/runs/run-1") {
		t.Fatal("run-1 not in B after full round-trip")
	}

	// B captures a new stage and syncs back.
	capture1Stage(t, repoB, "run-1", "implement", "impl.md", "# impl B")
	chdir(t, repoB)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync B→bare push error: %v\nstderr: %s", err, stderr)
	}
	if !refExists(t, bare, "refs/etude/runs/run-1") {
		t.Fatal("run-1 not propagated back to bare")
	}
}

// TestSyncRemoteAheadFastForwards: push from A, then sync from B that is behind.
func TestSyncRemoteAheadFastForwards(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// A captures and pushes.
	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync A push error: %v\nstderr: %s", err, stderr)
	}

	// B clones (has run-1 from clone), then A adds another stage and pushes.
	repoB := cloneFrom(t, bare)
	chdir(t, repoB)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("first sync B error: %v\nstderr: %s", err, stderr)
	}
	bBefore := resolveRef(t, repoB, "refs/etude/runs/run-1")

	capture1Stage(t, repoA, "run-1", "implement", "impl.md", "# impl A")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync A second push error: %v\nstderr: %s", err, stderr)
	}

	// B syncs; remote is ahead → local should fast-forward.
	chdir(t, repoB)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("sync B fast-forward error: %v\nstderr: %s", err, stderr)
	}
	bAfter := resolveRef(t, repoB, "refs/etude/runs/run-1")
	if bAfter == bBefore {
		t.Fatal("B did not fast-forward to remote-ahead commit")
	}
	aOID := resolveRef(t, repoA, "refs/etude/runs/run-1")
	if bAfter != aOID {
		t.Fatalf("B run-1 = %q, want A's %q", bAfter, aOID)
	}
}

// TestSyncLocalAheadRegression: local ref must NOT be moved backward; remote must advance.
func TestSyncLocalAheadRegression(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// A captures, syncs (run-1 in bare).
	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("first sync error: %v\nstderr: %s", err, stderr)
	}

	// A adds another stage (local is now ahead of remote).
	capture1Stage(t, repoA, "run-1", "implement", "impl.md", "# impl")
	localOID := resolveRef(t, repoA, "refs/etude/runs/run-1")

	// sync again: push should advance remote, local should stay put.
	chdir(t, repoA)
	stdout, stderr, err := execute("sync")
	if err != nil {
		t.Fatalf("sync local-ahead error: %v\nstderr: %s", err, stderr)
	}

	afterOID := resolveRef(t, repoA, "refs/etude/runs/run-1")
	if afterOID != localOID {
		t.Fatalf("local ref moved: before=%q after=%q", localOID, afterOID)
	}

	remoteOID := resolveRef(t, bare, "refs/etude/runs/run-1")
	if remoteOID != localOID {
		t.Fatalf("remote did not advance: remote=%q local=%q", remoteOID, localOID)
	}
	_ = stdout
}

// TestSyncTrueDivergence: two clones diverge from a shared base; sync errors with full ref path.
func TestSyncTrueDivergence(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// Shared base: A captures plan, syncs.
	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# base")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("initial sync error: %v\nstderr: %s", err, stderr)
	}

	// B clones and syncs to get the base.
	repoB := cloneFrom(t, bare)
	chdir(t, repoB)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("B initial sync error: %v\nstderr: %s", err, stderr)
	}

	// A and B BOTH add a different stage from the same base (diverge).
	capture1Stage(t, repoA, "run-1", "implement", "impl_a.md", "# A impl")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("A push diverge error: %v\nstderr: %s", err, stderr)
	}

	capture1Stage(t, repoB, "run-1", "implement", "impl_b.md", "# B impl")
	chdir(t, repoB)
	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with diverged run-1 expected error, got nil")
	}
	combined := err.Error() + " " + stderr
	// Must name the FULL ref path.
	if !strings.Contains(combined, "refs/etude/runs/run-1") {
		t.Fatalf("divergence error %q does not contain full ref path 'refs/etude/runs/run-1'", combined)
	}
	if !strings.Contains(strings.ToLower(combined), "manual resolution") {
		t.Fatalf("divergence error %q does not mention manual resolution", combined)
	}

	// Neither side should be clobbered.
	aOID := resolveRef(t, repoA, "refs/etude/runs/run-1")
	bOID := resolveRef(t, repoB, "refs/etude/runs/run-1")
	bareOID := resolveRef(t, bare, "refs/etude/runs/run-1")
	if bareOID == bOID {
		t.Fatal("bare was clobbered by B's diverged commit")
	}
	if aOID != bareOID {
		t.Fatalf("bare advanced unexpectedly: bare=%q A=%q", bareOID, aOID)
	}
}

// TestSyncDivergenceEvalsFullPath: diverged evals ref must also show full path.
// Tests that divergence messages use the full ref path (not hard-coded runs/).
func TestSyncDivergenceEvalsFullPath(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// A creates a base eval-1, syncs to bare.
	seedEvalRef(t, repoA, "eval-1", "initial eval")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("initial sync evals error: %v\nstderr: %s", err, stderr)
	}
	baseOID := resolveRef(t, bare, "refs/etude/evals/eval-1")

	// B clones and fetches etude refs so it has eval-1 at the base.
	repoB := cloneFrom(t, bare)
	gitCapture(t, repoB, "fetch", "origin", "refs/etude/*:refs/etude/*")

	// A diverges: create a new commit parented at base, update A's eval-1.
	divergeEvalRef(t, repoA, "eval-1", baseOID, "eval_a.txt", "A diverged")

	// A syncs → remote advances to A's commit.
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("A push diverge error: %v\nstderr: %s", err, stderr)
	}

	// B diverges: create a different new commit also parented at base.
	divergeEvalRef(t, repoB, "eval-1", baseOID, "eval_b.txt", "B diverged")

	// B syncs → push must be rejected with the full eval ref path.
	chdir(t, repoB)
	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with diverged eval-1 expected error, got nil")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "refs/etude/evals/eval-1") {
		t.Fatalf("divergence error %q does not contain 'refs/etude/evals/eval-1'", combined)
	}

	// Neither side should be clobbered (mirrors TestSyncTrueDivergence).
	aOID := resolveRef(t, repoA, "refs/etude/evals/eval-1")
	bOID := resolveRef(t, repoB, "refs/etude/evals/eval-1")
	bareOID := resolveRef(t, bare, "refs/etude/evals/eval-1")
	if bareOID == bOID {
		t.Fatal("bare eval ref was clobbered by B's diverged commit")
	}
	if aOID != bareOID {
		t.Fatalf("bare eval ref advanced unexpectedly: bare=%q A=%q", bareOID, aOID)
	}
}

// TestSyncRealFailureFetch: bad/nonexistent remote URL → fetch exit 128 → abort, no push.
func TestSyncRealFailureFetch(t *testing.T) {
	repo := initCaptureRepo(t)
	// Set origin to a nonexistent path.
	nonexistent := filepath.Join(t.TempDir(), "no-such-remote.git")
	gitCapture(t, repo, "remote", "add", "origin", nonexistent)

	capture1Stage(t, repo, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repo)

	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with unreachable remote returned nil error")
	}
	combined := err.Error() + " " + stderr
	// Should surface git's error, NOT a push error.
	if strings.Contains(combined, "push") && !strings.Contains(combined, "fetch") {
		t.Fatalf("error should be a fetch failure, not push: %q", combined)
	}
}

// TestSyncLockInducedRejectedFF: plant a .lock file → sync aborts as real failure.
func TestSyncLockInducedRejectedFF(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// A captures and pushes run-1.
	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("initial sync error: %v\nstderr: %s", err, stderr)
	}
	preFetchOID := resolveRef(t, repoA, "refs/etude/runs/run-1")

	// Remote advances (bare gets a new commit directly).
	seedRunRefInBare(t, bare, "run-1")

	// Plant a lock file for the ref in A's .git.
	lockPath := filepath.Join(repoA, ".git", "refs", "etude", "runs", "run-1.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir lock dir: %v", err)
	}
	if err := os.WriteFile(lockPath, []byte("locked"), 0o644); err != nil {
		t.Fatalf("write lock file: %v", err)
	}
	t.Cleanup(func() { os.Remove(lockPath) })

	// Deterministic no-push assertion: a client-side pre-push hook that records
	// it ran. A correct sync ABORTS at the fetch failure and never pushes, so
	// this hook must NOT run. (A buggy sync that ignored the fetch failure and
	// reached the non-forced push would also error with the local ref intact,
	// so the local-ref check alone cannot prove the abort happened at fetch.)
	sentinel := filepath.Join(t.TempDir(), "pushed.sentinel")
	hookPath := filepath.Join(repoA, ".git", "hooks", "pre-push")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\ntouch \""+sentinel+"\"\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pre-push hook: %v", err)
	}

	chdir(t, repoA)
	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with lock file expected error, got nil")
	}

	// The push must never have been attempted (sync aborts at the fetch failure).
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatal("push was attempted despite the fetch failure (pre-push sentinel created)")
	}

	// The error must be the fetch failure, not a push error.
	combined := strings.ToLower(err.Error() + " " + stderr)
	if !strings.Contains(combined, "cannot lock") && !strings.Contains(combined, "fetch failed") {
		t.Fatalf("error should be a fetch failure (cannot lock), got: %q", err.Error()+" "+stderr)
	}

	// Local ref must be unchanged.
	afterOID := resolveRef(t, repoA, "refs/etude/runs/run-1")
	if afterOID != preFetchOID {
		t.Fatalf("local ref changed despite lock: before=%q after=%q", preFetchOID, afterOID)
	}
}

// TestSyncGenericPushRejection: a remote pre-receive hook that rejects the push
// must surface as a GENERIC push failure (with git's stderr), NOT be
// misclassified as a divergence. Covers the non-divergence `!` push path.
func TestSyncGenericPushRejection(t *testing.T) {
	repoA := initCaptureRepo(t)
	bare := setupBareRemote(t, repoA)

	// Reject every push at the remote.
	hookPath := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("mkdir bare hooks: %v", err)
	}
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}

	capture1Stage(t, repoA, "run-1", "plan", "plan.md", "# plan")
	chdir(t, repoA)
	_, stderr, err := execute("sync")
	if err == nil {
		t.Fatal("sync with rejecting pre-receive hook expected error, got nil")
	}
	combined := strings.ToLower(err.Error() + " " + stderr)
	if !strings.Contains(combined, "push failed") {
		t.Fatalf("expected a generic push failure, got: %q", err.Error()+" "+stderr)
	}
	if strings.Contains(combined, "diverged") {
		t.Fatalf("remote-hook rejection must NOT be classified as divergence: %q", err.Error()+" "+stderr)
	}
}

// ---------------------------------------------------------------------------
// Helpers for seeding eval refs and bare-remote run refs directly.
// ---------------------------------------------------------------------------

// seedEvalRef creates refs/etude/evals/<id> in repo using a capture then aliasing.
// The underlying runs ref (refs/etude/runs/eval-run-<id>) is left in place.
func seedEvalRef(t *testing.T, repo, id, content string) {
	t.Helper()
	fname := "eval_" + id + ".txt"
	writeFile(t, repo, fname, content)
	chdir(t, repo)
	_, stderr, err := execute("capture", "plan", "--run", "eval-run-"+id, "--output", "output="+fname)
	if err != nil {
		t.Fatalf("seedEvalRef capture failed: %v\nstderr: %s", err, stderr)
	}
	oid := resolveRef(t, repo, "refs/etude/runs/eval-run-"+id)
	if oid == "" {
		t.Fatalf("seedEvalRef: refs/etude/runs/eval-run-%s not found after capture", id)
	}
	// Point evals/<id> at the same commit.
	gitCapture(t, repo, "update-ref", "refs/etude/evals/"+id, oid)
}

// divergeEvalRef creates a new git commit parented at parentOID, writes filename
// with content as a blob, and updates refs/etude/evals/<id> to point at it.
// This creates a true divergence when two repos each call divergeEvalRef from
// the same parentOID with different filenames/contents.
func divergeEvalRef(t *testing.T, repo, id, parentOID, filename, content string) {
	t.Helper()
	// Write the file and make a regular git commit, then use its tree for an
	// orphan-style ref update. Simpler: just append another capture stage to
	// the eval-run-<id> runs ref (which starts at parentOID).

	// First, reset the runs ref to parentOID so we build off the shared base.
	gitCapture(t, repo, "update-ref", "refs/etude/runs/eval-run-"+id, parentOID)

	fname := filename
	writeFile(t, repo, fname, content)
	chdir(t, repo)
	_, stderr, err := execute("capture", "implement", "--run", "eval-run-"+id, "--output", "output="+fname)
	if err != nil {
		t.Fatalf("divergeEvalRef capture failed: %v\nstderr: %s", err, stderr)
	}
	oid := resolveRef(t, repo, "refs/etude/runs/eval-run-"+id)
	if oid == "" {
		t.Fatalf("divergeEvalRef: refs/etude/runs/eval-run-%s not found", id)
	}
	// Update the eval ref to this new child commit.
	gitCapture(t, repo, "update-ref", "refs/etude/evals/"+id, oid)
}

// TestClassifyFetchBangAbortSurfacesStderr verifies that classifyFetchBang returns
// fetchBangAbort with git's stderr (e.g. "Not a valid commit name") when merge-base
// exits with a non-0/1 code (e.g. 128 from a bogus OID). It also confirms that
// the benign (exit 1) path does not leak any stderr into the error.
func TestClassifyFetchBangAbortSurfacesStderr(t *testing.T) {
	repo := initCaptureRepo(t)

	// Resolve HEAD so we have a valid resolvable OID for bl.old.
	headOID := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	if headOID == "" {
		t.Fatal("could not resolve HEAD OID")
	}

	// Construct a bogus OID: same length as headOID, syntactically valid hex,
	// but unresolvable — all d's except last char is 1 (avoids null OID guard).
	bogusOID := strings.Repeat("d", len(headOID)-1) + "1"

	// Abort path: bl.new is bogus → merge-base exits 128 with "Not a valid commit name".
	bl := bangLine{old: headOID, new: bogusOID, ref: "refs/etude/runs/abort-1"}
	result, err := classifyFetchBang(context.Background(), repo, bl)

	if result != fetchBangAbort {
		t.Errorf("abort path: got result %v, want fetchBangAbort", result)
	}
	if err == nil {
		t.Fatal("abort path: expected non-nil error, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "refs/etude/runs/abort-1") {
		t.Errorf("abort path: error %q does not contain full ref path", errStr)
	}
	if !strings.Contains(errStr, "merge-base exit 128") {
		t.Errorf("abort path: error %q does not contain 'merge-base exit 128'", errStr)
	}
	if !strings.Contains(strings.ToLower(errStr), "not a valid commit name") {
		t.Errorf("abort path: error %q does not contain git's stderr 'not a valid commit name'", errStr)
	}

	// Benign path: use two commits where old is NOT an ancestor of new.
	// Add a second commit to the repo.
	writeFile(t, repo, "extra.txt", "x")
	gitCapture(t, repo, "add", "extra.txt")
	gitCapture(t, repo, "commit", "-m", "second")
	secondOID := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	firstOID := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD~1"))

	// git merge-base --is-ancestor secondOID firstOID exits 1:
	// secondOID is NOT an ancestor of firstOID (it's the child, not parent).
	blBenign := bangLine{old: secondOID, new: firstOID, ref: "refs/etude/runs/benign-1"}
	resultBenign, errBenign := classifyFetchBang(context.Background(), repo, blBenign)

	if resultBenign != fetchBangBenign {
		t.Errorf("benign path: got result %v, want fetchBangBenign", resultBenign)
	}
	if errBenign != nil {
		t.Errorf("benign path: expected nil error, got %v", errBenign)
	}
}

// seedRunRefInBare creates a new commit directly in the bare repo advancing runID.
// This simulates "remote is ahead" by adding a commit that does not exist locally.
func seedRunRefInBare(t *testing.T, bare, runID string) {
	t.Helper()
	// Use a scratch clone, add a capture commit, sync back to bare.
	scratch := t.TempDir()
	gitCapture(t, scratch, "clone", bare, ".")
	gitCapture(t, scratch, "config", "user.name", "Test User")
	gitCapture(t, scratch, "config", "user.email", "test@example.invalid")
	// Fetch the etude refs into scratch.
	gitCapture(t, scratch, "fetch", "origin", "refs/etude/*:refs/etude/*")
	// Add a new stage to the runID.
	capture1Stage(t, scratch, runID, "implement", "remote_impl.md", "# remote impl")
	// Push to bare via sync.
	chdir(t, scratch)
	if _, stderr, err := execute("sync"); err != nil {
		t.Fatalf("seedRunRefInBare sync error: %v\nstderr: %s", err, stderr)
	}
}
