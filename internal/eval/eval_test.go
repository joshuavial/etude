package eval

import (
	"context"
	"errors"
	"testing"

	"github.com/joshuavial/etude/internal/runmanifest"
)

func TestStubEvaluatorReturnsCanned(t *testing.T) {
	v := 7.5
	m := 10.0
	canned := Evaluation{
		Score: Score{
			Kind:  ScoreRubric,
			Value: &v,
			Max:   &m,
		},
		Findings: []Finding{
			{Severity: SeverityInfo, Message: "looks good"},
		},
	}
	stub := &StubEvaluator{Canned: canned}

	got, err := stub.Evaluate(context.Background(), EvalRequest{Method: "rubric"})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if got.Score.Kind != canned.Score.Kind {
		t.Errorf("Score.Kind = %q, want %q", got.Score.Kind, canned.Score.Kind)
	}
	if got.Score.Value == nil || *got.Score.Value != v {
		t.Errorf("Score.Value = %v, want %v", got.Score.Value, v)
	}
	if len(got.Findings) != 1 || got.Findings[0].Message != "looks good" {
		t.Errorf("Findings = %v, want one finding with message 'looks good'", got.Findings)
	}
}

func TestStubEvaluatorReturnsErr(t *testing.T) {
	sentinel := errors.New("judge unavailable")
	stub := &StubEvaluator{Err: sentinel}

	got, err := stub.Evaluate(context.Background(), EvalRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel %v", err, sentinel)
	}
	// Must return zero Evaluation on error.
	if got.Score.Kind != "" || got.Findings != nil {
		t.Errorf("non-zero Evaluation returned on error: %+v", got)
	}
}

func TestStubEvaluatorImplementsEvaluator(t *testing.T) {
	// compile-time check is var _ Evaluator = (*StubEvaluator)(nil)
	// This test ensures the interface is satisfied at runtime too.
	var e Evaluator = &StubEvaluator{}
	_, err := e.Evaluate(context.Background(), EvalRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStubEvaluatorPreservesProducer(t *testing.T) {
	// Producer is part of EvalRequest, not Evaluation, but the stub should not
	// panic or mangle the request.
	stub := &StubEvaluator{Canned: Evaluation{Score: Score{Kind: ScorePairwise, Winner: WinnerA}}}
	req := EvalRequest{
		Method: "pairwise",
		Producer: runmanifest.Producer{
			Model: "claude-opus-4-7",
		},
	}
	_, err := stub.Evaluate(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
