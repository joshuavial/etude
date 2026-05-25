package bench

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// ---------------------------------------------------------------------------
// Test fixture helpers
// ---------------------------------------------------------------------------

// initRepoWithCommit creates a temporary git repo with identity configured,
// makes an initial commit (so HEAD exists and worktree.Checkout can work),
// and returns the repo path and the HEAD commit OID.
func initRepoWithCommit(t *testing.T) (repoDir, headSHA string) {
	t.Helper()
	dir := t.TempDir()
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
	run("init")
	run("config", "user.name", "Test User")
	run("config", "user.email", "test@example.invalid")
	// Create an initial commit so HEAD resolves.
	run("commit", "--allow-empty", "-m", "initial")
	sha := run("rev-parse", "HEAD")
	return dir, sha
}

// seedSourceRun writes a source run manifest into the store, using headSHA
// as the stage GitSHA (so worktree.Checkout succeeds in the tests).
// Returns the CohortRun wrapping the seeded run and the source commit OID.
func seedSourceRun(t *testing.T, store refstore.Store, headSHA string) (cr CohortRun, sourceCommit string) {
	t.Helper()

	inputRef, inputBytes := contentArtifact("prompt", []byte("bench input"))
	outputContent := []byte("original output bytes")
	stage := makeStageWithSHA("plan", headSHA, []runmanifest.ArtifactRef{inputRef}, outputContent)
	// Override Producer with real fields needed to pass manifest Validate.
	stage.Producer = runmanifest.Producer{
		Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
	}
	// Stage.Skill must also be set (validateStage requires it).
	stage.Skill = stage.Producer.Skill

	files := map[string][]byte{
		inputRef.Path:     inputBytes,
		stage.Output.Path: outputContent,
	}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := makeManifest("bench-source-run", ts, []runmanifest.Stage{stage})
	sourceCommit = seedRunInto(t, store, manifest, files)

	cr = CohortRun{
		RunID:   "bench-source-run",
		Commit:  sourceCommit,
		Stage:   stage,
		Created: ts,
	}
	return cr, sourceCommit
}

// newBenchPipeline builds a Pipeline with injected stubs and a fixed clock.
func newBenchPipeline(store refstore.Store, runner replay.Runner, judge eval.Judge, fixedTime time.Time) Pipeline {
	rec := replay.RunRecorder{Store: store, Now: func() time.Time { return fixedTime }}
	return Pipeline{
		Store:    store,
		Runner:   runner,
		Judge:    judge,
		Recorder: rec,
		Now:      func() time.Time { return fixedTime },
	}
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// TestBenchRunHappyPath verifies the full pipeline:
// - BenchOutcome is populated (ReplayRunID, EvalID, Winner),
// - EvalResult is persisted and re-readable via eval.ParseJSON,
// - both Targets are commit-pinned with valid SHA-256 artifacts,
// - EvalResult.Validate passes,
// - orientation: Targets[0]=original(cr.RunID), Targets[1]=replay.
func TestBenchRunHappyPath(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, sourceCommit := seedSourceRun(t, store, headSHA)

	replayOutput := []byte("replay output bytes")
	stub := &replay.StubRunner{
		CannedOutput: replayOutput,
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "bench-skill", Repo: "bench-repo", Version: "v1"},
		},
	}

	stubJudge := &eval.StubJudge{
		Canned: eval.JudgeResponse{Winner: eval.WinnerTie},
	}

	fixedTime := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, stubJudge, fixedTime)

	outcome, err := p.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("BenchRun: %v", err)
	}

	// BenchOutcome must be populated.
	if outcome.SourceRunID != cr.RunID {
		t.Errorf("SourceRunID = %q, want %q", outcome.SourceRunID, cr.RunID)
	}
	if outcome.Stage != "plan" {
		t.Errorf("Stage = %q, want plan", outcome.Stage)
	}
	if outcome.ReplayRunID == "" {
		t.Error("ReplayRunID is empty")
	}
	if outcome.ReplayCommit == "" {
		t.Error("ReplayCommit is empty")
	}
	if outcome.EvalID == "" {
		t.Error("EvalID is empty")
	}
	if outcome.Winner != eval.WinnerTie {
		t.Errorf("Winner = %q, want tie", outcome.Winner)
	}

	// EvalResult.Validate must pass.
	if err := outcome.Result.Validate(); err != nil {
		t.Errorf("Result.Validate: %v", err)
	}

	// EvalResult must be persisted and re-readable.
	evalRef := "refs/etude/evals/" + outcome.EvalID
	commit, err := store.Resolve(context.Background(), evalRef)
	if err != nil {
		t.Fatalf("Resolve eval ref: %v", err)
	}
	raw, err := store.ReadCommitFile(context.Background(), commit, "eval_result.json")
	if err != nil {
		t.Fatalf("ReadCommitFile eval_result.json: %v", err)
	}
	reparsed, err := eval.ParseJSON(raw)
	if err != nil {
		t.Fatalf("eval.ParseJSON: %v", err)
	}
	if reparsed.EvalID != outcome.EvalID {
		t.Errorf("reparsed EvalID = %q, want %q", reparsed.EvalID, outcome.EvalID)
	}

	// Targets: [0]=original (source run), [1]=replay (replay run).
	if len(reparsed.Targets) != 2 {
		t.Fatalf("len(Targets) = %d, want 2", len(reparsed.Targets))
	}
	tA := reparsed.Targets[0]
	tB := reparsed.Targets[1]

	if tA.RunID != cr.RunID {
		t.Errorf("Targets[0].RunID = %q, want %q", tA.RunID, cr.RunID)
	}
	if tA.Commit != sourceCommit {
		t.Errorf("Targets[0].Commit = %q, want %q", tA.Commit, sourceCommit)
	}
	if len(tA.Artifact) != 64 {
		t.Errorf("Targets[0].Artifact length = %d, want 64", len(tA.Artifact))
	}

	if tB.RunID != outcome.ReplayRunID {
		t.Errorf("Targets[1].RunID = %q, want %q", tB.RunID, outcome.ReplayRunID)
	}
	if tB.Commit != outcome.ReplayCommit {
		t.Errorf("Targets[1].Commit = %q, want %q", tB.Commit, outcome.ReplayCommit)
	}
	if len(tB.Artifact) != 64 {
		t.Errorf("Targets[1].Artifact length = %d, want 64", len(tB.Artifact))
	}
}

