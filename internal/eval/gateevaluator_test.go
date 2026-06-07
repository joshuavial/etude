package eval

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestGateEvaluatorGoResponse(t *testing.T) {
	passed := true
	ev := &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}}

	got, err := ev.Evaluate(context.Background(), gateEvalRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Score.Kind != ScoreGate {
		t.Fatalf("Score.Kind = %q, want %q", got.Score.Kind, ScoreGate)
	}
	if got.Score.Passed == nil || *got.Score.Passed != true {
		t.Fatalf("Score.Passed = %v, want true", got.Score.Passed)
	}
	if got.Score.Value != nil || got.Score.Max != nil || got.Score.Winner != WinnerNone || got.Score.Confidence != nil {
		t.Fatalf("gate score has non-gate fields set: %+v", got.Score)
	}
}

func TestGateEvaluatorBlockResponsePreservesFindings(t *testing.T) {
	passed := false
	ev := &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{
		Passed: &passed,
		Findings: []Finding{{
			Severity: SeverityError,
			Message:  "required change",
			Pointer:  "plan",
		}},
	}}}

	got, err := ev.Evaluate(context.Background(), gateEvalRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if got.Score.Passed == nil || *got.Score.Passed != false {
		t.Fatalf("Score.Passed = %v, want false", got.Score.Passed)
	}
	if len(got.Findings) != 1 || got.Findings[0].Message != "required change" {
		t.Fatalf("Findings = %+v, want required change", got.Findings)
	}
}

func TestGateEvaluatorValidation(t *testing.T) {
	passed := true
	baseReq := gateEvalRequest()

	cases := []struct {
		name string
		req  EvalRequest
		ev   *GateEvaluator
		want error
	}{
		{
			name: "wrong method",
			req:  withMethod(baseReq, "rubric"),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: nil,
		},
		{
			name: "rubric config",
			req:  withRubric(baseReq),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "assertion config",
			req:  withAssertion(baseReq),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "zero targets",
			req:  withTargets(baseReq, nil),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "two targets",
			req:  withTargets(baseReq, append(baseReq.Targets, baseReq.Targets[0])),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "nil judge",
			req:  baseReq,
			ev:   &GateEvaluator{},
			want: ErrJudgeNotConfigured,
		},
		{
			name: "missing gate prompt",
			req:  withContext(baseReq, []EvalInput{{Role: "extra", Content: []byte("x")}}),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "empty gate prompt",
			req:  withContext(baseReq, []EvalInput{{Role: GatePromptRole, Content: []byte("   ")}}),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "multiple gate prompts",
			req: withContext(baseReq, []EvalInput{
				{Role: GatePromptRole, Content: []byte("prompt A")},
				{Role: GatePromptRole, Content: []byte("prompt B")},
			}),
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{Passed: &passed}}},
			want: ErrGateConfig,
		},
		{
			name: "missing passed response",
			req:  baseReq,
			ev:   &GateEvaluator{Judge: &StubJudge{Canned: JudgeResponse{}}},
			want: ErrJudgeOutputInvalid,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.ev.Evaluate(context.Background(), tc.req)
			if tc.want == nil {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestGateEvaluatorForwardsFullContext(t *testing.T) {
	passed := true
	judge := &recordingGateJudge{resp: JudgeResponse{Passed: &passed}}
	ev := &GateEvaluator{Judge: judge}
	req := gateEvalRequest()
	req.Context = append(req.Context, EvalInput{Role: "extra", Content: []byte("extra context")})

	if _, err := ev.Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if judge.req.Method != "gate" {
		t.Fatalf("judge method = %q, want gate", judge.req.Method)
	}
	if len(judge.req.Targets) != 1 {
		t.Fatalf("len(judge.req.Targets) = %d, want 1", len(judge.req.Targets))
	}
	if judge.req.Targets[0].Role != "artifact" || string(judge.req.Targets[0].Content) != "plan text" {
		t.Fatalf("target = %+v, want artifact plan text", judge.req.Targets[0])
	}
	if len(judge.req.Context) != 2 {
		t.Fatalf("len(judge.req.Context) = %d, want 2", len(judge.req.Context))
	}
	if judge.req.Context[0].Role != GatePromptRole || string(judge.req.Context[0].Content) != "gate prompt" {
		t.Fatalf("first context = %+v, want gate prompt", judge.req.Context[0])
	}
	if judge.req.Context[1].Role != "extra" || string(judge.req.Context[1].Content) != "extra context" {
		t.Fatalf("second context = %+v, want extra context", judge.req.Context[1])
	}
	if !reflect.DeepEqual(judge.req.Producer, req.Producer) {
		t.Fatalf("producer = %+v, want %+v", judge.req.Producer, req.Producer)
	}
}

func TestGateEvaluatorRejectsIncoherentResponses(t *testing.T) {
	passed := true
	value := 1.0
	max := 2.0
	conf := 0.5

	cases := []struct {
		name string
		resp JudgeResponse
	}{
		{name: "value", resp: JudgeResponse{Passed: &passed, Value: &value}},
		{name: "max", resp: JudgeResponse{Passed: &passed, Max: &max}},
		{name: "winner", resp: JudgeResponse{Passed: &passed, Winner: WinnerA}},
		{name: "confidence", resp: JudgeResponse{Passed: &passed, Confidence: &conf}},
		{name: "bad finding", resp: JudgeResponse{Passed: &passed, Findings: []Finding{{Severity: "bad", Message: "x"}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := &GateEvaluator{Judge: &StubJudge{Canned: tc.resp}}
			_, err := ev.Evaluate(context.Background(), gateEvalRequest())
			if !errors.Is(err, ErrJudgeOutputInvalid) {
				t.Fatalf("want ErrJudgeOutputInvalid, got %v", err)
			}
		})
	}
}

type recordingGateJudge struct {
	req  JudgeRequest
	resp JudgeResponse
	err  error
}

func (r *recordingGateJudge) Judge(_ context.Context, req JudgeRequest) (JudgeResponse, error) {
	r.req = req
	if r.err != nil {
		return JudgeResponse{}, r.err
	}
	return r.resp, nil
}

func gateEvalRequest() EvalRequest {
	return EvalRequest{
		Method: "gate",
		Targets: []EvalInput{{
			Role:      "artifact",
			MediaType: "text/markdown",
			Content:   []byte("plan text"),
		}},
		Context: []EvalInput{{
			Role:      GatePromptRole,
			MediaType: "text/plain",
			Content:   []byte("gate prompt"),
		}},
		Producer: testProducer(),
	}
}

func withMethod(req EvalRequest, method string) EvalRequest {
	req.Method = method
	return req
}

func withRubric(req EvalRequest) EvalRequest {
	req.Rubric = &RubricRef{Path: "rubric.md", Version: "abc"}
	return req
}

func withAssertion(req EvalRequest) EvalRequest {
	req.Assertion = &AssertionSpec{Checks: []AssertionCheck{{Kind: "check"}}}
	return req
}

func withTargets(req EvalRequest, targets []EvalInput) EvalRequest {
	req.Targets = targets
	return req
}

func withContext(req EvalRequest, context []EvalInput) EvalRequest {
	req.Context = context
	return req
}
