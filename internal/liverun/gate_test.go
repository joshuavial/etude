package liverun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
)

// ---------------------------------------------------------------------------
// Stub helpers for gate tests
// ---------------------------------------------------------------------------

// stubCheckRunner is a test double for CheckRunner.
type stubCheckRunner struct {
	passed    bool
	rawOutput []byte
	detail    string
}

func (s *stubCheckRunner) RunCheck(_ context.Context, _ replay.RunRequest) (bool, []byte, string) {
	return s.passed, s.rawOutput, s.detail
}

// envelopeJSON encodes a seatEnvelope to JSON bytes.
func envelopeJSON(verdict string, required []string) []byte {
	env := seatEnvelope{Verdict: verdict, Required: required}
	b, _ := json.Marshal(env)
	return b
}

func sessionEnvelopeJSON(verdict string) []byte {
	return sessionEnvelopeJSONWithPath(verdict, "transcript.txt")
}

func sessionEnvelopeJSONWithPath(verdict, transcriptPath string) []byte {
	env := seatEnvelope{
		Verdict: verdict,
		Session: &seatSessionEnvelope{
			SessionID:      "session-123",
			TranscriptPath: transcriptPath,
		},
	}
	b, _ := json.Marshal(env)
	return b
}

type transcriptSeatRunner struct {
	envelope   []byte
	path       string
	transcript []byte
}

func (r transcriptSeatRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	path := r.path
	if path == "" {
		path = "transcript.txt"
	}
	outputPath := filepath.Join(req.ScratchDir, path)
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return replay.RunResult{}, err
	}
	if err := os.WriteFile(outputPath, r.transcript, 0o644); err != nil {
		return replay.RunResult{}, err
	}
	res := replay.RunResult{
		Output:    r.envelope,
		MediaType: "application/json",
		Producer:  req.Producer,
	}
	return res, nil
}

// stubSeats is a call-indexed seat stub: each call index returns the next
// entry from the responses slice (wraps at end).
type stubSeats struct {
	responses [][]byte // each entry: canned envelope JSON or nil for error
	call      int
}

func (s *stubSeats) runner() replay.Runner {
	idx := s.call
	s.call++
	var resp []byte
	if len(s.responses) > 0 {
		resp = s.responses[idx%len(s.responses)]
	}
	return &replay.StubRunner{CannedOutput: resp, CannedMediaType: "application/json"}
}

// fixedTiers returns a Tiers function for the given ladder map.
// ladder maps tier name → (seats, nextStronger).
func fixedTiers(ladder map[string][2]interface{}) func(string) ([]string, string, bool) {
	return func(name string) ([]string, string, bool) {
		v, ok := ladder[name]
		if !ok {
			return nil, "", false
		}
		seats := v[0].([]string)
		next, _ := v[1].(string)
		return seats, next, true
	}
}

// gateTestEngine returns an Engine wired with stub resolvers for gate testing.
// checkPassed: outcome for all checks.
// seatResponses: cyclic list of seat envelope responses (in order of invocation).
// tierLadder: tier name → (seats, nextStronger).
func gateTestEngine(
	repo string,
	resolveStage func(workflow.Stage) (replay.Runner, error),
	checkPassed bool,
	seatResponses [][]byte,
	tierLadder map[string][2]interface{},
) (*Engine, *stubSeats) {
	ss := &stubSeats{responses: seatResponses}
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: resolveStage,
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			detail := ""
			if !checkPassed {
				detail = "check failed"
			}
			return &stubCheckRunner{passed: checkPassed, rawOutput: []byte("check output"), detail: detail}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{
				HarnessName:  "stub-harness",
				ProviderName: "stub-provider",
				Model:        "stub-model",
			}, nil
		},
		Tiers: fixedTiers(tierLadder),
		Root:  repo,
		Now:   fixedClock(),
	}
	return e, ss
}

// gatedWorkflow returns a 1-stage workflow where the single stage has a gate.
func gatedWorkflow(gate *workflow.GateConfig) workflow.Workflow {
	return workflow.Workflow{
		Name: "mywf",
		Stages: []workflow.Stage{
			{
				Name:     "plan",
				Skill:    "sk",
				Produces: "plan",
				Inputs:   []string{"task"},
				Gate:     gate,
			},
		},
	}
}

