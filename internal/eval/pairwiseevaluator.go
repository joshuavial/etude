package eval

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
)

// PairwiseEvaluator implements Evaluator using a Judge to compare two artifacts
// and produce a canonical winner verdict.
//
// Canonical orientation is PINNED: Targets[0]=original=A, Targets[1]=replay=B.
// Position bias is mitigated by randomising the order targets are presented to
// the Judge (per-pair, seeded by Seed + stable content-addressed identities),
// then back-mapping the position-relative winner to canonical A/B.
//
// DoubleJudge=false (default): one judge call with randomised presentation order.
// DoubleJudge=true: two deterministic judge calls (A-first, then B-first); the
// canonical winners are compared and collapsed to a tie on disagreement.
type PairwiseEvaluator struct {
	Judge       Judge
	Seed        int64 // folded into per-pair RNG; 0 is valid and deterministic
	DoubleJudge bool  // false=single randomised pass; true=both-orders mode
}

// compile-time interface satisfaction assertion.
var _ Evaluator = (*PairwiseEvaluator)(nil)

// Evaluate scores two targets against each other.
//
// Head-validation order (mirrors RubricEvaluator):
//  1. req.Method must be "pairwise"
//  2. req.Rubric must be nil (pairwise forbids rubric config)
//  3. req.Assertion must be nil
//  4. len(req.Targets) must be exactly 2
//  5. p.Judge must be non-nil
func (p *PairwiseEvaluator) Evaluate(ctx context.Context, req EvalRequest) (Evaluation, error) {
	if req.Method != "pairwise" {
		return Evaluation{}, fmt.Errorf("PairwiseEvaluator requires method \"pairwise\", got %q", req.Method)
	}
	if req.Rubric != nil {
		return Evaluation{}, fmt.Errorf("PairwiseEvaluator requires nil Rubric (pairwise forbids rubric config)")
	}
	if req.Assertion != nil {
		return Evaluation{}, fmt.Errorf("PairwiseEvaluator requires nil Assertion")
	}
	if len(req.Targets) != 2 {
		return Evaluation{}, fmt.Errorf("PairwiseEvaluator requires exactly 2 targets, got %d", len(req.Targets))
	}
	if p.Judge == nil {
		return Evaluation{}, ErrJudgeNotConfigured
	}

	if p.DoubleJudge {
		return p.evaluateDouble(ctx, req)
	}
	return p.evaluateSingle(ctx, req)
}

// evaluateSingle runs one judge call with a per-pair-randomised presentation order.
func (p *PairwiseEvaluator) evaluateSingle(ctx context.Context, req EvalRequest) (Evaluation, error) {
	swapped := p.perPairSwap(req)
	jr := p.buildJudgeRequest(req, swapped)

	resp, err := p.Judge.Judge(ctx, jr)
	if err != nil {
		return Evaluation{}, err
	}

	if err := checkResponseCoherence(resp); err != nil {
		return Evaluation{}, err
	}

	winner := mapWinnerBack(resp.Winner, swapped)
	return Evaluation{
		Score: Score{
			Kind:       ScorePairwise,
			Winner:     winner,
			Confidence: resp.Confidence,
		},
		Findings: resp.Findings,
	}, nil
}

// evaluateDouble runs two deterministic judge calls (A-first, then B-first),
// validates both, and combines results.
func (p *PairwiseEvaluator) evaluateDouble(ctx context.Context, req EvalRequest) (Evaluation, error) {
	// Pass 1: present [A, B] (not swapped).
	jr1 := p.buildJudgeRequest(req, false)
	resp1, err := p.Judge.Judge(ctx, jr1)
	if err != nil {
		return Evaluation{}, err
	}
	if err := checkResponseCoherence(resp1); err != nil {
		return Evaluation{}, err
	}

	// Pass 2: present [B, A] (swapped).
	jr2 := p.buildJudgeRequest(req, true)
	resp2, err := p.Judge.Judge(ctx, jr2)
	if err != nil {
		return Evaluation{}, err
	}
	if err := checkResponseCoherence(resp2); err != nil {
		return Evaluation{}, err
	}

	// Back-map each pass to canonical A/B orientation.
	canon1 := mapWinnerBack(resp1.Winner, false)
	canon2 := mapWinnerBack(resp2.Winner, true)

	winner, confidence, findings := combineDouble(canon1, canon2, resp1, resp2)

	return Evaluation{
		Score: Score{
			Kind:       ScorePairwise,
			Winner:     winner,
			Confidence: confidence,
		},
		Findings: findings,
	}, nil
}

