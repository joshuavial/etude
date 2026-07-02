package liverun

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "config", "user.name", "Test User")
	gitRun(t, dir, "config", "user.email", "test@example.invalid")
	writeTestFile(t, dir, "README.md", "test\n")
	gitRun(t, dir, "add", "README.md")
	gitRun(t, dir, "commit", "-m", "initial")
	return dir
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	return gitRun(t, dir, "rev-parse", "HEAD")
}

func readLiveManifest(t *testing.T, repo, runID string) runmanifest.Manifest {
	t.Helper()
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+runID, "manifest.json")
	if err != nil {
		t.Fatalf("ReadFile manifest: %v", err)
	}
	m, err := runmanifest.ParseJSON(content)
	if err != nil {
		t.Fatalf("ParseJSON manifest: %v", err)
	}
	return m
}

// stubResolveRunner returns a ResolveRunner factory that always returns stub.
func stubResolveRunner(stub replay.Runner) func(workflow.Stage) (replay.Runner, error) {
	return func(workflow.Stage) (replay.Runner, error) { return stub, nil }
}

// threeStageWorkflow returns a 3-stage workflow where each stage chains the previous.
func threeStageWorkflow() workflow.Workflow {
	return workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"task", "plan"}},
			{Name: "stage-c", Skill: "sk", Produces: "review", Inputs: []string{"diff"}},
		},
	}
}

// fixedClock returns a Now function that increments by 1 second each call.
func fixedClock() func() time.Time {
	t := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// AC1: 3-stage deterministic workflow run writes a growing manifest chain.
func TestEngineRunThreeStages(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	stub := &replay.StubRunner{CannedOutput: []byte("output"), CannedMediaType: "text/plain; charset=utf-8"}
	wf := threeStageWorkflow()

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("my task"),
		TaskFile:  "task.txt",
		RunID:     "mywf-20260101T000000Z-aabbccdd",
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify output contains captured lines and final ref.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines (3 captured + 1 ref), got %d:\n%s", len(lines), out.String())
	}
	for i := 0; i < 3; i++ {
		if !strings.HasPrefix(lines[i], "captured ") {
			t.Errorf("line %d = %q, want 'captured <oid>'", i, lines[i])
		}
	}
	if !strings.Contains(lines[3], "refs/etude/runs/mywf-20260101T000000Z-aabbccdd") {
		t.Errorf("line 3 = %q, want ref line", lines[3])
	}

	// Verify manifest has 3 stages with correct roles.
	m := readLiveManifest(t, repo, "mywf-20260101T000000Z-aabbccdd")
	if len(m.Stages) != 3 {
		t.Fatalf("stages = %d, want 3", len(m.Stages))
	}
	wantRoles := []string{"plan", "diff", "review"}
	for i, s := range m.Stages {
		if s.Output.Role != wantRoles[i] {
			t.Errorf("stage[%d].output.role = %q, want %q", i, s.Output.Role, wantRoles[i])
		}
		if s.ProducedBy != "original" {
			t.Errorf("stage[%d].produced_by = %q, want original", i, s.ProducedBy)
		}
		if s.GitSHA != sha {
			t.Errorf("stage[%d].git_sha = %q, want %q", i, s.GitSHA, sha)
		}
	}
}

// AC4: Stage B's input ArtifactRef equals Stage A's output ArtifactRef.
func TestEngineArtifactRefChaining(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	stub := &replay.StubRunner{CannedOutput: []byte("chained-output"), CannedMediaType: "application/octet-stream"}
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
			{Name: "stage-b", Skill: "sk", Produces: "diff", Inputs: []string{"plan"}},
		},
	}

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	if err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     "mywf-20260101T000000Z-aabbccdd",
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, "mywf-20260101T000000Z-aabbccdd")
	if len(m.Stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(m.Stages))
	}

	// AC4: stage-b's first input ref must match stage-a's output ref.
	aOutput := m.Stages[0].Output
	bInput := m.Stages[1].Inputs[0]
	if bInput.Artifact != aOutput.Artifact {
		t.Errorf("stage-b input artifact %q != stage-a output artifact %q", bInput.Artifact, aOutput.Artifact)
	}
	if bInput.Path != aOutput.Path {
		t.Errorf("stage-b input path %q != stage-a output path %q", bInput.Path, aOutput.Path)
	}
	if bInput.Role != "plan" {
		t.Errorf("stage-b input role = %q, want plan", bInput.Role)
	}
}

