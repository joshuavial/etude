package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
)

// executeReplay runs the replay command with a pre-injected runner. It mirrors
// the execute() helper but wires a custom replayRunner instead of a plain one.
func executeReplay(stub *replay.StubRunner, args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &replayRunner{runner: stub}
	cmd := buildReplayCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

// captureStageForReplay creates a temp repo, captures one stage with a
// real HEAD commit (so the worktree checkout works), and returns the
// repo path and run id.
func captureStageForReplay(t *testing.T) (repo, runID string) {
	t.Helper()
	repo = initCaptureRepo(t)
	writeFile(t, repo, "input.txt", "hello world\n")
	writeFile(t, repo, "output.md", "# result\n")
	chdir(t, repo)

	runID = "replay-test-run"
	_, stderr, err := execute(
		"capture", "gen",
		"--run", runID,
		"--input", "prompt=input.txt",
		"--output", "output=output.md",
	)
	if err != nil {
		t.Fatalf("capture setup returned error: %v\nstderr: %s", err, stderr)
	}
	return repo, runID
}

// TestReplayEmitsOutputToStdout captures a real stage then replays it with a
// StubRunner and asserts the output reaches stdout.
func TestReplayEmitsOutputToStdout(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	want := []byte("stub output for replay")
	stub := &replay.StubRunner{CannedOutput: want}

	stdout, stderr, err := executeReplay(stub, runID, "gen")
	if err != nil {
		t.Fatalf("replay returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("replay wrote to stderr: %q", stderr)
	}
	if stdout != string(want) {
		t.Fatalf("stdout = %q, want %q", stdout, string(want))
	}
}

// TestReplayEmitsOutputToFile verifies --output writes to a file and prints a
// confirmation to stdout.
func TestReplayEmitsOutputToFile(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	want := []byte("file output content")
	stub := &replay.StubRunner{CannedOutput: want}
	outFile := filepath.Join(t.TempDir(), "replay-out.txt")

	stdout, stderr, err := executeReplay(stub, runID, "gen", "--output", outFile)
	if err != nil {
		t.Fatalf("replay returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("replay wrote to stderr: %q", stderr)
	}
	if !strings.Contains(stdout, outFile) {
		t.Fatalf("stdout %q does not mention output file %q", stdout, outFile)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file contents = %q, want %q", got, want)
	}
}

// TestReplayCleanupAfterRun verifies that the worktree and scratch dirs are
// removed after a successful replay.
func TestReplayCleanupAfterRun(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	var capturedWorktreeDir, capturedScratchDir string
	stub := &replay.StubRunner{CannedOutput: []byte("out")}

	// Wrap StubRunner to capture dirs from the RunRequest.
	dirCapture := &dirCapturingRunner{
		inner: stub,
		capture: func(req replay.RunRequest) {
			capturedWorktreeDir = req.WorktreeDir
			capturedScratchDir = req.ScratchDir
		},
	}

	r := &replayRunner{runner: dirCapture}
	var out, errOut bytes.Buffer
	cmd := buildReplayCommand(&out, &errOut, r)
	cmd.SetArgs([]string{runID, "gen"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("replay returned error: %v\nstderr: %s", err, errOut.String())
	}

	if capturedWorktreeDir == "" {
		t.Fatal("worktree dir was not captured")
	}
	if capturedScratchDir == "" {
		t.Fatal("scratch dir was not captured")
	}
	if _, err := os.Stat(capturedWorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %q still exists after replay", capturedWorktreeDir)
	}
	if _, err := os.Stat(capturedScratchDir); !os.IsNotExist(err) {
		t.Fatalf("scratch dir %q still exists after replay", capturedScratchDir)
	}
}

// dirCapturingRunner wraps a Runner to capture the RunRequest dirs.
type dirCapturingRunner struct {
	inner   replay.Runner
	capture func(replay.RunRequest)
}

func (d *dirCapturingRunner) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	d.capture(req)
	return d.inner.Run(ctx, req)
}

// TestReplayRunnerFailure verifies that a StubRunner error propagates cleanly.
func TestReplayRunnerFailure(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	runErr := errors.New("runner exploded")
	stub := &replay.StubRunner{Err: runErr}

	_, stderr, err := executeReplay(stub, runID, "gen")
	if err == nil {
		t.Fatal("replay returned nil error, want runner error")
	}
	if !errors.Is(err, runErr) && !strings.Contains(stderr, runErr.Error()) {
		t.Fatalf("error %v stderr %q do not contain runner error", err, stderr)
	}
}

// TestReplayUnknownRun verifies a friendly error for an unknown run id.
func TestReplayUnknownRun(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	_, stderr, err := executeReplay(stub, "no-such-run", "gen")
	if err == nil {
		t.Fatal("replay returned nil error, want run-not-found error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "not found") && !strings.Contains(combined, "no-such-run") {
		t.Fatalf("error %q stderr %q do not indicate run not found", err, stderr)
	}
}

// TestReplayUnknownStage verifies a friendly error listing available stages.
func TestReplayUnknownStage(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	_, stderr, err := executeReplay(stub, runID, "no-such-stage")
	if err == nil {
		t.Fatal("replay returned nil error, want stage-not-found error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "not found") {
		t.Fatalf("error %q stderr %q do not indicate stage not found", err, stderr)
	}
	// The error should also list available stages.
	if !strings.Contains(combined, "gen") {
		t.Fatalf("error %q stderr %q do not list available stages (want 'gen')", err, stderr)
	}
}

// TestReplaySHANotInRepo verifies a friendly error when the stage's recorded
// git SHA is valid but not present in the repository. The git sha guard in
// replay.go (empty-sha check) is a defensive belt-and-suspenders guard that
// is unreachable via valid manifests (manifest.Validate() rejects empty SHA
// before ResolveInputs returns), so this test exercises the next failure mode:
// a syntactically valid SHA that is absent from the repo.
func TestReplaySHANotInRepo(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	// Overwrite the run's manifest with one that records a real-looking but
	// absent SHA, bypassing normal capture flow.
	fakeSHA := strings.Repeat("a", 40) // syntactically valid, not in repo

	// Read the existing manifest to extract artifact paths and hashes.
	manifest := readRunManifest(t, repo, runID)
	stage := manifest.Stages[0]

	// Patch git_sha and re-write via the JSON layer.
	stage.GitSHA = fakeSHA
	manifest.Stages[0] = stage
	manifestBytes, err := manifest.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	// Read existing artifact files so the commit is consistent.
	store := refstore.New(repo)
	files := map[string][]byte{}
	for _, stage := range manifest.Stages {
		for _, input := range stage.Inputs {
			content, err := store.ReadFile(context.Background(), "refs/etude/runs/"+runID, input.Path)
			if err != nil {
				t.Fatalf("read input artifact: %v", err)
			}
			files[input.Path] = content
		}
		content, err := store.ReadFile(context.Background(), "refs/etude/runs/"+runID, stage.Output.Path)
		if err != nil {
			t.Fatalf("read output artifact: %v", err)
		}
		files[stage.Output.Path] = content
	}
	files["manifest.json"] = manifestBytes

	existing, err := store.Resolve(context.Background(), "refs/etude/runs/"+runID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, err = store.WriteCommit(
		context.Background(),
		"refs/etude/runs/"+runID,
		files,
		refstore.WriteOptions{ExpectedOld: existing},
	)
	if err != nil {
		t.Fatalf("write patched manifest: %v", err)
	}

	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	_, stderr, replayErr := executeReplay(stub, runID, "gen")
	if replayErr == nil {
		t.Fatal("replay returned nil error, want SHA-not-found error")
	}
	combined := replayErr.Error() + " " + stderr
	if !strings.Contains(combined, "not found") {
		t.Fatalf("error %q stderr %q do not indicate SHA not found", replayErr, stderr)
	}
}

// TestReplayNoRunnerConfigured verifies a friendly error when no runner is
// available (no --runner flag, no injected runner, no git config).
func TestReplayNoRunnerConfigured(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	// Use execute() (no injection) so the code tries to resolve a real runner.
	// No etude.runner in git config and no --runner flag.
	_, stderr, err := execute("replay", runID, "gen")
	if err == nil {
		t.Fatal("replay returned nil error, want no-runner-configured error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "no runner configured") {
		t.Fatalf("error %q stderr %q do not indicate missing runner", err, stderr)
	}
}

// TestReplayAmbiguousStage verifies a friendly error when a run has two stages
// with the same name. We create this by capturing the same stage name twice.
func TestReplayAmbiguousStage(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "content\n")
	chdir(t, repo)

	runID := "ambiguous-stage-run"
	// Capture "gen" twice into the same run — both succeed, producing a manifest
	// with two stages named "gen".
	if _, stderr, err := execute("capture", "gen", "--run", runID, "--output", "output=out.md", "--produced-by", "original"); err != nil {
		t.Fatalf("first capture: %v\nstderr: %s", err, stderr)
	}
	if _, stderr, err := execute("capture", "gen", "--run", runID, "--output", "output=out.md", "--produced-by", "retry"); err != nil {
		t.Fatalf("second capture: %v\nstderr: %s", err, stderr)
	}

	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	_, stderr, err := executeReplay(stub, runID, "gen")
	if err == nil {
		t.Fatal("replay returned nil error, want ambiguous-stage error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "ambiguous") {
		t.Fatalf("error %q stderr %q do not indicate ambiguous stage", err, stderr)
	}
}

// TestReplayPointerInputViaResolveInputs verifies a friendly error for pointer
// artifacts.
//
// NOTE: It is not possible to create a pointer artifact via `etude capture`
// (capture only supports file-backed artifacts). This test therefore writes a
// manifest directly using the correct JSON wire format ("stage" key, 64-char
// SHA-256 artifact hashes, correct path prefixes, "pointer" storage type) and
// verifies both the low-level ReadContent sentinel and the CLI error message.
func TestReplayPointerInputViaResolveInputs(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	head := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))

	// Valid 64-char lowercase hex SHA-256 values for the two artifact refs.
	pointerSum := strings.Repeat("a", 64)
	outputSum := strings.Repeat("b", 64)

	// The manifest wire format uses "stage" (not "name"), "storage": "pointer" /
	// "content", and paths that match expectedArtifactPath().
	runID := "pointer-input-run"
	manifestJSON := fmt.Sprintf(`{
  "manifest_version": 2,
  "run_id": %q,
  "workflow": "manual",
  "workflow_version": "manual-v1",
  "created": "2026-01-01T00:00:00Z",
  "refs": {},
  "stages": [{
    "stage": "gen",
    "produced_by": "original",
    "git_sha": %q,
    "producer": {"skill": {"id": "gen", "repo": "manual", "version": "manual"}},
    "inputs": [{
      "role": "prompt",
      "artifact": %q,
      "path": "artifacts/pointers/sha256/%s/%s.json",
      "media_type": "text/plain; charset=utf-8",
      "storage": "pointer",
      "size": 5
    }],
    "output": {
      "role": "output",
      "artifact": %q,
      "path": "artifacts/sha256/%s/%s",
      "media_type": "text/plain; charset=utf-8",
      "storage": "content",
      "size": 3
    },
    "timestamp": "2026-01-01T00:00:00Z"
  }]
}`,
		runID,
		head,
		pointerSum, pointerSum[:2], pointerSum,
		outputSum, outputSum[:2], outputSum,
	)

	store := refstore.New(repo)
	_, err := store.WriteCommit(
		context.Background(),
		"refs/etude/runs/"+runID,
		map[string][]byte{"manifest.json": []byte(manifestJSON)},
		refstore.WriteOptions{},
	)
	if err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Verify ResolveInputs parses the manifest and ReadContent returns the
	// pointer sentinel rather than trying to fetch the non-existent content.
	resolved, err := replay.ResolveInputs(context.Background(), store, runID, "gen")
	if err != nil {
		t.Fatalf("ResolveInputs returned error: %v", err)
	}
	if len(resolved.ResolvedInputs) != 1 {
		t.Fatalf("expected 1 resolved input, got %d", len(resolved.ResolvedInputs))
	}

	_, readErr := resolved.ResolvedInputs[0].ReadContent(context.Background())
	if !errors.Is(readErr, replay.ErrPointerNotMaterialized) {
		t.Fatalf("ReadContent returned %v, want ErrPointerNotMaterialized", readErr)
	}

	// Verify the CLI path surfaces a friendly error for this run.
	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	_, stderr, execErr := executeReplay(stub, runID, "gen")
	if execErr == nil {
		t.Fatal("replay returned nil error, want pointer-not-materialized error")
	}
	combined := execErr.Error() + " " + stderr
	if !strings.Contains(combined, "pointer artifact") && !strings.Contains(combined, "cannot be replayed") {
		t.Fatalf("error %q stderr %q do not indicate pointer artifact error", execErr, stderr)
	}
}

// TestReplayHelp verifies the --help flag does not error and mentions the command.
func TestReplayHelp(t *testing.T) {
	output, stderr, err := execute("replay", "--help")
	if err != nil {
		t.Fatalf("replay --help returned error: %v\nstderr: %s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("replay --help wrote to stderr: %q", stderr)
	}
	for _, want := range []string{"replay", "<run>", "<stage>"} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output missing %q:\n%s", want, output)
		}
	}
}