// perPairSwap derives a deterministic swap decision for the given pair.
//
// Byte layout (two independent FNV-64a streams):
//
//	hi stream: [8-byte big-endian Seed] [0x00] [k0 bytes] [0x1f] [k1 bytes]
//	lo stream: [8-byte big-endian Seed] [0x01] [k1 bytes] [0x1f] [k0 bytes]
//
// The two targets appear in OPPOSITE order across streams so hi≠lo even when
// k0==k1. The 128-bit (hi,lo) pair seeds a PCG generator; swap iff IntN(2)==1.
// Changing Seed reshuffles every pair; identical (Seed,k0,k1) always produces
// the same decision.
func (p *PairwiseEvaluator) perPairSwap(req EvalRequest) bool {
	k0 := pairKey(req.Targets[0])
	k1 := pairKey(req.Targets[1])

	seed8 := make([]byte, 8)
	binary.BigEndian.PutUint64(seed8, uint64(p.Seed))

	hi := fnv.New64a()
	hi.Write(seed8)
	hi.Write([]byte{0x00})
	hi.Write([]byte(k0))
	hi.Write([]byte{0x1f})
	hi.Write([]byte(k1))

	lo := fnv.New64a()
	lo.Write(seed8)
	lo.Write([]byte{0x01})
	lo.Write([]byte(k1))
	lo.Write([]byte{0x1f})
	lo.Write([]byte(k0))

	rng := rand.New(rand.NewPCG(hi.Sum64(), lo.Sum64()))
	return rng.IntN(2) == 1
}

// pairKey returns a stable per-target identity string for use in perPairSwap.
// When Source.Artifact is non-empty (a 64-hex sha256), it is used directly.
// Otherwise falls back to Role + ":" + sha256hex(Content) so stubs and tests
// without artifact IDs still get deterministic, per-pair-varying behaviour.
func pairKey(t EvalInput) string {
	if t.Source.Artifact != "" {
		return t.Source.Artifact
	}
	return t.Role + ":" + sha256hex(t.Content)
}

// mapWinnerBack converts a judge's position-relative winner to canonical A/B.
//
// When swapped=false the judge received [A, B] so positions match canonical:
//
//	judgeA→A, judgeB→B, tie→tie.
//
// When swapped=true the judge received [B, A] so positions are reversed:
//
//	judgeA (=first=canonical B)→B, judgeB (=second=canonical A)→A, tie→tie.
func mapWinnerBack(judged Winner, swapped bool) Winner {
	if !swapped {
		return judged
	}
	switch judged {
	case WinnerA:
		return WinnerB
	case WinnerB:
		return WinnerA
	default:
		return judged // tie stays tie
	}
}