// AC3: Stop-and-capture on failure + resume completes the run.
func TestEngineResumeAfterFailure(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	// Stage-a succeeds; stage-b fails.
	runID := "mywf-20260101T000000Z-aabbccdd"
	wf := threeStageWorkflow()

	callCount := 0
	failRunner := func(stage workflow.Stage) (replay.Runner, error) {
		callCount++
		if stage.Name == "stage-b" {
			return &replay.StubRunner{Err: errors.New("stage-b error")}, nil
		}
		return &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}, nil
	}

	var out bytes.Buffer
	e := Engine{
		Store:         store,
		ResolveRunner: failRunner,
		Root:          repo,
		Now:           fixedClock(),
	}
	err := e.Run(context.Background(), &out, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})

	// Verify failure is reported as StageError.
	var stageErr *StageError
	if !errors.As(err, &stageErr) {
		t.Fatalf("expected StageError, got: %v", err)
	}
	if stageErr.StageName != "stage-b" {
		t.Errorf("StageError.StageName = %q, want stage-b", stageErr.StageName)
	}
	if stageErr.RunID != runID {
		t.Errorf("StageError.RunID = %q, want %q", stageErr.RunID, runID)
	}

	// Partial manifest must have stage-a only.
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 {
		t.Fatalf("partial manifest stages = %d, want 1", len(m.Stages))
	}
	if m.Stages[0].Name != "stage-a" {
		t.Errorf("partial manifest stage[0].name = %q, want stage-a", m.Stages[0].Name)
	}

	// Now resume: stage-b and stage-c succeed.
	successRunner := func(stage workflow.Stage) (replay.Runner, error) {
		return &replay.StubRunner{CannedOutput: []byte("resumed"), CannedMediaType: "application/octet-stream"}, nil
	}
	e.ResolveRunner = successRunner
	e.Now = fixedClock()

	var out2 bytes.Buffer
	err = e.Run(context.Background(), &out2, wf, RunOptions{ResumeID: runID})
	if err != nil {
		t.Fatalf("resume Run: %v", err)
	}

	// Final manifest must have all 3 stages.
	m = readLiveManifest(t, repo, runID)
	if len(m.Stages) != 3 {
		t.Fatalf("final manifest stages = %d, want 3", len(m.Stages))
	}

	// Explicit reseed byte-presence: the resumed stage-b CAS append could only
	// succeed if the task input blob AND stage-a's output blob were reseeded with
	// correct bytes from the partial run commit (WriteManifestTree rejects any
	// referenced-but-missing artifact). Assert both blobs are byte-present in the
	// final run commit, not merely referenced.
	rs := refstore.New(repo)
	taskPath := ""
	for _, in := range m.Stages[0].Inputs {
		if in.Role == "task" {
			taskPath = in.Path
		}
	}
	if taskPath == "" {
		t.Fatal("stage-a has no task input role in final manifest")
	}
	for _, p := range []string{taskPath, m.Stages[0].Output.Path} {
		b, err := rs.ReadFile(context.Background(), "refs/etude/runs/"+runID, p)
		if err != nil {
			t.Fatalf("reseeded blob %q not present in resumed run commit: %v", p, err)
		}
		if len(b) == 0 {
			t.Errorf("reseeded blob %q is empty", p)
		}
	}
}

// blockingRunner signals `started` when its Run begins and blocks until
// `release` is closed — lets a test inspect the run ref mid-execution.
type blockingRunner struct {
	output  []byte
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	if r.started != nil {
		close(r.started)
	}
	if r.release != nil {
		<-r.release
	}
	return replay.RunResult{Output: r.output, MediaType: "application/octet-stream", Producer: req.Producer}, nil
}

// AC1 (incl. mid-run): while a later stage is still executing, the run ref is a
// valid snapshot inspectable by `run show` and lists already-captured stages.
func TestEngineMidRunInspectable(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)
	wf := threeStageWorkflow() // stage-a(plan) -> stage-b(diff) -> stage-c(review)
	runID := "mywf-20260101T000000Z-midrun01"

	started := make(chan struct{})
	release := make(chan struct{})
	resolve := func(stage workflow.Stage) (replay.Runner, error) {
		if stage.Name == "stage-b" {
			return &blockingRunner{output: []byte("b-out"), started: started, release: release}, nil
		}
		return &replay.StubRunner{CannedOutput: []byte("out"), CannedMediaType: "application/octet-stream"}, nil
	}
	e := Engine{Store: store, ResolveRunner: resolve, Root: repo, Now: fixedClock()}

	done := make(chan error, 1)
	go func() {
		done <- e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{
			TaskBytes: []byte("t"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
	}()

	// stage-b is now executing; stage-a has been CAS-captured.
	<-started
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 || m.Stages[0].Name != "stage-a" {
		close(release)
		<-done
		t.Fatalf("mid-run manifest = %d stages, want exactly [stage-a]", len(m.Stages))
	}

	close(release) // let stage-b + stage-c finish
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	m = readLiveManifest(t, repo, runID)
	if len(m.Stages) != 3 {
		t.Fatalf("final stages = %d, want 3", len(m.Stages))
	}
}

// TestEngineInvalidExplicitRunID: an explicit --run-id override must be validated
// via runmanifest.IsValidRunID before any git ref path is touched (gate round-1
// BLOCK: prevents path traversal / .lock / bad-charset ids reaching the ref).
func TestEngineInvalidExplicitRunID(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)
	stub := &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}
	for _, bad := range []string{"../evil", "bad/id", "x.lock", "has space", ".hidden"} {
		err := e.Run(context.Background(), &bytes.Buffer{}, threeStageWorkflow(), RunOptions{
			TaskBytes: []byte("t"),
			TaskFile:  "task.txt",
			RunID:     bad,
			GitSHA:    sha,
		})
		if err == nil || !strings.Contains(err.Error(), "invalid run id") {
			t.Errorf("run id %q: expected 'invalid run id' error, got: %v", bad, err)
		}
		// No ref must have been created for the rejected id.
		if _, rerr := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/"+bad, "manifest.json"); rerr == nil {
			t.Errorf("run id %q: a ref was created despite validation failure", bad)
		}
	}
}

