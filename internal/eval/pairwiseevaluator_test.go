package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// recordingJudge is a test double that returns canned responses in sequence
// and records each JudgeRequest it receives.
type recordingJudge struct {
	responses []JudgeResponse
	errors    []error
	recorded  []JudgeRequest
	callCount int
}

func (r *recordingJudge) Judge(_ context.Context, req JudgeRequest) (JudgeResponse, error) {
	idx := r.callCount
	r.callCount++
	r.recorded = append(r.recorded, req)
	if idx < len(r.errors) && r.errors[idx] != nil {
		return JudgeResponse{}, r.errors[idx]
	}
	if idx < len(r.responses) {
		return r.responses[idx], nil
	}
	// default: winner A, no error
	return JudgeResponse{Winner: WinnerA}, nil
}

// makeTarget builds a minimal EvalInput with a stable artifact id.
func makeTarget(role, artifact string, content []byte) EvalInput {
	return EvalInput{
		Role:    role,
		Content: content,
		Source:  ArtifactSource{Artifact: artifact},
	}
}

// hexArtifact returns a 64-char lowercase hex string filled with the given byte.
func hexArtifact(b byte) string {
	return strings.Repeat(string([]byte{hexByte(b >> 4), hexByte(b & 0xf)}), 32)
}

func hexByte(n byte) byte {
	if n < 10 {
		return '0' + n
	}
	return 'a' + n - 10
}

// validArtifact builds a 64-char all-zero lowercase hex string with controlled variation.
func validArtifact(prefix string) string {
	s := prefix + strings.Repeat("0", 64-len(prefix))
	return s[:64]
}

// ---- Head-validation tests ----

func TestPairwiseEvaluator_RejectWrongMethod(t *testing.T) {
	p := &PairwiseEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerA}}}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method:  "rubric",
		Targets: []EvalInput{makeTarget("a", validArtifact("aa"), []byte("x")), makeTarget("b", validArtifact("bb"), []byte("y"))},
	})
	if err == nil || !strings.Contains(err.Error(), "pairwise") {
		t.Fatalf("want pairwise method error, got %v", err)
	}
}

func TestPairwiseEvaluator_RejectRubricConfig(t *testing.T) {
	p := &PairwiseEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerA}}}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method:  "pairwise",
		Rubric:  &RubricRef{Path: "r.md", Version: "abc"},
		Targets: []EvalInput{makeTarget("a", validArtifact("aa"), []byte("x")), makeTarget("b", validArtifact("bb"), []byte("y"))},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "rubric") {
		t.Fatalf("want rubric error, got %v", err)
	}
}

func TestPairwiseEvaluator_RejectAssertionConfig(t *testing.T) {
	p := &PairwiseEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerA}}}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method:    "pairwise",
		Assertion: &AssertionSpec{Checks: []AssertionCheck{{Kind: "exact"}}},
		Targets:   []EvalInput{makeTarget("a", validArtifact("aa"), []byte("x")), makeTarget("b", validArtifact("bb"), []byte("y"))},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "assertion") {
		t.Fatalf("want assertion error, got %v", err)
	}
}

func TestPairwiseEvaluator_RejectWrongTargetCount(t *testing.T) {
	p := &PairwiseEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerA}}}
	for _, targets := range [][]EvalInput{
		{},
		{makeTarget("a", validArtifact("aa"), []byte("x"))},
		{makeTarget("a", validArtifact("aa"), []byte("x")), makeTarget("b", validArtifact("bb"), []byte("y")), makeTarget("c", validArtifact("cc"), []byte("z"))},
	} {
		_, err := p.Evaluate(context.Background(), EvalRequest{Method: "pairwise", Targets: targets})
		if err == nil || !strings.Contains(err.Error(), "2 targets") {
			t.Fatalf("len=%d: want '2 targets' error, got %v", len(targets), err)
		}
	}
}

func TestPairwiseEvaluator_NilJudge(t *testing.T) {
	p := &PairwiseEvaluator{}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method:  "pairwise",
		Targets: []EvalInput{makeTarget("a", validArtifact("aa"), []byte("x")), makeTarget("b", validArtifact("bb"), []byte("y"))},
	})
	if !errors.Is(err, ErrJudgeNotConfigured) {
		t.Fatalf("want ErrJudgeNotConfigured, got %v", err)
	}
}

// ---- Back-mapping table ----