// maxRoundsPtr returns a pointer to n for use in GateConfig.MaxRounds.
func maxRoundsPtr(n int) *int { return &n }

// passThresholdPtr returns a pointer to f for use in GateConfig.PassThreshold.
func passThresholdPtr(f float64) *float64 { return &f }

// ---------------------------------------------------------------------------
// AC1: records a GateAttempt with the synthesized verdict; manifest_version==3;
//      JSON round-trip with gates.
// ---------------------------------------------------------------------------

func TestGateAC1_RecordsGateAttempt(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// 1 check (pass) + 2 go seats → PASS on round 1.
	goEnv := envelopeJSON("go", nil)
	e, _ := gateTestEngine(repo,
		stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan output"), CannedMediaType: "text/plain; charset=utf-8"}),
		true,                   // check passes
		[][]byte{goEnv, goEnv}, // two go seats
		map[string][2]interface{}{
			"L2": {[]string{"seatA", "seatB"}, "L1"},
		},
	)

	wf := gatedWorkflow(&workflow.GateConfig{
		Checks: []workflow.Runner{{Command: "true"}},
		Tier:   "L2",
	})

	runID := "mywf-20260101T000000Z-gateac01"
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

	// manifest_version == 3 because gates are present.
	raw, err := m.JSON()
	if err != nil {
		t.Fatalf("JSON(): %v", err)
	}
	var doc struct {
		ManifestVersion int `json:"manifest_version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal manifest_version: %v", err)
	}
	if doc.ManifestVersion != 3 {
		t.Errorf("manifest_version = %d, want 3", doc.ManifestVersion)
	}

	// ParseJSON round-trip must succeed.
	m2, err := runmanifest.ParseJSON(raw)
	if err != nil {
		t.Fatalf("ParseJSON round-trip: %v", err)
	}
	if len(m2.Gates) != 1 {
		t.Fatalf("round-trip gates = %d, want 1", len(m2.Gates))
	}

	// Check the gate attempt.
	if len(m.Gates) != 1 {
		t.Fatalf("gates = %d, want 1", len(m.Gates))
	}
	g := m.Gates[0]
	if g.GateID != "plan.r1" {
		t.Errorf("gate_id = %q, want plan.r1", g.GateID)
	}
	if g.Phase != "plan" {
		t.Errorf("phase = %q, want plan", g.Phase)
	}
	if g.Round != 1 {
		t.Errorf("round = %d, want 1", g.Round)
	}
	if g.Tier != 2 { // L2 → 2
		t.Errorf("tier = %d, want 2", g.Tier)
	}
	if g.Status != runmanifest.GateStatusPass {
		t.Errorf("status = %q, want pass", g.Status)
	}

	// reviewed_stages must bind the plan stage output.
	if len(g.ReviewedStages) != 1 {
		t.Fatalf("reviewed_stages = %d, want 1", len(g.ReviewedStages))
	}
	rs := g.ReviewedStages[0]
	if rs.Stage != "plan" {
		t.Errorf("reviewed stage = %q, want plan", rs.Stage)
	}
	if rs.Role != "plan" {
		t.Errorf("reviewed role = %q, want plan", rs.Role)
	}
	if rs.Artifact != m.Stages[0].Output.Artifact {
		t.Errorf("reviewed artifact mismatch: got %q, want %q", rs.Artifact, m.Stages[0].Output.Artifact)
	}

	// Seats: check.0, seatA, seatB.
	if len(g.Seats) != 3 {
		t.Fatalf("seats = %d, want 3", len(g.Seats))
	}
	checkSeat := g.Seats[0]
	if checkSeat.Seat != "check.0" {
		t.Errorf("seat[0].seat = %q, want check.0", checkSeat.Seat)
	}
	if checkSeat.Verdict != runmanifest.SeatVerdictGo {
		t.Errorf("seat[0].verdict = %q, want go", checkSeat.Verdict)
	}
	if checkSeat.Provider.Name != "deterministic" {
		t.Errorf("seat[0].provider.name = %q, want deterministic", checkSeat.Provider.Name)
	}
	for _, s := range g.Seats[1:] {
		if s.Verdict != runmanifest.SeatVerdictGo {
			t.Errorf("seat %q verdict = %q, want go", s.Seat, s.Verdict)
		}
		if s.Provider.Name != "stub-provider" {
			t.Errorf("seat %q provider.name = %q, want stub-provider", s.Seat, s.Provider.Name)
		}
		if s.Provider.Model != "stub-model" {
			t.Errorf("seat %q provider.model = %q, want stub-model", s.Seat, s.Provider.Model)
		}
	}
}

func TestGateAgenticSeatRequiresSessionEvidence(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	goEnv := envelopeJSON("go", nil)
	ss := &stubSeats{responses: [][]byte{goEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	runID := "mywf-20260101T000000Z-session01"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate escalation from missing session evidence, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictMalfunction {
		t.Fatalf("seat verdict = %q, want malfunction", seat.Verdict)
	}
	if seat.FailureNote != "agentic seat did not provide session evidence" {
		t.Fatalf("failure note = %q", seat.FailureNote)
	}
}

func TestGateAgenticSeatStoresTranscriptEvidence(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			runner := transcriptSeatRunner{
				envelope:   sessionEnvelopeJSONWithPath("go", "transcript.md"),
				path:       "transcript.md",
				transcript: []byte("full transcript without secrets"),
			}
			meta := SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}
			return runner, meta, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1"})
	runID := "mywf-20260101T000000Z-session02"
	if err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Session == nil {
		t.Fatal("session evidence missing")
	}
	if seat.Session.SessionID != "session-123" {
		t.Fatalf("session id = %q", seat.Session.SessionID)
	}
	if seat.Session.RetrievalStatus != runmanifest.SessionEvidenceRetrievalImported {
		t.Fatalf("retrieval status = %q", seat.Session.RetrievalStatus)
	}
	if seat.Session.RedactionStatus != runmanifest.SessionEvidenceRedactionPassed {
		t.Fatalf("redaction status = %q", seat.Session.RedactionStatus)
	}
	if seat.Session.TranscriptArtifact == nil {
		t.Fatal("transcript artifact missing")
	}
	if seat.Session.TranscriptArtifact.MediaType != "text/markdown; charset=utf-8" {
		t.Fatalf("transcript media type = %q, want text/markdown; charset=utf-8", seat.Session.TranscriptArtifact.MediaType)
	}
}

func TestGateAgenticSeatFailsClosedOnSecretTranscript(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			runner := transcriptSeatRunner{
				envelope:   sessionEnvelopeJSON("go"),
				transcript: []byte("token ghp_123456789012345678901234567890123456"),
			}
			meta := SeatMeta{
				HarnessName:            "codex",
				ProviderName:           "openai",
				Model:                  "gpt-5.5",
				RequireSessionEvidence: true,
			}
			return runner, meta, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"codex"}, ""},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
	runID := "mywf-20260101T000000Z-session03"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	var gateErr *GateEscalationError
	if !errors.As(err, &gateErr) {
		t.Fatalf("expected gate escalation from secret transcript, got %v", err)
	}
	m := readLiveManifest(t, repo, runID)
	seat := m.Gates[0].Seats[0]
	if seat.Verdict != runmanifest.SeatVerdictMalfunction {
		t.Fatalf("seat verdict = %q, want malfunction", seat.Verdict)
	}
	if seat.Session == nil || seat.Session.RedactionStatus != runmanifest.SessionEvidenceFailed {
		t.Fatalf("redaction status = %#v, want failed", seat.Session)
	}
}

// ---------------------------------------------------------------------------
// AC2: failing check hard-blocks regardless of seat votes.
// ---------------------------------------------------------------------------

func TestGateAC2_FailingCheckHardBlocks(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// Check fails; 2 go seats — gate must NOT pass.
	// With max_rounds=1, should end up ESCALATED (no stronger tier → error).
	goEnv := envelopeJSON("go", nil)
	e, _ := gateTestEngine(repo,
		stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan output"), CannedMediaType: "text/plain; charset=utf-8"}),
		false, // check FAILS
		[][]byte{goEnv, goEnv},
		map[string][2]interface{}{
			"L1": {[]string{"seatA", "seatB"}, ""}, // L1 = top tier, no stronger
		},
	)

	one := 1
	wf := gatedWorkflow(&workflow.GateConfig{
		Checks:    []workflow.Runner{{Command: "false"}},
		Tier:      "L1",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac02"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	// Must escalate because check failed and max_rounds=1.
	var gateEscErr *GateEscalationError
	if !errors.As(err, &gateEscErr) {
		t.Fatalf("expected GateEscalationError, got: %v", err)
	}
	if gateEscErr.Phase != "plan" {
		t.Errorf("phase = %q, want plan", gateEscErr.Phase)
	}

	// The partial run is inspectable.
	m := readLiveManifest(t, repo, runID)
	if len(m.Gates) != 1 {
		t.Fatalf("gates = %d, want 1", len(m.Gates))
	}
	g := m.Gates[0]
	if g.Status != runmanifest.GateStatusEscalated {
		t.Errorf("status = %q, want escalated", g.Status)
	}

	// The failing check must be recorded as block (not go).
	checkSeat := g.Seats[0]
	if checkSeat.Seat != "check.0" {
		t.Errorf("seat[0].seat = %q, want check.0", checkSeat.Seat)
	}
	if checkSeat.Verdict != runmanifest.SeatVerdictBlock {
		t.Errorf("check seat verdict = %q, want block", checkSeat.Verdict)
	}
	// Both seats voted go but gate still didn't pass.
	for _, s := range g.Seats[1:] {
		if s.Verdict != runmanifest.SeatVerdictGo {
			t.Errorf("seat %q verdict = %q, want go despite check failure", s.Seat, s.Verdict)
		}
	}
}

// ---------------------------------------------------------------------------
// AC3: rerun re-executes stage with gate-feedback in its inputs + round bump.
// ---------------------------------------------------------------------------

func TestGateAC3_RerunWithFeedback(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// Round 1: seats block → RERUN.
	// Round 2: seats go → PASS.
	blockEnv := envelopeJSON("block", []string{"fix the plan"})
	goEnv := envelopeJSON("go", nil)

	// Seat stub returns block on first 2 calls (round 1: 2 seats), go on next 2 (round 2).
	seatResponses := [][]byte{blockEnv, blockEnv, goEnv, goEnv}

	// Stage runner: 1st call = original; 2nd call = rerun (must see gate-feedback input).
	stageCallCount := 0
	var rerunSawFeedback bool
	resolveStage := func(stage workflow.Stage) (replay.Runner, error) {
		call := stageCallCount
		stageCallCount++
		if call == 0 {
			return &replay.StubRunner{CannedOutput: []byte("plan v1"), CannedMediaType: "text/plain; charset=utf-8"}, nil
		}
		// Rerun: return a runner that inspects inputs.
		return &feedbackCheckRunner{
			output:      []byte("plan v2"),
			mediaType:   "text/plain; charset=utf-8",
			sawFeedback: &rerunSawFeedback,
		}, nil
	}

	ss := &stubSeats{responses: seatResponses}
	two := 2
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: resolveStage,
		ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
			return &stubCheckRunner{passed: true}, nil
		},
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L2": {[]string{"seatA", "seatB"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}
	_ = e.ResolveCheck // ensure ResolveCheck is set; checks are configured but pass

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L2",
		MaxRounds: &two,
	})

	runID := "mywf-20260101T000000Z-gateac03"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !rerunSawFeedback {
		t.Error("rerun stage runner did not receive gate-feedback input")
	}

	m := readLiveManifest(t, repo, runID)

	// Two gate attempts: r1 rerun, r2 pass.
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(m.Gates))
	}
	if m.Gates[0].Status != runmanifest.GateStatusRerun {
		t.Errorf("gate[0].status = %q, want rerun", m.Gates[0].Status)
	}
	if m.Gates[1].Status != runmanifest.GateStatusPass {
		t.Errorf("gate[1].status = %q, want pass", m.Gates[1].Status)
	}

	// Round numbers must match.
	if m.Gates[0].Round != 1 {
		t.Errorf("gate[0].round = %d, want 1", m.Gates[0].Round)
	}
	if m.Gates[1].Round != 2 {
		t.Errorf("gate[1].round = %d, want 2", m.Gates[1].Round)
	}

	// A second Stage named "plan.r2" must exist with gate-feedback in its Inputs.
	foundRerunStage := false
	for _, s := range m.Stages {
		if s.Name == "plan.r2" {
			foundRerunStage = true
			hasFeedback := false
			for _, inp := range s.Inputs {
				if inp.Role == "gate-feedback" {
					hasFeedback = true
					break
				}
			}
			if !hasFeedback {
				t.Error("plan.r2 stage has no gate-feedback input")
			}
			// chain role unchanged: output role is still "plan".
			if s.Output.Role != "plan" {
				t.Errorf("plan.r2 output role = %q, want plan", s.Output.Role)
			}
		}
	}
	if !foundRerunStage {
		t.Errorf("no stage named plan.r2 found in manifest stages: %v",
			func() []string {
				names := make([]string, 0, len(m.Stages))
				for _, s := range m.Stages {
					names = append(names, s.Name)
				}
				return names
			}())
	}

	// Gate r2 reviewed_stages must reference plan.r2 (not the original plan).
	if m.Gates[1].ReviewedStages[0].Stage != "plan.r2" {
		t.Errorf("gate[1] reviewed stage = %q, want plan.r2", m.Gates[1].ReviewedStages[0].Stage)
	}
}

// feedbackCheckRunner is a replay.Runner that records whether it received a
// gate-feedback input.
type feedbackCheckRunner struct {
	output      []byte
	mediaType   string
	sawFeedback *bool
}

func (r *feedbackCheckRunner) Run(_ context.Context, req replay.RunRequest) (replay.RunResult, error) {
	for _, inp := range req.Inputs {
		if inp.Role == "gate-feedback" {
			*r.sawFeedback = true
			break
		}
	}
	return replay.RunResult{Output: r.output, MediaType: r.mediaType, Producer: req.Producer}, nil
}

// ---------------------------------------------------------------------------
// AC4: escalation advances tier; terminal escalation → GateEscalationError.
// ---------------------------------------------------------------------------

func TestGateAC4_EscalationAdvancesTier(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// L3: single seat blocks → ESCALATED (max_rounds=1).
	// L2: two seats go → PASS.
	blockEnv := envelopeJSON("block", []string{"needs work"})
	goEnv := envelopeJSON("go", nil)

	// Responses: 1 block (L3 round 1), 2 go (L2 round 2).
	ss := &stubSeats{responses: [][]byte{blockEnv, goEnv, goEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L3": {[]string{"seatA"}, "L2"},
			"L2": {[]string{"seatB", "seatC"}, "L1"},
		}),
		Root: repo,
		Now:  fixedClock(),
	}

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L3",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac04a"
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
	if len(m.Gates) != 2 {
		t.Fatalf("gates = %d, want 2", len(m.Gates))
	}

	// First attempt: L3 (tier=3), escalated.
	g0 := m.Gates[0]
	if g0.Tier != 3 {
		t.Errorf("gate[0].tier = %d, want 3", g0.Tier)
	}
	if g0.Status != runmanifest.GateStatusEscalated {
		t.Errorf("gate[0].status = %q, want escalated", g0.Status)
	}
	if g0.Decision.EscalationReason == "" {
		t.Error("gate[0].escalation_reason must not be empty")
	}

	// Second attempt: L2 (tier=2), pass.
	g1 := m.Gates[1]
	if g1.Tier != 2 {
		t.Errorf("gate[1].tier = %d, want 2", g1.Tier)
	}
	if g1.Status != runmanifest.GateStatusPass {
		t.Errorf("gate[1].status = %q, want pass", g1.Status)
	}

	// Rounds are monotonically increasing.
	if g0.Round >= g1.Round {
		t.Errorf("rounds not monotonic: gate[0].round=%d gate[1].round=%d", g0.Round, g1.Round)
	}
}

func TestGateAC4_TerminalEscalation(t *testing.T) {
	repo := initTestRepo(t)
	sha := headSHA(t, repo)

	// L1 is the top tier (no stronger). Seat blocks → ESCALATED → GateEscalationError.
	blockEnv := envelopeJSON("block", []string{"still blocked"})
	ss := &stubSeats{responses: [][]byte{blockEnv, blockEnv}}
	one := 1
	e := &Engine{
		Store:         refstore.New(repo),
		ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
		ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
		ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
			return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
		},
		Tiers: fixedTiers(map[string][2]interface{}{
			"L1": {[]string{"seatA", "seatB"}, ""}, // top: no stronger
		}),
		Root: repo,
		Now:  fixedClock(),
	}

	wf := gatedWorkflow(&workflow.GateConfig{
		Tier:      "L1",
		MaxRounds: &one,
	})

	runID := "mywf-20260101T000000Z-gateac04b"
	err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
		TaskBytes: []byte("task"),
		TaskFile:  "task.txt",
		RunID:     runID,
		GitSHA:    sha,
	})

	var gateEscErr *GateEscalationError
	if !errors.As(err, &gateEscErr) {
		t.Fatalf("expected GateEscalationError, got: %v", err)
	}
	if gateEscErr.Phase != "plan" {
		t.Errorf("phase = %q, want plan", gateEscErr.Phase)
	}
	if gateEscErr.RunID != runID {
		t.Errorf("run_id = %q, want %q", gateEscErr.RunID, runID)
	}

	// Partial run must be valid and inspectable.
	m := readLiveManifest(t, repo, runID)
	if len(m.Stages) == 0 {
		t.Error("partial run has no stages")
	}
	if len(m.Gates) != 1 {
		t.Fatalf("partial run gates = %d, want 1 (the escalated attempt)", len(m.Gates))
	}
	if m.Gates[0].Status != runmanifest.GateStatusEscalated {
		t.Errorf("gate status = %q, want escalated", m.Gates[0].Status)
	}
}

// ---------------------------------------------------------------------------
// AC5: fail-closed cases.
// ---------------------------------------------------------------------------

func TestGateAC5_FailClosed(t *testing.T) {
	t.Run("errored-seat-escalates", func(t *testing.T) {
		// 2 seats, one errors (Err set) → usable=1 < min(2,2) → ESCALATED immediately.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		callCount := 0
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				callCount++
				meta := SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}
				if callCount == 1 {
					return &replay.StubRunner{Err: errors.New("seat error")}, meta, nil
				}
				return &replay.StubRunner{CannedOutput: goEnv, CannedMediaType: "application/json"}, meta, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L1": {[]string{"seatA", "seatB"}, ""}, // top: no stronger
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{
			Tier:      "L1",
			MaxRounds: &one,
		})

		runID := "mywf-20260101T000000Z-gateac05a"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})

		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError (insufficient usable), got: %v", err)
		}

		m := readLiveManifest(t, repo, runID)
		if len(m.Gates) != 1 {
			t.Fatalf("gates = %d, want 1", len(m.Gates))
		}
		g := m.Gates[0]
		if g.Status != runmanifest.GateStatusEscalated {
			t.Errorf("status = %q, want escalated", g.Status)
		}
		// Find the errored seat and verify it's failed with a failure_note.
		foundFailed := false
		for _, s := range g.Seats {
			if s.Verdict == runmanifest.SeatVerdictFailed {
				foundFailed = true
				if s.FailureNote == "" {
					t.Error("errored seat has no failure_note")
				}
			}
		}
		if !foundFailed {
			t.Error("no failed seat found in gate seats")
		}
	})

	t.Run("malformed-envelope-malfunction", func(t *testing.T) {
		// Seat returns non-JSON → malfunction.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		// Two malfunction seats → usable=0 < min(2,2) → ESCALATED.
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				meta := SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}
				return &replay.StubRunner{CannedOutput: []byte("not json"), CannedMediaType: "text/plain"}, meta, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L1": {[]string{"seatA", "seatB"}, ""},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L1", MaxRounds: &one})
		runID := "mywf-20260101T000000Z-gateac05b"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})

		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError, got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		for _, s := range m.Gates[0].Seats {
			if s.Verdict != runmanifest.SeatVerdictMalfunction {
				t.Errorf("seat %q verdict = %q, want malfunction", s.Seat, s.Verdict)
			}
			if s.FailureNote == "" {
				t.Errorf("seat %q has no failure_note for malfunction", s.Seat)
			}
		}
	})

	t.Run("threshold-0.5-two-go-one-block-passes", func(t *testing.T) {
		// 3 seats, pass_threshold=0.5, 2 go + 1 block → 0.67 >= 0.5 → PASS.
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		blockEnv := envelopeJSON("block", []string{"fix it"})
		ss := &stubSeats{responses: [][]byte{goEnv, goEnv, blockEnv}}
		pt := 0.5
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				"L3": {[]string{"seatA", "seatB", "seatC"}, "L2"},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L3", PassThreshold: &pt})
		runID := "mywf-20260101T000000Z-gateac05c1"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		if err != nil {
			t.Fatalf("expected pass with threshold 0.5 (2go/1block), got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		if m.Gates[0].Status != runmanifest.GateStatusPass {
			t.Errorf("status = %q, want pass", m.Gates[0].Status)
		}
	})

	t.Run("threshold-1.0-two-go-one-block-reruns", func(t *testing.T) {
		// Same seats but threshold=1.0 → 0.67 < 1.0 → not pass.
		// With max_rounds=1 → ESCALATED (top tier L3 → L2 not in map → terminal).
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		goEnv := envelopeJSON("go", nil)
		blockEnv := envelopeJSON("block", []string{"fix it"})
		ss := &stubSeats{responses: [][]byte{goEnv, goEnv, blockEnv}}
		pt := 1.0
		one := 1
		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck:  func(r workflow.Runner) (CheckRunner, error) { return &stubCheckRunner{passed: true}, nil },
			ResolveSeat: func(seatName string) (replay.Runner, SeatMeta, error) {
				return ss.runner(), SeatMeta{HarnessName: "stub", ProviderName: "stub", Model: "stub"}, nil
			},
			Tiers: fixedTiers(map[string][2]interface{}{
				// Only L3 in map; no L2 → nextStronger = "" → terminal on escalate.
				"L3": {[]string{"seatA", "seatB", "seatC"}, ""},
			}),
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{Tier: "L3", PassThreshold: &pt, MaxRounds: &one})
		runID := "mywf-20260101T000000Z-gateac05c2"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		var gateEscErr *GateEscalationError
		if !errors.As(err, &gateEscErr) {
			t.Fatalf("expected GateEscalationError with threshold 1.0 (2go/1block), got: %v", err)
		}
	})

	t.Run("checks-only-gate-passes", func(t *testing.T) {
		// No seats, only a passing check → PASS (checks-only gate).
		repo := initTestRepo(t)
		sha := headSHA(t, repo)

		e := &Engine{
			Store:         refstore.New(repo),
			ResolveRunner: stubResolveRunner(&replay.StubRunner{CannedOutput: []byte("plan"), CannedMediaType: "text/plain; charset=utf-8"}),
			ResolveCheck: func(r workflow.Runner) (CheckRunner, error) {
				return &stubCheckRunner{passed: true}, nil
			},
			// ResolveSeat / Tiers not set: no seats in this gate.
			Root: repo,
			Now:  fixedClock(),
		}

		wf := gatedWorkflow(&workflow.GateConfig{
			Checks: []workflow.Runner{{Command: "true"}},
		})
		runID := "mywf-20260101T000000Z-gateac05d"
		err := e.Run(context.Background(), noopWriter(), wf, RunOptions{
			TaskBytes: []byte("task"), TaskFile: "task.txt", RunID: runID, GitSHA: sha,
		})
		if err != nil {
			t.Fatalf("checks-only gate: expected pass, got: %v", err)
		}
		m := readLiveManifest(t, repo, runID)
		if len(m.Gates) != 1 {
			t.Fatalf("gates = %d, want 1", len(m.Gates))
		}
		if m.Gates[0].Status != runmanifest.GateStatusPass {
			t.Errorf("status = %q, want pass", m.Gates[0].Status)
		}
	})
}

// ---------------------------------------------------------------------------
// Unit tests for synthesis and helpers
// ---------------------------------------------------------------------------

func TestSynthesizeVerdict(t *testing.T) {
	tests := []struct {
		name           string
		checksPassed   []bool
		seatVerdicts   []runmanifest.SeatVerdict
		tierRound      int
		maxRounds      int
		passThreshold  float64
		expectedSeats  int
		wantStatus     runmanifest.GateStatus
		wantEscalation bool
	}{
		{
			name:         "all-checks-pass-no-seats",
			checksPassed: []bool{true},
			seatVerdicts: nil,
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 0,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "check-fails-rerun",
			checksPassed: []bool{false},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusRerun,
		},
		{
			name:         "check-fails-max-rounds-escalates",
			checksPassed: []bool{false},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    3, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusEscalated, wantEscalation: true,
		},
		{
			name:         "insufficient-usable-escalates",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictFailed, runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusEscalated, wantEscalation: true,
		},
		{
			name:         "single-seat-one-usable-passes",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 1,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "threshold-met-passes",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictGo, runmanifest.SeatVerdictBlock},
			tierRound:    1, maxRounds: 3, passThreshold: 0.5, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusPass,
		},
		{
			name:         "threshold-not-met-reruns",
			checksPassed: []bool{true},
			seatVerdicts: []runmanifest.SeatVerdict{runmanifest.SeatVerdictBlock, runmanifest.SeatVerdictBlock},
			tierRound:    1, maxRounds: 3, passThreshold: 1.0, expectedSeats: 2,
			wantStatus: runmanifest.GateStatusRerun,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			syn := synthesizeVerdict(tc.checksPassed, tc.seatVerdicts, tc.tierRound, tc.maxRounds, tc.passThreshold, tc.expectedSeats)
			if syn.status != tc.wantStatus {
				t.Errorf("status = %q, want %q", syn.status, tc.wantStatus)
			}
			if tc.wantEscalation && syn.escalationReason == "" {
				t.Error("escalation_reason must not be empty for escalated status")
			}
		})
	}
}

func TestTierToInt(t *testing.T) {
	tests := []struct {
		name string
		want int
	}{
		{"L1", 1}, {"L2", 2}, {"L3", 3},
		{"L4", 0}, {"", 0}, {"inline", 0}, {"L", 0}, {"L10", 0},
	}
	for _, tc := range tests {
		if got := tierToInt(tc.name); got != tc.want {
			t.Errorf("tierToInt(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestSplitProvider(t *testing.T) {
	tests := []struct{ s, name, model string }{
		{"anthropic/claude-opus", "anthropic", "claude-opus"},
		{"openai/gpt-5", "openai", "gpt-5"},
		{"singlename", "singlename", "singlename"},
		{"a/b/c", "a", "b/c"}, // split on FIRST slash only
	}
	for _, tc := range tests {
		n, m := splitProvider(tc.s)
		if n != tc.name || m != tc.model {
			t.Errorf("splitProvider(%q) = (%q, %q), want (%q, %q)", tc.s, n, m, tc.name, tc.model)
		}
	}
}

// noopWriter returns an io.Writer that discards all output.
func noopWriter() *nopW { return &nopW{} }

type nopW struct{}

func (*nopW) Write(p []byte) (int, error) { return len(p), nil }

// Ensure noopWriter is used as io.Writer.
var _ interface{ Write([]byte) (int, error) } = (*nopW)(nil)

// Compile-time check that fixedClock is available (defined in engine_test.go).
var _ = fixedClock

// Compile-time check that time is imported (used in SeatResult.Timestamp).
var _ = time.Now
