package liverun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
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

// ladderRunner records that it ran and returns a canned outcome.
type ladderRunner struct {
	envelope []byte
	err      error
	ran      *int
}

func (r ladderRunner) Run(_ context.Context, _ replay.RunRequest) (replay.RunResult, error) {
	if r.ran != nil {
		*r.ran++
	}
	if r.err != nil {
		return replay.RunResult{}, r.err
	}
	return replay.RunResult{Output: r.envelope, MediaType: "application/json"}, nil
}

// ladderEngine wires an Engine whose seat resolves to the given candidates.
func ladderEngine(t *testing.T, repo string, candidates []SeatCandidate) *Engine {
	t.Helper()
	return &Engine{
		Store: refstore.New(repo),
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) {
			if len(candidates) == 0 {
				return nil, SeatMeta{}, errors.New("no candidates")
			}
			return candidates[0].Runner, candidates[0].Meta, nil
		},
		ResolveSeatCandidates: func(string) ([]SeatCandidate, error) { return candidates, nil },
		Tiers:                 fixedTiers(map[string][2]interface{}{"L2": {[]string{"opus"}, "L1"}}),
		Root:                  repo,
		Now:                   fixedClock(),
	}
}

func ladderCandidate(harness, invoke string, runner replay.Runner) SeatCandidate {
	return SeatCandidate{
		Harness:   harness,
		Invoke:    invoke,
		InHarness: strings.HasPrefix(invoke, InHarnessPrefix),
		Runner:    runner,
		Meta:      SeatMeta{HarnessName: harness, ProviderName: "anthropic", Model: "claude-opus"},
	}
}

// gateOnceDocsStage mirrors the workflow's `docs` stage, which is the ONLY stage
// whose produced role (docs-diff) differs from its own name (docs).
func gateOnceDocsStage() workflow.Stage {
	return workflow.Stage{
		Name:     "docs",
		Produces: "docs-diff",
		Gate: &workflow.GateConfig{
			Tier:        "L2",
			Abstraction: "docs match implemented behavior; docs policy",
		},
	}
}

// TestGateStageResolvesDocsStageByRoleNotName is the regression for etude-1od.
//
// It is deliberately NOT a copy of TestGateStageRunWithoutReviewableStageIsAClearError,
// which pins the generic "no stage for this role" path. What is pinned here is the
// thing that produced the bead: resolution keys on the stage's PRODUCED ROLE, and
// `docs` is the one stage where that role (docs-diff) differs from the stage name.
// A resolver that keyed on the stage name instead would pass every other stage in
// the workflow and fail only this one — which is exactly how the original defect
// stayed invisible.
func TestGateStageResolvesDocsStageByRoleNotName(t *testing.T) {
	repo := initTestRepo(t)
	// The captured stage is named `docs` and produces `docs-diff`.
	seedGateRun(t, repo, "r1", "docs", "docs-diff")
	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, true, nil)

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceDocsStage(), Artifact: []byte("docs artifact"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("a docs stage produced by role docs-diff must be gateable: %v", err)
	}
	if !outcome.Passed() {
		t.Fatalf("expected pass, got %s", outcome.Status)
	}
	reviewed := outcome.Attempt.ReviewedStages
	if len(reviewed) != 1 {
		t.Fatalf("expected one reviewed stage, got %+v", reviewed)
	}
	if reviewed[0].Stage != "docs" || reviewed[0].Role != "docs-diff" {
		t.Errorf("reviewed stage = %+v; want stage docs with role docs-diff", reviewed[0])
	}
}

// TestGateStageDocsWithoutDocsDiffStageWritesNothing pins the half the generic
// test does not: on the failure path the run ref must be left completely
// untouched. The original defect was capture-gate REJECTING a gate whose
// reviewed_stages named an absent stage; etude gate has to refuse upstream
// without leaving a partial attempt behind.
func TestGateStageDocsWithoutDocsDiffStageWritesNothing(t *testing.T) {
	repo := initTestRepo(t)
	// A run that has a `docs`-NAMED stage but producing the wrong role. A
	// name-keyed resolver would wrongly accept this.
	seedGateRun(t, repo, "r1", "docs", "verify")
	before := readLiveManifest(t, repo, "r1")

	e := gateOnceEngine(t, repo, [][]byte{goEnvelope(), goEnvelope()}, true, nil)
	_, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceDocsStage(), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if !errors.Is(err, ErrNoReviewableStage) {
		t.Fatalf("a stage NAMED docs but producing another role must not satisfy the docs gate; got %v", err)
	}

	// "Untouched" means untouched: compare the whole manifest, not just counts.
	// A count check would pass if a stage were swapped for another, or if any
	// field on the existing attempt were rewritten.
	after := readLiveManifest(t, repo, "r1")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the failure path modified the run manifest\n before: %+v\n  after: %+v", before, after)
	}
	if len(after.Gates) != 0 {
		t.Errorf("the failure path wrote %d gate attempts; it must write none", len(after.Gates))
	}
}

