package liverun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// gateOnceStage is the stage under test: a `verify` stage gated at L2 with an
// abstraction, mirroring the repo's own .etude/workflow.yaml.
func gateOnceStage(checks []workflow.Runner) workflow.Stage {
	return workflow.Stage{
		Name:     "verify",
		Produces: "verify",
		Gate: &workflow.GateConfig{
			Tier:        "L2",
			Abstraction: "test adequacy + real built-binary behavior",
			Checks:      checks,
		},
	}
}

// seedGateRun creates a run ref carrying one captured stage that produces the
// gated role, which is the precondition `etude gate` enforces.
func seedGateRun(t *testing.T, repo, runID, stageName, role string) {
	t.Helper()
	store := refstore.New(repo)
	as := artifactstore.New()
	art, err := as.AddContent(role, "text/markdown; charset=utf-8", []byte("the artifact under review\n"))
	if err != nil {
		t.Fatalf("add artifact: %v", err)
	}
	manifest := runmanifest.Manifest{
		RunID:           runID,
		Workflow:        "default",
		WorkflowVersion: "default-v1",
		Created:         fixedClock()(),
		Refs:            map[string]string{},
		Stages: []runmanifest.Stage{{
			Name:       stageName,
			ProducedBy: "original",
			GitSHA:     headSHA(t, repo),
			Skill:      runmanifest.Skill{ID: "dev-qa", Repo: "manual", Version: "manual"},
			Output:     runmanifest.ArtifactFromManifestArtifact(art),
			Timestamp:  fixedClock()(),
		}},
	}
	if _, err := (runmanifest.Writer{Store: store}).Write(context.Background(), manifest, as.Files(), runmanifest.WriteOptions{}); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// gateOnceEngine wires an Engine whose seats return the given canned envelopes
// in order, and records the input every seat received.
func gateOnceEngine(t *testing.T, repo string, envelopes [][]byte, checkPassed bool, seen *[][]byte) *Engine {
	t.Helper()
	ss := &stubSeats{responses: envelopes}
	return &Engine{
		Store: refstore.New(repo),
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			detail := ""
			if !checkPassed {
				detail = "check failed"
			}
			return &stubCheckRunner{passed: checkPassed, rawOutput: []byte("check output"), detail: detail}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			inner := ss.runner()
			return recordingRunner{inner: inner, seen: seen}, SeatMeta{
				HarnessName:  "stub-harness",
				ProviderName: "stub-provider",
				Model:        "stub-model",
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L2": {[]string{"opus", "codex"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
}

// recordingRunner captures the gate-prompt bytes each seat is handed, so a test
// can assert every seat reviewed byte-identical input.
type recordingRunner struct {
	inner replay.Runner
	seen  *[][]byte
}

func (r recordingRunner) Run(ctx context.Context, req replay.RunRequest) (replay.RunResult, error) {
	if r.seen != nil {
		for _, in := range req.Inputs {
			*r.seen = append(*r.seen, append([]byte(nil), in.Content...))
		}
	}
	return r.inner.Run(ctx, req)
}

func gateScratch(t *testing.T) string {
	t.Helper()
	// Scratch must not live under the worktree; t.TempDir gives a sibling.
	return filepath.Join(t.TempDir(), "scratch")
}

func goEnvelope() []byte    { return []byte(`{"verdict":"go"}`) }
func blockEnvelope() []byte { return []byte(`{"verdict":"block","required":["fix the guard"]}`) }

func TestGateStageMissingRunIsAClearError(t *testing.T) {
	repo := initTestRepo(t)
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope()}, true, nil)

	_, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID:       "nope",
		Stage:       gateOnceStage(nil),
		Artifact:    []byte("x"),
		WorktreeDir: repo,
		ScratchDir:  gateScratch(t),
	})
	if !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "etude capture") {
		t.Errorf("error should point at the fix, got %q", err)
	}
}