func TestMapWinnerBack(t *testing.T) {
	cases := []struct {
		judged   Winner
		swapped  bool
		wantBack Winner
	}{
		{WinnerA, false, WinnerA},
		{WinnerB, false, WinnerB},
		{WinnerTie, false, WinnerTie},
		{WinnerA, true, WinnerB}, // judge saw [B,A]; first position=B; judge says A→canonical B
		{WinnerB, true, WinnerA}, // judge saw [B,A]; second position=A; judge says B→canonical A
		{WinnerTie, true, WinnerTie},
	}
	for _, tc := range cases {
		got := mapWinnerBack(tc.judged, tc.swapped)
		if got != tc.wantBack {
			t.Errorf("mapWinnerBack(%q, swapped=%v) = %q, want %q", tc.judged, tc.swapped, got, tc.wantBack)
		}
	}
}

// ---- End-to-end single-pass ----

func TestPairwiseEvaluator_SinglePass_WinnerA(t *testing.T) {
	// Use a fixed seed + fixed artifacts so the swap is deterministic.
	// We don't care which order is chosen; we just verify the canonical
	// winner is back-mapped correctly.
	aArtifact := validArtifact("aa")
	bArtifact := validArtifact("bb")
	p := &PairwiseEvaluator{
		Seed:  42,
		Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerA}},
	}
	req := EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("original", aArtifact, []byte("content-a")),
			makeTarget("replay", bArtifact, []byte("content-b")),
		},
	}
	// Compute the expected canonical winner via the back-mapping logic.
	swapped := p.perPairSwap(req)
	wantCanonical := mapWinnerBack(WinnerA, swapped) // judge always returns A

	eval, err := p.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Kind != ScorePairwise {
		t.Errorf("Score.Kind = %q, want ScorePairwise", eval.Score.Kind)
	}
	if eval.Score.Winner != wantCanonical {
		t.Errorf("Score.Winner = %q, want %q", eval.Score.Winner, wantCanonical)
	}
}

func TestPairwiseEvaluator_SinglePass_FindingsPassthrough(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityInfo, Message: "looks good"},
		{Severity: SeverityWarning, Message: "minor issue", Pointer: "/top"},
	}
	p := &PairwiseEvaluator{
		Seed:  0,
		Judge: &StubJudge{Canned: JudgeResponse{Winner: WinnerTie, Findings: findings}},
	}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eval.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(eval.Findings))
	}
	if eval.Findings[0].Message != "looks good" {
		t.Errorf("finding[0].Message = %q", eval.Findings[0].Message)
	}
}

// ---- Per-pair variation (deterministic proof) ----
//
// We precompute a fixed set of (artifact-pair) inputs with known swap decisions
// for Seed=0 and assert that the cohort produces BOTH swap values — no single
// constant outcome for all pairs.

func TestPairwiseEvaluator_PerPairVariation(t *testing.T) {
	// Build 16 distinct artifact pairs and record their swap decisions for Seed=0.
	p := &PairwiseEvaluator{Seed: 0}
	swapCounts := map[bool]int{}
	for i := byte(0); i < 16; i++ {
		aArt := validArtifact(strings.Repeat(string(rune('a'+i%26)), 2))
		bArt := validArtifact(strings.Repeat(string(rune('A'+i%26)), 2))
		req := EvalRequest{
			Method: "pairwise",
			Targets: []EvalInput{
				makeTarget("a", aArt, []byte{i}),
				makeTarget("b", bArt, []byte{i + 100}),
			},
		}
		sw := p.perPairSwap(req)
		swapCounts[sw]++
	}
	if swapCounts[false] == 0 || swapCounts[true] == 0 {
		t.Errorf("per-pair swap not varied across cohort: swapCounts=%v (want both true and false present)", swapCounts)
	}
}

// ---- Determinism ----

func TestPairwiseEvaluator_Determinism(t *testing.T) {
	aArt := validArtifact("aabbcc")
	bArt := validArtifact("ddeeff")

	req := EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("original", aArt, []byte("hello")),
			makeTarget("replay", bArt, []byte("world")),
		},
	}

	// Same seed + same targets → same swap, reproducibly.
	p1 := &PairwiseEvaluator{Seed: 123}
	p2 := &PairwiseEvaluator{Seed: 123}
	if p1.perPairSwap(req) != p2.perPairSwap(req) {
		t.Error("same Seed + same targets produced different swap decisions")
	}

	// Different seed → may produce different decision (for this pair it should differ).
	p3 := &PairwiseEvaluator{Seed: 456}
	// We just verify the per-pair function is pure (same call twice → same result).
	swap3a := p3.perPairSwap(req)
	swap3b := p3.perPairSwap(req)
	if swap3a != swap3b {
		t.Error("perPairSwap is not deterministic within same evaluator")
	}
}

// ---- Determinism via recording judge ----