// TestEngineReservedNamesPreventedAtCLILevel verifies DeriveFrontier handles
// already-complete runs (no "etude run" execution needed here; the guard is CLI-level).
func TestEngineAlreadyCompleteResumeErrors(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)
	store := refstore.New(repo)

	runID := "mywf-20260101T000000Z-aabbccdd"
	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "stage-a", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	stub := &replay.StubRunner{CannedOutput: []byte("ok"), CannedMediaType: "application/octet-stream"}
	e := Engine{
		Store:         store,
		ResolveRunner: stubResolveRunner(stub),
		Root:          repo,
		Now:           fixedClock(),
	}

	// Complete the run.
	if err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Resume of complete run must error.
	err := e.Run(context.Background(), &bytes.Buffer{}, wf, RunOptions{ResumeID: runID})
	if err == nil || !strings.Contains(err.Error(), "already complete") {
		t.Errorf("expected 'already complete' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Producer Session Evidence tests (etude-7ri.2)
// ---------------------------------------------------------------------------

// sessionStubRunner is a runner that writes a transcript file and returns a
// RunResult with a Session field populated.
type sessionStubRunner struct {
	output          []byte
	transcriptName  string
	transcriptBytes []byte
	sessionID       string
	harnessName     string
}

func (r *sessionStubRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	path := filepath.Join(req.ScratchDir, r.transcriptName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return replay.RunResult{}, err
	}
	if err := os.WriteFile(path, r.transcriptBytes, 0o644); err != nil {
		return replay.RunResult{}, err
	}
	return replay.RunResult{
		Output:    r.output,
		MediaType: "text/plain; charset=utf-8",
		Producer: runmanifest.Producer{
			Harness: runmanifest.Harness{Name: r.harnessName},
			Skill:   req.Producer.Skill,
		},
		Session: &replay.SessionInfo{
			SessionID:      r.sessionID,
			TranscriptPath: r.transcriptName,
		},
	}, nil
}

func TestProducerSession_AgenticStagePopulatesSession(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	stub := &sessionStubRunner{
		output:          []byte("plan output"),
		transcriptName:  "transcript.txt",
		transcriptBytes: []byte("this is the transcript"),
		sessionID:       "session-abc",
		harnessName:     "claude-code",
	}

	runID := "mywf-20260101T000000Z-sesstest1"
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) { return stub, nil },
		Root:          repo,
		Now:           fixedClock(),
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) != 1 {
		t.Fatalf("stages = %d, want 1", len(m.Stages))
	}
	sess := m.Stages[0].Producer.Session
	if sess == nil {
		t.Fatal("expected non-nil producer.session for agentic stage")
	}
	if sess.SessionID != "session-abc" {
		t.Errorf("session_id = %q, want session-abc", sess.SessionID)
	}
	if sess.RetrievalStatus != runmanifest.SessionEvidenceRetrievalImported {
		t.Errorf("retrieval_status = %q, want imported", sess.RetrievalStatus)
	}
	if sess.RedactionStatus != runmanifest.SessionEvidenceRedactionPassed {
		t.Errorf("redaction_status = %q, want passed", sess.RedactionStatus)
	}
	if sess.TranscriptArtifact == nil {
		t.Error("expected non-nil transcript_artifact")
	}
}

func TestProducerSession_DeterministicStageNilSession(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	wf := workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{Name: "plan", Skill: "sk", Produces: "plan", Inputs: []string{"task"}},
		},
	}

	// Stub with harnessName="shell" — should skip session evidence.
	stub := &sessionStubRunner{
		output:          []byte("plan output"),
		transcriptName:  "transcript.txt",
		transcriptBytes: []byte("transcript"),
		sessionID:       "should-be-ignored",
		harnessName:     "shell",
	}

	runID := "mywf-20260101T000000Z-sesstest2"
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: func(workflow.Stage) (replay.Runner, error) { return stub, nil },
		Root:          repo,
		Now:           fixedClock(),
	}

	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	m := readLiveManifest(t, repo, runID)
	if m.Stages[0].Producer.Session != nil {
		t.Error("expected nil producer.session for shell/deterministic stage")
	}
}