func TestGateStageRunWithoutReviewableStageIsAClearError(t *testing.T) {
	repo := initTestRepo(t)
	// The run exists but its only stage produces "plan", not "verify".
	seedGateRun(t, repo, "r1", "plan", "plan")
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope()}, true, nil)

	_, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID:       "r1",
		Stage:       gateOnceStage(nil),
		Artifact:    []byte("x"),
		WorktreeDir: repo,
		ScratchDir:  gateScratch(t),
	})
	if !errors.Is(err, ErrNoReviewableStage) {
		t.Fatalf("expected ErrNoReviewableStage, got %v", err)
	}
}

func TestGateStagePassRecordsOneAttemptAndAdvances(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	var seen [][]byte
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, true, &seen)

	var out bytes.Buffer
	outcome, err := e.GateStage(context.Background(), &out, GateRequest{
		RunID:       "r1",
		Stage:       gateOnceStage(nil),
		Artifact:    []byte("the artifact under review\n"),
		WorktreeDir: repo,
		ScratchDir:  gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if !outcome.Passed() {
		t.Fatalf("expected pass, got %s", outcome.Status)
	}
	if outcome.GateID != "verify.r1" {
		t.Errorf("gate id = %q, want verify.r1", outcome.GateID)
	}

	m := readLiveManifest(t, repo, "r1")
	if len(m.Gates) != 1 {
		t.Fatalf("expected 1 gate attempt, got %d", len(m.Gates))
	}
	g := m.Gates[0]
	if g.Tier != 2 {
		t.Errorf("tier = %d, want 2 (L2)", g.Tier)
	}
	if len(g.ReviewedStages) != 1 || g.ReviewedStages[0].Stage != "verify" || g.ReviewedStages[0].Role != "verify" {
		t.Errorf("reviewed_stages = %+v", g.ReviewedStages)
	}
	if len(g.Seats) != 2 {
		t.Fatalf("expected 2 seat results, got %d", len(g.Seats))
	}

	// Every seat must have reviewed byte-identical input: seats are model
	// identities voting on ONE prompt, not personas with different briefs.
	if len(seen) != 2 {
		t.Fatalf("expected 2 recorded seat inputs, got %d", len(seen))
	}
	if !bytes.Equal(seen[0], seen[1]) {
		t.Errorf("seats received different prompts:\n--- seat 0 ---\n%s\n--- seat 1 ---\n%s", seen[0], seen[1])
	}
	prompt := string(seen[0])
	// The abstraction is the altitude control; it reached no seat before this bead.
	if !strings.Contains(prompt, "test adequacy + real built-binary behavior") {
		t.Errorf("prompt is missing the stage abstraction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "the artifact under review") {
		t.Errorf("prompt is missing the inlined artifact:\n%s", prompt)
	}
	// The envelope is the adapter's job, so the prompt must not ask the model
	// for it — a model that could write the file could fake a pass.
	if strings.Contains(prompt, "ETUDE_OUTPUT_FILE") {
		t.Errorf("prompt must not mention ETUDE_OUTPUT_FILE:\n%s", prompt)
	}
}

func TestGateStageBlockDoesNotPassAndCarriesRequired(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), blockEnvelope()}, true, nil)

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if outcome.Passed() {
		t.Fatal("a blocking seat must not pass the gate")
	}
	if outcome.Status != runmanifest.GateStatusRerun {
		t.Errorf("status = %s, want rerun (below max_rounds)", outcome.Status)
	}
	var found bool
	for _, s := range outcome.Attempt.Seats {
		for _, r := range s.Required {
			if r == "fix the guard" {
				found = true
			}
		}
	}
	if !found {
		t.Error("the blocking seat's required change was not recorded")
	}
}