// TestBenchRunTargetsPinned verifies that both eval targets carry valid
// 40/64-char lowercase hex commit OIDs and 64-char SHA-256 artifact hashes
// (the exact shape required by eval.EvalResult.Validate).
func TestBenchRunTargetsPinned(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("some output"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "s", Repo: "r", Version: "v1"},
		},
	}
	judge := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}

	fixedTime := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)

	outcome, err := p.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("BenchRun: %v", err)
	}

	if err := outcome.Result.Validate(); err != nil {
		t.Errorf("Result.Validate: %v", err)
	}

	for i, tgt := range outcome.Result.Targets {
		if !isHex4064(tgt.Commit) {
			t.Errorf("Targets[%d].Commit %q is not a 40/64-char lowercase hex OID", i, tgt.Commit)
		}
		if len(tgt.Artifact) != 64 || !isHex4064(tgt.Artifact) {
			t.Errorf("Targets[%d].Artifact %q is not a 64-char lowercase hex SHA-256", i, tgt.Artifact)
		}
	}
}

// TestBenchRunProducerOverrideApplied verifies that ProducerOverrides are
// reflected in the recorded replay stage and the EvalResult.Producer.
func TestBenchRunProducerOverrideApplied(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	// The stub ECHOES req.Producer (no ProducerOverride), so the recorded
	// Skill.Version can only be "vNEW" if BenchRun's Overrides merge produced it
	// (the source run's version is "v1"). This gives the test real teeth: drop
	// the Overrides merge and the recorded version would be "v1", failing below.
	stub := &replay.StubRunner{
		CannedOutput: []byte("override output"),
	}
	judge := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}

	fixedTime := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)
	p.Overrides = ProducerOverrides{
		SkillVersionChanged: true,
		SkillVersion:        "vNEW",
	}

	outcome, err := p.BenchRun(context.Background(), repoDir, cr)
	if err != nil {
		t.Fatalf("BenchRun: %v", err)
	}

	// Inspect the recorded replay manifest to verify the override was applied.
	replayRef := "refs/etude/runs/" + outcome.ReplayRunID
	replayCommit, err := store.Resolve(context.Background(), replayRef)
	if err != nil {
		t.Fatalf("resolve replay ref: %v", err)
	}
	raw, err := store.ReadCommitFile(context.Background(), replayCommit, "manifest.json")
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	m, err := runmanifest.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if len(m.Stages) != 1 {
		t.Fatalf("replay stages = %d, want 1", len(m.Stages))
	}
	s := m.Stages[0]
	if s.Producer.Skill.Version != "vNEW" {
		t.Errorf("recorded Producer.Skill.Version = %q, want vNEW (from Overrides merge, not source v1)", s.Producer.Skill.Version)
	}
	// Non-overridden fields fall through to the source producer, proving the
	// merge is selective (only SkillVersion changed).
	if s.Producer.Skill.ID != "bench-skill" {
		t.Errorf("recorded Producer.Skill.ID = %q, want bench-skill (unchanged from source)", s.Producer.Skill.ID)
	}
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