func TestPairwiseEvaluator_DeterministicPresentationOrder(t *testing.T) {
	aArt := validArtifact("aaaa11")
	bArt := validArtifact("bbbb22")

	req := EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("original", aArt, []byte("content-a")),
			makeTarget("replay", bArt, []byte("content-b")),
		},
	}

	j1 := &recordingJudge{responses: []JudgeResponse{{Winner: WinnerA}, {Winner: WinnerA}}}
	j2 := &recordingJudge{responses: []JudgeResponse{{Winner: WinnerA}, {Winner: WinnerA}}}

	p1 := &PairwiseEvaluator{Seed: 77, Judge: j1}
	p2 := &PairwiseEvaluator{Seed: 77, Judge: j2}

	_, _ = p1.Evaluate(context.Background(), req)
	_, _ = p2.Evaluate(context.Background(), req)

	if len(j1.recorded) != 1 || len(j2.recorded) != 1 {
		t.Fatalf("expected 1 judge call each, got %d and %d", len(j1.recorded), len(j2.recorded))
	}

	// Both calls must have received the same presentation order.
	r1 := j1.recorded[0]
	r2 := j2.recorded[0]
	if r1.Targets[0].Source.Artifact != r2.Targets[0].Source.Artifact {
		t.Errorf("presentation order differs between identical evaluators: first[0].Artifact=%q vs %q",
			r1.Targets[0].Source.Artifact, r2.Targets[0].Source.Artifact)
	}
}

// ---- Fallback pairKey (empty artifact) ----

func TestPairwiseEvaluator_FallbackKey(t *testing.T) {
	// Targets with empty Source.Artifact but distinct content/role.
	req1 := EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			{Role: "a", Content: []byte("content-alpha")},
			{Role: "b", Content: []byte("content-beta")},
		},
	}
	req2 := EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			{Role: "a", Content: []byte("content-gamma")},
			{Role: "b", Content: []byte("content-delta")},
		},
	}

	p := &PairwiseEvaluator{Seed: 0}
	// Deterministic: repeated calls for same content → same result.
	sw1a := p.perPairSwap(req1)
	sw1b := p.perPairSwap(req1)
	if sw1a != sw1b {
		t.Error("fallback key: perPairSwap not deterministic for same content")
	}

	// Distinct content → can differ (vary across the cohort if enough pairs).
	// We just verify the pairKey fallback doesn't panic and produces a string.
	k0 := pairKey(req1.Targets[0])
	k1 := pairKey(req2.Targets[0])
	if k0 == k1 {
		t.Errorf("fallback keys for distinct content should differ: %q == %q", k0, k1)
	}
}

// ---- Neutral roles ----

func TestPairwiseEvaluator_NeutralRoles(t *testing.T) {
	j := &recordingJudge{responses: []JudgeResponse{{Winner: WinnerA}}}
	p := &PairwiseEvaluator{Seed: 0, Judge: j}

	_, _ = p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("original", validArtifact("aa"), []byte("a")),
			makeTarget("replay", validArtifact("bb"), []byte("b")),
		},
	})

	if len(j.recorded) != 1 {
		t.Fatalf("expected 1 judge call, got %d", len(j.recorded))
	}
	req := j.recorded[0]
	if len(req.Targets) != 2 {
		t.Fatalf("want 2 judge targets, got %d", len(req.Targets))
	}
	for _, target := range req.Targets {
		if target.Role != "left" && target.Role != "right" {
			t.Errorf("expected neutral role left/right, got %q", target.Role)
		}
	}
}

// ---- Judge error propagation ----

func TestPairwiseEvaluator_JudgeErrorPropagates(t *testing.T) {
	sentinel := errors.New("judge failed for test")
	p := &PairwiseEvaluator{Judge: &StubJudge{Err: sentinel}}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

// ---- Malformed response tests ----

func TestPairwiseEvaluator_MalformedResponse(t *testing.T) {
	v := 5.0
	m := 10.0
	conf15 := 1.5
	conf_neg := -0.1

	cases := []struct {
		name string
		resp JudgeResponse
	}{
		{"empty winner", JudgeResponse{Winner: ""}},
		{"unknown winner C", JudgeResponse{Winner: "C"}},
		{"value set", JudgeResponse{Winner: WinnerA, Value: &v}},
		{"max set", JudgeResponse{Winner: WinnerA, Max: &m}},
		{"confidence out of range high", JudgeResponse{Winner: WinnerA, Confidence: &conf15}},
		{"confidence out of range low", JudgeResponse{Winner: WinnerA, Confidence: &conf_neg}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &PairwiseEvaluator{Judge: &StubJudge{Canned: tc.resp}}
			_, err := p.Evaluate(context.Background(), EvalRequest{
				Method: "pairwise",
				Targets: []EvalInput{
					makeTarget("a", validArtifact("aa"), []byte("x")),
					makeTarget("b", validArtifact("bb"), []byte("y")),
				},
			})
			if !errors.Is(err, ErrJudgeOutputInvalid) {
				t.Errorf("want ErrJudgeOutputInvalid, got %v", err)
			}
		})
	}
}

