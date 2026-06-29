package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/liverun"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/workflow"
)

// executeReplay runs the replay command with a pre-injected single-stage runner.
func executeReplay(stub *replay.StubRunner, args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &replayRunner{runner: stub, now: time.Now}
	cmd := buildReplayCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

// executeReplayForward runs the forward replay (1-arg) with a pre-injected
// forwardRunner so tests don't need workflow.yaml or registry.yaml.
func executeReplayForward(stub *replay.StubRunner, args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &replayRunner{forwardRunner: stub, now: time.Now}
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

// ---------------------------------------------------------------------------
// --record tests
// ---------------------------------------------------------------------------

// executeReplayWithClock builds a replayRunner with an injected stub and fixed
// clock, then runs the replay command with the given args.
func executeReplayWithClock(stub *replay.StubRunner, fixedTime time.Time, args ...string) (string, string, error) {
	var out bytes.Buffer
	var errOut bytes.Buffer
	r := &replayRunner{runner: stub, now: func() time.Time { return fixedTime }}
	cmd := buildReplayCommand(&out, &errOut, r)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(&errOut, err)
	}
	return out.String(), errOut.String(), err
}

// TestReplayRecordCreatesLinkedRun is the acceptance round-trip test.
// It captures a real stage, replays with --record, then asserts:
//   - a new run ref exists with the expected id
//   - the stage has produced_by:replay + ReplayOf populated
//   - ReplayOf.Commit matches the source's resolved commit
//   - Stage.Skill mirrors Producer.Skill
//   - output artifact bytes are persisted
//   - source run is unmodified
func TestReplayRecordCreatesLinkedRun(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	// Resolve the source run commit for later assertions.
	sourceCommit, err := refstore.New(repo).Resolve(context.Background(), "refs/etude/runs/"+sourceRunID)
	if err != nil {
		t.Fatalf("resolve source run: %v", err)
	}

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	wantRunID := sourceRunID + "-replay-20260522T103000Z"
	wantOutput := []byte("recorded-replay-output")
	stub := &replay.StubRunner{CannedOutput: wantOutput}

	stdout, stderr, err := executeReplayWithClock(stub, fixedTime, sourceRunID, "gen", "--record")
	if err != nil {
		t.Fatalf("replay --record returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, wantRunID) {
		t.Fatalf("stdout %q does not contain replay run id %q", stdout, wantRunID)
	}

	// Assert the new run ref exists.
	replayRunRef := "refs/etude/runs/" + wantRunID
	store := refstore.New(repo)
	_, err = store.Resolve(context.Background(), replayRunRef)
	if err != nil {
		t.Fatalf("replay run ref %q not found: %v", replayRunRef, err)
	}

	// Parse and inspect the replay manifest.
	replayManifest := readRunManifest(t, repo, wantRunID)
	if len(replayManifest.Stages) != 1 {
		t.Fatalf("replay run has %d stages, want 1", len(replayManifest.Stages))
	}
	s := replayManifest.Stages[0]

	if s.ProducedBy != "replay" {
		t.Fatalf("ProducedBy = %q, want %q", s.ProducedBy, "replay")
	}
	if s.ReplayOf == nil {
		t.Fatal("ReplayOf is nil, want non-nil")
	}
	if s.ReplayOf.RunID != sourceRunID {
		t.Fatalf("ReplayOf.RunID = %q, want %q", s.ReplayOf.RunID, sourceRunID)
	}
	if s.ReplayOf.Stage != "gen" {
		t.Fatalf("ReplayOf.Stage = %q, want %q", s.ReplayOf.Stage, "gen")
	}
	if s.ReplayOf.Commit != sourceCommit {
		t.Fatalf("ReplayOf.Commit = %q, want %q", s.ReplayOf.Commit, sourceCommit)
	}

	// Stage.Skill must mirror Producer.Skill (codex change #1).
	if s.Skill != s.Producer.Skill {
		t.Fatalf("Stage.Skill %+v != Producer.Skill %+v", s.Skill, s.Producer.Skill)
	}
	if s.Skill.ID == "" {
		t.Fatal("Stage.Skill.ID is empty")
	}

	// Output artifact must be persisted in the replay run.
	outputBytes, err := store.ReadFile(context.Background(), replayRunRef, s.Output.Path)
	if err != nil {
		t.Fatalf("read replay output artifact: %v", err)
	}
	if !bytes.Equal(outputBytes, wantOutput) {
		t.Fatalf("replay output bytes = %q, want %q", outputBytes, wantOutput)
	}

	// Source run must be unmodified (still has exactly 1 stage named "gen").
	sourceManifest := readRunManifest(t, repo, sourceRunID)
	if len(sourceManifest.Stages) != 1 {
		t.Fatalf("source run has %d stages after replay, want 1", len(sourceManifest.Stages))
	}
	if sourceManifest.Stages[0].Name != "gen" {
		t.Fatalf("source stage name = %q, want %q", sourceManifest.Stages[0].Name, "gen")
	}

	// Round-trip: ResolveInputs(ReplayOf.RunID, ReplayOf.Stage) resolves the source.
	resolved, err := replay.ResolveInputs(context.Background(), store, s.ReplayOf.RunID, s.ReplayOf.Stage)
	if err != nil {
		t.Fatalf("ResolveInputs(source via ReplayOf) returned error: %v", err)
	}
	if resolved.Name != "gen" {
		t.Fatalf("resolved source stage Name = %q, want gen", resolved.Name)
	}
}

// TestReplayRecordNoOutputError verifies that --record with an empty output errors.
func TestReplayRecordNoOutputError(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	stub := &replay.StubRunner{CannedOutput: nil} // empty output
	_, stderr, err := executeReplayWithClock(stub, fixedTime, runID, "gen", "--record")
	if err == nil {
		t.Fatal("replay --record with empty output returned nil error")
	}
	combined := err.Error() + " " + stderr
	if !strings.Contains(combined, "no output") && !strings.Contains(combined, "empty") {
		t.Fatalf("error %q does not indicate empty output", combined)
	}
}

// TestReplayRecordAndOutputCoexist verifies that --record + --output can both be
// specified: the output is written to the file AND the replay run is recorded.
func TestReplayRecordAndOutputCoexist(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	wantRunID := sourceRunID + "-replay-20260522T103000Z"
	wantOutput := []byte("coexist-output")
	stub := &replay.StubRunner{CannedOutput: wantOutput}
	outFile := filepath.Join(t.TempDir(), "replay-out.txt")

	_, stderr, err := executeReplayWithClock(stub, fixedTime, sourceRunID, "gen", "--record", "--output", outFile)
	if err != nil {
		t.Fatalf("replay --record --output returned error: %v\nstderr: %s", err, stderr)
	}

	// File must be written.
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !bytes.Equal(got, wantOutput) {
		t.Fatalf("file contents = %q, want %q", got, wantOutput)
	}

	// Replay run ref must exist.
	_, err = refstore.New(repo).Resolve(context.Background(), "refs/etude/runs/"+wantRunID)
	if err != nil {
		t.Fatalf("replay run ref %q not found after --record --output: %v", wantRunID, err)
	}
}

// TestReplayRecordProducerFlagOverride verifies that --skill-version overrides
// the skill version in the recorded stage's Producer and Stage.Skill.
func TestReplayRecordProducerFlagOverride(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	wantRunID := sourceRunID + "-replay-20260522T103000Z"
	stub := &replay.StubRunner{CannedOutput: []byte("producer-override-output")}

	_, stderr, err := executeReplayWithClock(stub, fixedTime, sourceRunID, "gen", "--record", "--skill-version", "vNEW")
	if err != nil {
		t.Fatalf("replay --record --skill-version returned error: %v\nstderr: %s", err, stderr)
	}

	m := readRunManifest(t, repo, wantRunID)
	if len(m.Stages) != 1 {
		t.Fatalf("replay run has %d stages, want 1", len(m.Stages))
	}
	s := m.Stages[0]
	if s.Producer.Skill.Version != "vNEW" {
		t.Fatalf("Producer.Skill.Version = %q, want %q", s.Producer.Skill.Version, "vNEW")
	}
	if s.Skill.Version != "vNEW" {
		t.Fatalf("Stage.Skill.Version = %q, want %q", s.Skill.Version, "vNEW")
	}
	// Skill id and repo should be inherited from source.
	if s.Skill.ID == "" {
		t.Fatal("Skill.ID is empty after --skill-version override")
	}
	if s.Skill.Repo == "" {
		t.Fatal("Skill.Repo is empty after --skill-version override")
	}
}

// TestReplayRecordCollisionHandling verifies that two --record calls with the
// same injected clock produce ids with a -2 suffix for the second.
func TestReplayRecordCollisionHandling(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	baseRunID := sourceRunID + "-replay-20260522T103000Z"
	stub1 := &replay.StubRunner{CannedOutput: []byte("first-output")}
	stub2 := &replay.StubRunner{CannedOutput: []byte("second-output")}

	// First record — should get the base id.
	_, stderr, err := executeReplayWithClock(stub1, fixedTime, sourceRunID, "gen", "--record")
	if err != nil {
		t.Fatalf("first replay --record returned error: %v\nstderr: %s", err, stderr)
	}

	// Second record with same clock — should get base-2.
	stdout2, stderr, err := executeReplayWithClock(stub2, fixedTime, sourceRunID, "gen", "--record")
	if err != nil {
		t.Fatalf("second replay --record returned error: %v\nstderr: %s", err, stderr)
	}
	wantSecondID := baseRunID + "-2"
	if !strings.Contains(stdout2, wantSecondID) {
		t.Fatalf("stdout %q does not contain second replay run id %q", stdout2, wantSecondID)
	}

	// Both refs must exist.
	store := refstore.New(repo)
	for _, id := range []string{baseRunID, wantSecondID} {
		if _, err := store.Resolve(context.Background(), "refs/etude/runs/"+id); err != nil {
			t.Fatalf("run ref %q not found: %v", id, err)
		}
	}
}

// TestReplayEmitOnlyWritesNoNewRun verifies that without --record, no new run
// ref is written (the emit-only path is unchanged).
func TestReplayEmitOnlyWritesNoNewRun(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	stub := &replay.StubRunner{CannedOutput: []byte("emit-only-output")}
	stdout, stderr, err := executeReplay(stub, runID, "gen")
	if err != nil {
		t.Fatalf("replay returned error: %v\nstderr: %s", err, stderr)
	}
	// Output must reach stdout.
	if !strings.Contains(stdout, "emit-only-output") {
		t.Fatalf("stdout %q does not contain expected output", stdout)
	}

	// Only the source run ref should exist — no new run with -replay- suffix pattern.
	store := refstore.New(repo)
	refs, err := store.List(context.Background(), "refs/etude/runs")
	if err != nil {
		t.Fatalf("List refs: %v", err)
	}
	for _, ref := range refs {
		// The replay timestamp suffix pattern is: -replay-YYYYMMDDTHHMMSSZ (20 chars after -replay-)
		if strings.Contains(ref, "-replay-") {
			t.Fatalf("unexpected replay ref %q found after emit-only run", ref)
		}
	}
}

// TestReplayRecordInputBytesPreserved verifies that the source input's bytes
// are byte-identical in the recorded replay run.
func TestReplayRecordInputBytesPreserved(t *testing.T) {
	repo, sourceRunID := captureStageForReplay(t)
	chdir(t, repo)

	// Read the source input's artifact path and bytes.
	sourceManifest := readRunManifest(t, repo, sourceRunID)
	if len(sourceManifest.Stages[0].Inputs) == 0 {
		t.Skip("source stage has no inputs — skip input-bytes test")
	}
	sourceInputRef := sourceManifest.Stages[0].Inputs[0]
	store := refstore.New(repo)
	sourceInputBytes, err := store.ReadFile(context.Background(), "refs/etude/runs/"+sourceRunID, sourceInputRef.Path)
	if err != nil {
		t.Fatalf("read source input: %v", err)
	}

	fixedTime := time.Date(2026, 5, 22, 10, 30, 0, 0, time.UTC)
	wantRunID := sourceRunID + "-replay-20260522T103000Z"
	stub := &replay.StubRunner{CannedOutput: []byte("input-preserved-output")}
	_, stderr, err := executeReplayWithClock(stub, fixedTime, sourceRunID, "gen", "--record")
	if err != nil {
		t.Fatalf("replay --record returned error: %v\nstderr: %s", err, stderr)
	}

	// The input must also be present (at the same path) in the replay run.
	replayInputBytes, err := store.ReadFile(context.Background(), "refs/etude/runs/"+wantRunID, sourceInputRef.Path)
	if err != nil {
		t.Fatalf("read replay input: %v", err)
	}
	if !bytes.Equal(replayInputBytes, sourceInputBytes) {
		t.Fatalf("replay input bytes differ from source:\ngot: %q\nwant: %q", replayInputBytes, sourceInputBytes)
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

// TestReplayOutputSymlinkRejected verifies that --output pointing at an
// existing symlink is rejected and the symlink target is NOT clobbered.
func TestReplayOutputSymlinkRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks not supported on windows")
	}
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	// Create a file we want to protect, and a symlink that points to it.
	protectedFile := filepath.Join(t.TempDir(), "protected.txt")
	if err := os.WriteFile(protectedFile, []byte("do not clobber\n"), 0o644); err != nil {
		t.Fatalf("write protected file: %v", err)
	}
	symlinkOut := filepath.Join(t.TempDir(), "out-link.txt")
	if err := os.Symlink(protectedFile, symlinkOut); err != nil {
		t.Skipf("symlink creation unsupported: %v", err)
	}

	stub := &replay.StubRunner{CannedOutput: []byte("should not reach disk")}
	_, _, err := executeReplay(stub, runID, "gen", "--output", symlinkOut)
	if err == nil {
		t.Fatal("replay accepted --output pointing at a symlink; want rejection")
	}
	if !strings.Contains(err.Error(), "not a regular file") && !strings.Contains(err.Error(), symlinkOut) {
		t.Fatalf("error %q does not indicate symlink rejection", err.Error())
	}

	// The protected file must be untouched.
	got, err2 := os.ReadFile(protectedFile)
	if err2 != nil {
		t.Fatalf("read protected file: %v", err2)
	}
	if string(got) != "do not clobber\n" {
		t.Fatalf("protected file was clobbered; content = %q", got)
	}
}

// TestReplayOutputNewPathCreated verifies that --output to a non-existent path
// creates the file normally (unchanged happy path).
func TestReplayOutputNewPathCreated(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	want := []byte("new file output")
	stub := &replay.StubRunner{CannedOutput: want}
	outFile := filepath.Join(t.TempDir(), "brand-new-output.txt")

	_, stderr, err := executeReplay(stub, runID, "gen", "--output", outFile)
	if err != nil {
		t.Fatalf("replay --output to new path failed: %v\nstderr: %s", err, stderr)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output file content = %q, want %q", got, want)
	}
}

// TestReplayOutputExistingRegularFileOverwritten verifies that --output to an
// existing regular file truncates and rewrites it (unchanged happy path).
func TestReplayOutputExistingRegularFileOverwritten(t *testing.T) {
	repo, runID := captureStageForReplay(t)
	chdir(t, repo)

	want := []byte("overwritten content")
	stub := &replay.StubRunner{CannedOutput: want}
	outFile := filepath.Join(t.TempDir(), "existing-output.txt")
	// Pre-populate the file with different content.
	if err := os.WriteFile(outFile, []byte("old content that should be replaced"), 0o644); err != nil {
		t.Fatalf("pre-populate output file: %v", err)
	}

	_, stderr, err := executeReplay(stub, runID, "gen", "--output", outFile)
	if err != nil {
		t.Fatalf("replay --output to existing regular file failed: %v\nstderr: %s", err, stderr)
	}
	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output file content = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// AC2: forward replay (etude replay <run> — no stage arg)
// ---------------------------------------------------------------------------

// createLiveRun creates a 3-stage run in repo using liverun.Engine with a StubRunner.
// Used as setup for forward replay tests.
func createLiveRun(t *testing.T, repo, runID string) {
	t.Helper()
	headSHA := strings.TrimSpace(gitCapture(t, repo, "rev-parse", "HEAD"))
	store := refstore.New(repo)
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
			{Name: "stage-c", Skill: "sk", Produces: "review", Inputs: []string{"diff"}},
		},
	}
	stub := &replay.StubRunner{CannedOutput: []byte("stub-output"), CannedMediaType: "application/octet-stream"}
	e := liverun.Engine{
		Store:         store,
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) { return stub, nil },
		Root:          repo,
		Now:           func() time.Time { return time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC) },
	}
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, liverun.RunOptions{
		TaskBytes: []byte("task data"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    headSHA,
	}); err != nil {
		t.Fatalf("create live run: %v", err)
	}
}