// TestSeatLadderResolvesCandidatesOncePerSeat: a resolver is a caller-supplied
// closure and may be stateful, so resolving twice per seat is both wasteful and
// surprising. The first cut called it in resolveSeatRunner AND runSeatLadder.
func TestSeatLadderResolvesCandidatesOncePerSeat(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	resolves := 0
	cands := []SeatCandidate{ladderCandidate("claude-code", "primary", ladderRunner{envelope: goEnvelope()})}
	e := &Engine{
		Store:       refstore.New(repo),
		ResolveSeat: func(string) (replay.Runner, SeatMeta, error) { return cands[0].Runner, cands[0].Meta, nil },
		ResolveSeatCandidates: func(string) ([]SeatCandidate, error) {
			resolves++
			return cands, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{"L2": {[]string{"opus"}, "L1"}}),
		Root:  repo,
		Now:   fixedClock(),
	}

	if _, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	}); err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if resolves != 1 {
		t.Errorf("candidate resolver called %d times for one seat, want 1", resolves)
	}
}

// TestSeatLadderPrimarySucceedsFallbackUntouched: the common case must not
// change — a working primary means no fallback is ever invoked.
func TestSeatLadderPrimarySucceedsFallbackUntouched(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	primaryRuns, fallbackRuns := 0, 0
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code", "primary", ladderRunner{envelope: goEnvelope(), ran: &primaryRuns}),
		ladderCandidate("agy", "fallback", ladderRunner{envelope: goEnvelope(), ran: &fallbackRuns}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if !outcome.Passed() {
		t.Fatalf("expected pass, got %s", outcome.Status)
	}
	if primaryRuns != 1 || fallbackRuns != 0 {
		t.Errorf("primary ran %d times, fallback %d; want 1 and 0", primaryRuns, fallbackRuns)
	}
	if h := outcome.Attempt.Seats[0].Harness.Name; h != "claude-code" {
		t.Errorf("recorded harness = %q, want the primary's", h)
	}
}

// TestSeatLadderFallsThroughAndRecordsTheHarnessThatRan is the bead's point: a
// failed primary must not be a flat outage when a working fallback exists, and
// the record must name the harness that actually produced the verdict.
func TestSeatLadderFallsThroughAndRecordsTheHarnessThatRan(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code", "primary", ladderRunner{err: errors.New("not logged in")}),
		ladderCandidate("agy", "fallback", ladderRunner{envelope: goEnvelope()}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictGo {
		t.Fatalf("verdict = %s, want go from the fallback", seat.Verdict)
	}
	if seat.Harness.Name != "agy" {
		t.Errorf("recorded harness = %q, want the FALLBACK's (agy)", seat.Harness.Name)
	}
	// On a USABLE verdict the manifest forbids a failure_note, so the fallthrough
	// is recorded at gate level instead — the gate did run degraded.
	if seat.FailureNote != "" {
		t.Errorf("a go verdict must carry no failure_note (the manifest rejects it): %q", seat.FailureNote)
	}
	if !strings.Contains(outcome.Attempt.Decision.DegradedReason, "CANDIDATE_FAILED harness=claude-code") {
		t.Errorf("the failed rung was not recorded in degraded_reason: %q", outcome.Attempt.Decision.DegradedReason)
	}
}

// TestSeatLadderBlockFromPrimaryIsNeverReplacedByAFallbackGo is the
// safety-critical half of the stopping rule, and the one invariant whose
// violation would silently loosen EVERY gate: a BLOCK is a real verdict, not an
// outage, so it must terminate the ladder. If a blocking primary fell through,
// any seat with a configured fallback could have its objection overwritten by a
// more agreeable rung — the gate would pass on work a reviewer rejected.
func TestSeatLadderBlockFromPrimaryIsNeverReplacedByAFallbackGo(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	fallbackRuns := 0
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code", "primary", ladderRunner{envelope: blockEnvelope()}),
		ladderCandidate("agy", "fallback", ladderRunner{envelope: goEnvelope(), ran: &fallbackRuns}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if fallbackRuns != 0 {
		t.Fatalf("a BLOCKING primary fell through to a fallback (%d runs); a block is a verdict, not an outage", fallbackRuns)
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictBlock {
		t.Fatalf("verdict = %s, want the primary's block to survive", seat.Verdict)
	}
	if seat.Harness.Name != "claude-code" {
		t.Errorf("recorded harness = %q, want the primary's", seat.Harness.Name)
	}
	if outcome.Passed() {
		t.Fatal("the gate passed despite a seat blocking")
	}
}

// TestSeatLadderSkipsInHarnessAndKeepsGoing: an in-harness rung is never exec'd
// and must NOT terminate the walk — the repo's own opus ladder has an exec-able
// candidate after it.
func TestSeatLadderSkipsInHarnessAndKeepsGoing(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	inHarnessRuns := 0
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code", "primary", ladderRunner{err: errors.New("not logged in")}),
		ladderCandidate("claude-code-subagent", InHarnessPrefix+"task subagent_type=general-purpose model=opus",
			ladderRunner{envelope: goEnvelope(), ran: &inHarnessRuns}),
		ladderCandidate("agy", "fallback", ladderRunner{envelope: goEnvelope()}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if inHarnessRuns != 0 {
		t.Errorf("an in-harness candidate was EXECUTED %d times; it must never be run", inHarnessRuns)
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Harness.Name != "agy" {
		t.Errorf("the walk stopped at the in-harness rung; harness = %q, want agy", seat.Harness.Name)
	}
	if !strings.Contains(outcome.Attempt.Decision.DegradedReason, "IN_HARNESS_CANDIDATE_SKIPPED harness=claude-code-subagent") {
		t.Errorf("the skipped in-harness candidate was not reported: %q", outcome.Attempt.Decision.DegradedReason)
	}
}

// TestSeatLadderExhaustedRecordsEveryRungAndAHarness: when nothing works, the
// note names each rung AND harness.name must be non-empty — runmanifest rejects
// a blank one, which would turn a recorded outage into no record at all.
func TestSeatLadderExhaustedRecordsEveryRungAndAHarness(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code", "primary", ladderRunner{err: errors.New("not logged in")}),
		ladderCandidate("agy", "fallback", ladderRunner{err: errors.New("quota reached")}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if outcome.Passed() {
		t.Fatal("an exhausted ladder must not pass")
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Harness.Name == "" {
		t.Error("harness.name is empty; runmanifest rejects that, so the attempt would not record")
	}
	for _, want := range []string{"harness=claude-code", "harness=agy"} {
		if !strings.Contains(seat.FailureNote, want) {
			t.Errorf("note does not name rung %s: %q", want, seat.FailureNote)
		}
	}
	// The attempt must actually be on the run, not just in memory.
	if m := readLiveManifest(t, repo, "r1"); len(m.Gates) != 1 {
		t.Fatalf("expected the failed attempt to be recorded, got %d gates", len(m.Gates))
	}
}

// TestSeatLadderAllInHarnessRunsNothing: a ladder etude cannot run at all is a
// failure, not an empty result — "produced no output" would imply an attempt.
func TestSeatLadderAllInHarnessRunsNothing(t *testing.T) {
	repo := initTestRepo(t)
	seedGateRun(t, repo, "r1", "verify", "verify")
	runs := 0
	e := ladderEngine(t, repo, []SeatCandidate{
		ladderCandidate("claude-code-subagent", InHarnessPrefix+"task model=opus",
			ladderRunner{envelope: goEnvelope(), ran: &runs}),
	})

	outcome, err := e.GateStage(context.Background(), io.Discard, GateRequest{
		RunID: "r1", Stage: gateOnceStage(nil), Artifact: []byte("x"),
		WorktreeDir: repo, ScratchDir: gateScratch(t),
	})
	if err != nil {
		t.Fatalf("GateStage: %v", err)
	}
	if runs != 0 {
		t.Errorf("etude executed an in-harness candidate %d times", runs)
	}
	if outcome.Passed() {
		t.Fatal("a seat etude could not run must not pass the gate")
	}
	seat := outcome.Attempt.Seats[0]
	if seat.Harness.Name == "" {
		t.Error("harness.name is empty; the attempt would not record")
	}
	if !strings.Contains(seat.FailureNote, "IN_HARNESS_CANDIDATE_SKIPPED") {
		t.Errorf("note should name the candidate the host must run: %q", seat.FailureNote)
	}
}