// TestGateStageSeatOutageEscalates is the load-bearing test: a gate must NEVER
// pass when a seat could not be reached, no matter what the other seat said.
func TestGateStageSeatOutageEscalates(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	// Second seat produces no output at all -> classified `empty`.
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), nil}, true, nil)

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if outcome.Passed() {
		t.Fatal("a gate passed with only one usable seat at a two-seat tier")
	}
	if outcome.Status != runmanifest.GateStatusEscalated {
		t.Errorf("status = %s, want escalated", outcome.Status)
	}
	if !strings.Contains(outcome.Attempt.Decision.EscalationReason, "insufficient usable seats") {
		t.Errorf("escalation reason = %q", outcome.Attempt.Decision.EscalationReason)
	}
	// The outage is recorded, not silently dropped.
	m := readLiveManifest(t, repo, "r1")
	if len(m.Gates) != 1 {
		t.Fatalf("the failed attempt must still be recorded, got %d gates", len(m.Gates))
	}
}

func TestGateStageFailingCheckDoesNotPass(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	checks := []workflow.Runner{{Command: "make test"}}
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, false, nil)

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(checks), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if outcome.Passed() {
		t.Fatal("a failing check must not pass the gate even when every seat says go")
	}
}

// TestGateStageRoundIncrementsAcrossInvocations proves a re-gate after a worker
// fix records round 2, derived from the run rather than from anyone's memory.
func TestGateStageRoundIncrementsAcrossInvocations(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")

	first := gateOnceEngine(t, repo, [][]byte{goEnvelope(), blockEnvelope()}, true, nil)
	o1, err := first.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("first gate: %v", err)
	}
	if o1.Round != 1 {
		t.Fatalf("first round = %d, want 1", o1.Round)
	}

	second := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, true, nil)
	o2, err := second.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("second gate: %v", err)
	}
	if o2.Round != 2 || o2.GateID != "verify.r2" {
		t.Errorf("second attempt = round %d id %q, want round 2 verify.r2", o2.Round, o2.GateID)
	}
	if !o2.Passed() {
		t.Errorf("second attempt should pass, got %s", o2.Status)
	}

	m := readLiveManifest(t, repo, "r1")
	if len(m.Gates) != 2 {
		t.Fatalf("expected both attempts recorded, got %d", len(m.Gates))
	}
	if m.Gates[0].Status != runmanifest.GateStatusRerun || m.Gates[1].Status != runmanifest.GateStatusPass {
		t.Errorf("history = %s then %s, want rerun then pass", m.Gates[0].Status, m.Gates[1].Status)
	}
}

// TestGateStageRecordParitesWithCaptureGate asserts the attempt round-trips
// through the manifest wire schema with the fields capture-gate also carries —
// session evidence and raw output included, since those are the ones most likely
// to diverge between the two write paths.
func TestGateStageRecordParitesWithCaptureGate(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")

	ss := &stubSeats{responses: [][]byte{sessionEnvelopeJSONWithPath("go", "transcript.md")}}
	e := &Engine{
		Store: refstore.New(repo),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			_ = ss
			return transcriptSeatRunner{
				envelope:   sessionEnvelopeJSONWithPath("go", "transcript.md"),
				transcript: []byte("seat transcript body\n"),
				path:       "transcript.md",
			}, SeatMeta{HarnessName: "codex", ProviderName: "openai", Model: "gpt-5.5", RequireSessionEvidence: true}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"codex"}, "L1"}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	if _, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	}); err != nil {
		t.Fatalf("GateStage: %v", err)
	}

	m := readLiveManifest(t, repo, "r1")
	if len(m.Gates) != 1 || len(m.Gates[0].Seats) != 1 {
		t.Fatalf("unexpected shape: %+v", m.Gates)
	}
	seat := m.Gates[0].Seats[0]
	if seat.Session == nil {
		t.Fatal("session evidence was not recorded; the acceptance names it explicitly")
	}
	if seat.Session.RetrievalStatus != runmanifest.SessionEvidenceRetrievalImported {
		t.Errorf("retrieval = %s, want imported", seat.Session.RetrievalStatus)
	}
	if seat.Session.TranscriptArtifact == nil {
		t.Error("transcript artifact was not stored on the run")
	}
	if seat.RawOutput == nil {
		t.Error("seat raw output was not stored on the run")
	}
	if seat.Provider.Name != "openai" || seat.Provider.Model != "gpt-5.5" {
		t.Errorf("provider = %+v, want the registry identity", seat.Provider)
	}
}