func TestPairwiseEvaluator_MalformedResponse_BadSeverity(t *testing.T) {
	resp := JudgeResponse{
		Winner:   WinnerA,
		Findings: []Finding{{Severity: "critical", Message: "bad"}},
	}
	p := &PairwiseEvaluator{Judge: &StubJudge{Canned: resp}}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for bad severity, got %v", err)
	}
}

// ---- DoubleJudge tests ----

func TestPairwiseEvaluator_DoubleJudge_Agree_A(t *testing.T) {
	conf := 0.8
	// pass1 presents [A,B]; judge returns "A" → back-map(A, swapped=false) = canonical A
	// pass2 presents [B,A]; judge returns "B" → back-map(B, swapped=true) = canonical A
	// both canonical A → agree → winner A
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA, Confidence: &conf, Findings: []Finding{{Severity: SeverityInfo, Message: "pass1 note"}}},
			{Winner: WinnerB, Confidence: &conf, Findings: []Finding{{Severity: SeverityInfo, Message: "pass2 note"}}},
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Winner != WinnerA {
		t.Errorf("Winner = %q, want A", eval.Score.Winner)
	}
	if j.callCount != 2 {
		t.Errorf("expected 2 judge calls, got %d", j.callCount)
	}
}

func TestPairwiseEvaluator_DoubleJudge_Disagree_Tie(t *testing.T) {
	// pass1 presents [A,B] and judges winner=A → canonical A
	// pass2 presents [B,A] and judges winner=A → back-map → canonical B
	// canonical A vs canonical B → tie
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA}, // pass1: [A,B] presented, judge says A → canonical A
			{Winner: WinnerA}, // pass2: [B,A] presented, judge says A → canonical B
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Winner != WinnerTie {
		t.Errorf("Winner = %q, want tie (disagreement should collapse)", eval.Score.Winner)
	}
}

func TestPairwiseEvaluator_DoubleJudge_AVersusTie_CollapsesTie(t *testing.T) {
	// pass1 canonical=A, pass2 canonical=tie → disagree → tie
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA},   // pass1: [A,B]; judge A → canonical A
			{Winner: WinnerTie}, // pass2: [B,A]; judge tie → canonical tie
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Winner != WinnerTie {
		t.Errorf("Winner = %q, want tie", eval.Score.Winner)
	}
}

func TestPairwiseEvaluator_DoubleJudge_ConfidenceMin(t *testing.T) {
	conf1 := 0.9
	conf2 := 0.6
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA, Confidence: &conf1},
			{Winner: WinnerA, Confidence: &conf2},
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Confidence == nil {
		t.Fatal("Confidence is nil, want 0.6")
	}
	if *eval.Score.Confidence != 0.6 {
		t.Errorf("Confidence = %v, want 0.6 (min)", *eval.Score.Confidence)
	}
}

func TestPairwiseEvaluator_DoubleJudge_ConfidenceOneSet(t *testing.T) {
	conf1 := 0.7
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA, Confidence: &conf1},
			{Winner: WinnerA}, // no confidence
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Confidence == nil || *eval.Score.Confidence != 0.7 {
		t.Errorf("Confidence = %v, want 0.7", eval.Score.Confidence)
	}
}

func TestPairwiseEvaluator_DoubleJudge_ConfidenceNeitherSet(t *testing.T) {
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA},
			{Winner: WinnerA},
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Score.Confidence != nil {
		t.Errorf("Confidence = %v, want nil", *eval.Score.Confidence)
	}
}