// TestBenchRunRunnerError verifies that a runner error propagates and no eval
// ref is written.
func TestBenchRunRunnerError(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	runnerErr := errors.New("runner exploded")
	stub := &replay.StubRunner{Err: runnerErr}
	judge := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}

	fixedTime := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)

	_, err := p.BenchRun(context.Background(), repoDir, cr)
	if err == nil {
		t.Fatal("BenchRun returned nil error, want runner error")
	}
	if !errors.Is(err, runnerErr) {
		t.Errorf("error %v does not wrap runner error", err)
	}

	// No eval ref must have been written.
	refs, listErr := store.List(context.Background(), "refs/etude/evals")
	if listErr != nil {
		t.Fatalf("List evals: %v", listErr)
	}
	if len(refs) != 0 {
		t.Errorf("eval refs written despite runner error: %v", refs)
	}
}

// TestBenchRunJudgeError verifies that a judge error propagates.
// The replay run is recorded before the judge call, so the replay ref will
// exist, but no eval ref should be written.
func TestBenchRunJudgeError(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{
		CannedOutput: []byte("runner output"),
		ProducerOverride: runmanifest.Producer{
			Skill: runmanifest.Skill{ID: "s", Repo: "r", Version: "v1"},
		},
	}
	judgeErr := errors.New("judge on fire")
	judge := &eval.StubJudge{Err: judgeErr}

	fixedTime := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)

	_, err := p.BenchRun(context.Background(), repoDir, cr)
	if err == nil {
		t.Fatal("BenchRun returned nil error, want judge error")
	}
	if !errors.Is(err, judgeErr) {
		t.Errorf("error %v does not wrap judge error", err)
	}

	// No eval ref must have been written.
	refs, listErr := store.List(context.Background(), "refs/etude/evals")
	if listErr != nil {
		t.Fatalf("List evals: %v", listErr)
	}
	if len(refs) != 0 {
		t.Errorf("eval refs written despite judge error: %v", refs)
	}
}

// TestBenchRunEmptyRunnerOutput verifies that an empty runner output is an error.
func TestBenchRunEmptyRunnerOutput(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)
	cr, _ := seedSourceRun(t, store, headSHA)

	stub := &replay.StubRunner{CannedOutput: nil} // nil -> empty output
	judge := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}

	fixedTime := time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)

	_, err := p.BenchRun(context.Background(), repoDir, cr)
	if err == nil {
		t.Fatal("BenchRun returned nil error, want empty-output error")
	}
	if !strings.Contains(err.Error(), "no output") {
		t.Errorf("error %q does not indicate empty output", err.Error())
	}
}

// TestBenchRunPointerInputError verifies that a pointer input in the source run
// causes BenchRun to return an error (mirrors replay.go's pointer guard).
// We test this by resolving a stage that has a pointer input. However, since
// cohort.go's SelectCohort already filters pointer inputs (SkipPointerInput),
// we simulate the error at the resolve level by seeding a run with a pointer
// input directly via the refstore.
func TestBenchRunPointerInputError(t *testing.T) {
	repoDir, headSHA := initRepoWithCommit(t)
	store := newStore(repoDir)

	// Build a manifest with a pointer input, bypassing the normal cohort filter.
	// Use a valid pointer artifact (storage="pointer") so the manifest is valid.
	pointerRef, pointerBytes := pointerArtifact(t, "prompt")
	outputContent := []byte("bench pointer output")
	outputRef, _ := contentArtifact("output", outputContent)
	// Override output to use content storage so manifest passes Validate.
	stage := runmanifest.Stage{
		Name:       "plan",
		ProducedBy: "test-agent",
		GitSHA:     headSHA,
		Skill:      runmanifest.Skill{ID: "s", Repo: "r", Version: "v1"},
		Producer:   runmanifest.Producer{Skill: runmanifest.Skill{ID: "s", Repo: "r", Version: "v1"}},
		Timestamp:  time.Now().UTC(),
		Inputs:     []runmanifest.ArtifactRef{pointerRef},
		Output:     outputRef,
	}
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manifest := makeManifest("bench-pointer-run", ts, []runmanifest.Stage{stage})
	files := map[string][]byte{
		pointerRef.Path: pointerBytes,
		outputRef.Path:  outputContent,
	}
	sourceCommit := seedRunInto(t, store, manifest, files)

	cr := CohortRun{
		RunID:   "bench-pointer-run",
		Commit:  sourceCommit,
		Stage:   stage,
		Created: ts,
	}

	stub := &replay.StubRunner{CannedOutput: []byte("x")}
	judge := &eval.StubJudge{Canned: eval.JudgeResponse{Winner: eval.WinnerA}}

	fixedTime := time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)
	p := newBenchPipeline(store, stub, judge, fixedTime)

	_, err := p.BenchRun(context.Background(), repoDir, cr)
	if err == nil {
		t.Fatal("BenchRun returned nil error, want pointer-input error")
	}
	if !errors.Is(err, replay.ErrPointerNotMaterialized) {
		t.Errorf("error %v does not wrap ErrPointerNotMaterialized", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isHex4064 reports whether s is a 40- or 64-char lowercase hex string.
func isHex4064(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