// TestGateStageSeatClaimingMissingTranscriptIsNotGo: a seat that asserts session
// evidence it cannot produce must not be counted as a pass.
func TestGateStageSeatClaimingMissingTranscriptIsNotGo(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")

	e := &Engine{
		Store: refstore.New(repo),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return &replay.StubRunner{
				CannedOutput:    sessionEnvelopeJSONWithPath("go", "does-not-exist.md"),
				CannedMediaType: "application/json",
			}, SeatMeta{HarnessName: "codex", ProviderName: "openai", Model: "gpt-5.5", RequireSessionEvidence: true}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"codex"}, "L1"}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if outcome.Passed() {
		t.Fatal("a seat claiming a transcript it cannot produce must not pass the gate")
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictMalfunction {
		t.Errorf("verdict = %s, want malfunction", seat.Verdict)
	}
}

func TestGateStageRemovesNothingFromPriorRun(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	before := readLiveManifest(t, repo, "r1")

	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, true, nil)
	if _, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	}); err != nil {
		t.Fatalf("GateStage: %v", err)
	}

	after := readLiveManifest(t, repo, "r1")
	if len(after.Stages) != len(before.Stages) {
		t.Errorf("stages changed from %d to %d; a gate must only append",
			len(before.Stages), len(after.Stages))
	}
	if after.Stages[0].Output.Artifact != before.Stages[0].Output.Artifact {
		t.Error("the reviewed stage's artifact was rewritten by the gate")
	}
	// The prior stage's artifact bytes must still be readable from the new commit.
	store := refstore.New(repo)
	if _, err := store.ReadFile(context.Background(), runsPrefix+"r1", before.Stages[0].Output.Path); err != nil {
		t.Errorf("prior artifact is no longer readable after the gate: %v", err)
	}
}

// TestGateCheckReceivesAllowlistedEnv pins a defect the first literal
// `etude gate --stage verify` run exposed: execCheckRunner built a hermetic env
// of PATH + ETUDE_* only and ignored the workflow's env_allowlist entirely, so
// the verify stage's own `make test` / `make lint` checks died with
// "go: module cache not found: neither GOMODCACHE nor GOPATH is set" — the gate
// could never pass on the one stage whose acceptance names it. Seats already
// honoured the allowlist; checks did not.
func TestGateCheckReceivesAllowlistedEnv(t *testing.T) {
	t.Setenv("ETUDE_TEST_ALLOWED", "visible")
	t.Setenv("ETUDE_TEST_DENIED", "hidden")

	scratch := filepath.Join(t.TempDir(), "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()

	// A check that reports what it can see, and passes either way: the assertion
	// is on the captured output, not on the exit code.
	script := filepath.Join(t.TempDir(), "probe.sh")
	body := "#!/bin/sh\nprintf 'ALLOWED=[%s] DENIED=[%s]\\n' \"$ETUDE_TEST_ALLOWED\" \"$ETUDE_TEST_DENIED\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	runner, err := ResolveCheckRunner(registry.Registry{}, workflow.Runner{Command: script},
		10*time.Second, []string{"ETUDE_TEST_ALLOWED"})
	if err != nil {
		t.Fatalf("ResolveCheckRunner: %v", err)
	}

	passed, raw, detail := runner.RunCheck(context.Background(), replay.RunRequest{
		WorktreeDir: worktree,
		ScratchDir:  scratch,
	})
	if !passed {
		t.Fatalf("probe check failed: %s", detail)
	}
	out := string(raw)
	if !strings.Contains(out, "ALLOWED=[visible]") {
		t.Errorf("an allowlisted variable did not reach the check: %q", out)
	}
	if !strings.Contains(out, "DENIED=[]") {
		t.Errorf("a NON-allowlisted variable leaked into the check: %q", out)
	}
}