func TestPairwiseEvaluator_DoubleJudge_FindingsTagged(t *testing.T) {
	j := &recordingJudge{
		responses: []JudgeResponse{
			{
				Winner: WinnerA,
				Findings: []Finding{
					{Severity: SeverityInfo, Message: "first observation"},
				},
			},
			{
				Winner: WinnerA,
				Findings: []Finding{
					{Severity: SeverityWarning, Message: "second observation", Pointer: "/p"},
				},
			},
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	eval, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(eval.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(eval.Findings))
	}
	if !strings.HasPrefix(eval.Findings[0].Message, "[A-first] ") {
		t.Errorf("finding[0].Message = %q, want [A-first] prefix", eval.Findings[0].Message)
	}
	if !strings.HasPrefix(eval.Findings[1].Message, "[B-first] ") {
		t.Errorf("finding[1].Message = %q, want [B-first] prefix", eval.Findings[1].Message)
	}
	if eval.Findings[0].Severity != SeverityInfo {
		t.Errorf("finding[0].Severity = %q", eval.Findings[0].Severity)
	}
	if eval.Findings[1].Pointer != "/p" {
		t.Errorf("finding[1].Pointer = %q, want /p", eval.Findings[1].Pointer)
	}
}

func TestPairwiseEvaluator_DoubleJudge_Call2ErrorPropagates(t *testing.T) {
	sentinel := errors.New("call2 failed")
	j := &recordingJudge{
		responses: []JudgeResponse{{Winner: WinnerA}},
		errors:    []error{nil, sentinel},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error from call2, got %v", err)
	}
}

func TestPairwiseEvaluator_DoubleJudge_CoherenceFailurePropagates(t *testing.T) {
	// pass2 returns an invalid response (empty winner).
	j := &recordingJudge{
		responses: []JudgeResponse{
			{Winner: WinnerA},
			{Winner: ""}, // invalid
		},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("a", validArtifact("aa"), []byte("x")),
			makeTarget("b", validArtifact("bb"), []byte("y")),
		},
	})
	if !errors.Is(err, ErrJudgeOutputInvalid) {
		t.Fatalf("want ErrJudgeOutputInvalid for coherence failure, got %v", err)
	}
}

// ---- DoubleJudge presentation order test ----

func TestPairwiseEvaluator_DoubleJudge_PresentationOrder(t *testing.T) {
	aArt := validArtifact("aaaaaa")
	bArt := validArtifact("bbbbbb")

	j := &recordingJudge{
		responses: []JudgeResponse{{Winner: WinnerA}, {Winner: WinnerA}},
	}
	p := &PairwiseEvaluator{DoubleJudge: true, Judge: j}
	_, err := p.Evaluate(context.Background(), EvalRequest{
		Method: "pairwise",
		Targets: []EvalInput{
			makeTarget("original", aArt, []byte("a-content")),
			makeTarget("replay", bArt, []byte("b-content")),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if j.callCount != 2 {
		t.Fatalf("want 2 calls, got %d", j.callCount)
	}
	// pass1: A first → first target Source.Artifact == aArt
	if j.recorded[0].Targets[0].Source.Artifact != aArt {
		t.Errorf("pass1 first target = %q, want aArt", j.recorded[0].Targets[0].Source.Artifact)
	}
	// pass2: B first → first target Source.Artifact == bArt
	if j.recorded[1].Targets[0].Source.Artifact != bArt {
		t.Errorf("pass2 first target = %q, want bArt", j.recorded[1].Targets[0].Source.Artifact)
	}
}

// ---- EvalResult round-trip through validateScoreCoherence ----

func TestPairwiseEvaluator_ScorePassesValidation(t *testing.T) {
	conf := 0.75
	score := Score{
		Kind:       ScorePairwise,
		Winner:     WinnerA,
		Confidence: &conf,
	}
	if err := validateScoreCoherence("pairwise", score); err != nil {
		t.Errorf("valid pairwise score rejected: %v", err)
	}

	// Also test without confidence (optional).
	score2 := Score{Kind: ScorePairwise, Winner: WinnerTie}
	if err := validateScoreCoherence("pairwise", score2); err != nil {
		t.Errorf("valid pairwise tie score rejected: %v", err)
	}
}

func TestPairwiseEvaluator_EvalResultValidate(t *testing.T) {
	// A full EvalResult round-trip exercising result.go's pairwise arm.
	aArt := strings.Repeat("a", 64)
	bArt := strings.Repeat("b", 64)
	commit := strings.Repeat("c", 40)

	er := EvalResult{
		EvalResultVersion: 1,
		EvalID:            "pairwise-run1-stage1-20260101T000000Z",
		Method:            "pairwise",
		Score:             Score{Kind: ScorePairwise, Winner: WinnerA},
		Targets: []ArtifactSource{
			{RunID: "run1", Stage: "stage1", Commit: commit, Artifact: aArt},
			{RunID: "run2", Stage: "stage1", Commit: commit, Artifact: bArt},
		},
		Producer: runmanifest.Producer{},
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := er.Validate(); err != nil {
		t.Errorf("EvalResult.Validate() failed: %v", err)
	}
}