// AC2: etude replay <id> (no stage) forward re-executes all stages.
func TestReplayForwardAllStages(t *testing.T) {
	repo := initCaptureRepo(t)
	runID := "mywf-20260101T000000Z-forward1"
	createLiveRun(t, repo, runID)
	chdir(t, repo)

	// Forward replay with a stub that returns "replayed" for every stage.
	stub := &replay.StubRunner{CannedOutput: []byte("replayed"), CannedMediaType: "text/plain; charset=utf-8"}
	stdout, stderr, err := executeReplayForward(stub, runID)
	if err != nil {
		t.Fatalf("forward replay returned error: %v\nstderr: %s", err, stderr)
	}

	// All 3 stage outputs ("replayed") must appear concatenated in stdout.
	want := "replayedreplayedreplayed"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestReplayForwardSingleStageOnlyFlagsRejected verifies that --record and --output
// are rejected in 1-arg (forward replay) mode.
func TestReplayForwardSingleStageOnlyFlagsRejected(t *testing.T) {
	repo := initCaptureRepo(t)
	runID := "mywf-20260101T000000Z-forward2"
	createLiveRun(t, repo, runID)
	chdir(t, repo)

	for _, flag := range []string{"--record", "--output", "--skill-id"} {
		args := []string{runID, flag}
		if flag == "--output" || flag == "--skill-id" {
			args = append(args, "dummy")
		}
		stub := &replay.StubRunner{}
		_, stderr, err := executeReplayForward(stub, args...)
		if err == nil {
			t.Errorf("expected error for %s in forward replay mode", flag)
			continue
		}
		if !strings.Contains(stderr, "only valid for single-stage replay") {
			t.Errorf("%s: stderr = %q, want 'only valid for single-stage replay'", flag, stderr)
		}
	}
}

// TestReplayForwardMissingRunErrors verifies that a missing run id returns an error.
func TestReplayForwardMissingRunErrors(t *testing.T) {
	repo := initCaptureRepo(t)
	chdir(t, repo)

	stub := &replay.StubRunner{}
	_, stderr, err := executeReplayForward(stub, "mywf-20260101T000000Z-notexist")
	if err == nil {
		t.Fatal("expected error for missing run")
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr = %q, want 'not found'", stderr)
	}
}