// buildJudgeRequest constructs a JudgeRequest from an EvalRequest.
// Targets are presented with neutral roles "left"/"right" (not "original"/"replay")
// so the judge cannot infer canonical provenance from role text.
// If swapped=true, canonical B is presented first (left); otherwise canonical A
// is presented first.
func (p *PairwiseEvaluator) buildJudgeRequest(req EvalRequest, swapped bool) JudgeRequest {
	a := req.Targets[0] // canonical A
	b := req.Targets[1] // canonical B

	var first, second EvalInput
	if swapped {
		first, second = b, a
	} else {
		first, second = a, b
	}

	targets := []JudgeInput{
		{Role: "left", MediaType: first.MediaType, Content: first.Content, Source: first.Source},
		{Role: "right", MediaType: second.MediaType, Content: second.Content, Source: second.Source},
	}

	context_ := make([]JudgeInput, 0, len(req.Context))
	for _, c := range req.Context {
		context_ = append(context_, JudgeInput{
			Role:      c.Role,
			MediaType: c.MediaType,
			Content:   c.Content,
			Source:    c.Source,
		})
	}

	return JudgeRequest{
		Method:   "pairwise",
		Targets:  targets,
		Context:  context_,
		Rubric:   nil,
		Producer: req.Producer,
	}
}

// checkResponseCoherence validates pairwise-method coherence on a JudgeResponse.
// This is belt-and-suspenders: ExecJudge already calls validateJudgeOutput, but
// StubJudge bypasses that path so we re-check here.
func checkResponseCoherence(resp JudgeResponse) error {
	if resp.Winner != WinnerA && resp.Winner != WinnerB && resp.Winner != WinnerTie {
		return fmt.Errorf("%w: pairwise response winner must be A, B, or tie; got %q", ErrJudgeOutputInvalid, resp.Winner)
	}
	if resp.Value != nil {
		return fmt.Errorf("%w: pairwise response must not set value", ErrJudgeOutputInvalid)
	}
	if resp.Max != nil {
		return fmt.Errorf("%w: pairwise response must not set max", ErrJudgeOutputInvalid)
	}
	if resp.Passed != nil {
		return fmt.Errorf("%w: pairwise response must not set passed", ErrJudgeOutputInvalid)
	}
	if resp.Confidence != nil && (*resp.Confidence < 0 || *resp.Confidence > 1) {
		return fmt.Errorf("%w: pairwise response confidence must be in [0, 1], got %v", ErrJudgeOutputInvalid, *resp.Confidence)
	}
	for i, f := range resp.Findings {
		if err := validateFinding(i, f); err != nil {
			return fmt.Errorf("%w: %v", ErrJudgeOutputInvalid, err)
		}
	}
	return nil
}

// combineDouble merges two canonically-oriented judge verdicts into a single
// result. It takes the already-back-mapped canonical winners plus the raw
// responses for confidence and findings.
//
// Rules:
//   - winner: canonical winners agree → that winner; disagree → WinnerTie.
//   - confidence: both set → pointer to MIN (conservative); one set → that one; neither → nil.
//   - findings: pass1 findings prefixed "[A-first] " followed by pass2 findings
//     prefixed "[B-first] " (provenance-tagged for audit).
func combineDouble(canon1, canon2 Winner, resp1, resp2 JudgeResponse) (Winner, *float64, []Finding) {
	// Winner: agree or collapse to tie.
	winner := canon1
	if canon1 != canon2 {
		winner = WinnerTie
	}

	// Confidence: conservative minimum.
	var confidence *float64
	switch {
	case resp1.Confidence != nil && resp2.Confidence != nil:
		min := *resp1.Confidence
		if *resp2.Confidence < min {
			min = *resp2.Confidence
		}
		confidence = &min
	case resp1.Confidence != nil:
		v := *resp1.Confidence
		confidence = &v
	case resp2.Confidence != nil:
		v := *resp2.Confidence
		confidence = &v
	}

	// Findings: tag and concatenate.
	findings := make([]Finding, 0, len(resp1.Findings)+len(resp2.Findings))
	for _, f := range resp1.Findings {
		findings = append(findings, Finding{
			Severity: f.Severity,
			Message:  "[A-first] " + f.Message,
			Pointer:  f.Pointer,
		})
	}
	for _, f := range resp2.Findings {
		findings = append(findings, Finding{
			Severity: f.Severity,
			Message:  "[B-first] " + f.Message,
			Pointer:  f.Pointer,
		})
	}
	if len(findings) == 0 {
		findings = nil
	}

	return winner, confidence, findings
}
