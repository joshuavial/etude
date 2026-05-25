package eval

import (
	"context"
	"errors"
	"testing"
)

func TestStubJudge_ReturnsCanned(t *testing.T) {
	v := 8.0
	m := 10.0
	canned := JudgeResponse{
		Value: &v,
		Max:   &m,
		Findings: []Finding{
			{Severity: SeverityInfo, Message: "well structured"},
		},
	}
	stub := &StubJudge{Canned: canned}

	got, err := stub.Judge(context.Background(), JudgeRequest{Method: "rubric"})
	if err != nil {
		t.Fatalf("Judge returned error: %v", err)
	}
	if got.Value == nil || *got.Value != v {
		t.Errorf("Value = %v, want %v", got.Value, v)
	}
	if got.Max == nil || *got.Max != m {
		t.Errorf("Max = %v, want %v", got.Max, m)
	}
	if len(got.Findings) != 1 || got.Findings[0].Message != "well structured" {
		t.Errorf("Findings = %v, want one finding", got.Findings)
	}
}

func TestStubJudge_ReturnsErr(t *testing.T) {
	sentinel := errors.New("model unavailable")
	stub := &StubJudge{Err: sentinel}

	got, err := stub.Judge(context.Background(), JudgeRequest{})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	// Must return zero JudgeResponse on error.
	if got.Value != nil || got.Max != nil || got.Winner != "" || got.Confidence != nil || got.Findings != nil {
		t.Errorf("want zero JudgeResponse on error, got %+v", got)
	}
}

func TestStubJudge_InterfaceAssertion(t *testing.T) {
	// Verifies the compile-time assertion holds at runtime too.
	var _ Judge = (*StubJudge)(nil)
}
