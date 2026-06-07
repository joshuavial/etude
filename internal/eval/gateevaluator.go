package eval

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// ErrGateConfig is returned for invalid gate evaluator configuration.
var ErrGateConfig = errors.New("invalid gate config")

// GatePromptRole is the context role convention for the gate prompt variant.
// GateEvaluator requires exactly one non-empty context input with this role and
// forwards the full context slice to the Judge.
const GatePromptRole = "gate-prompt"

// GateEvaluator implements Evaluator by invoking a Judge as a phase-gate
// reviewer over one recorded artifact.
//
// Method "gate" returns Score.Passed=true for GO and false for BLOCK. It is
// judge-backed, unlike assertion checks, and uses Context to carry the
// gate-prompt variant plus any additional unscored review inputs.
type GateEvaluator struct {
	Judge Judge
}

var _ Evaluator = (*GateEvaluator)(nil)

// Evaluate scores one target artifact with a gate prompt carried in Context.
func (g *GateEvaluator) Evaluate(ctx context.Context, req EvalRequest) (Evaluation, error) {
	if req.Method != "gate" {
		return Evaluation{}, fmt.Errorf("GateEvaluator requires method \"gate\", got %q", req.Method)
	}
	if req.Rubric != nil {
		return Evaluation{}, fmt.Errorf("%w: gate method must not have rubric config", ErrGateConfig)
	}
	if req.Assertion != nil {
		return Evaluation{}, fmt.Errorf("%w: gate method must not have assertion config", ErrGateConfig)
	}
	if len(req.Targets) != 1 {
		return Evaluation{}, fmt.Errorf("%w: gate requires exactly 1 target, got %d", ErrGateConfig, len(req.Targets))
	}
	if g.Judge == nil {
		return Evaluation{}, ErrJudgeNotConfigured
	}
	if err := validateGatePromptContext(req.Context); err != nil {
		return Evaluation{}, err
	}

	targets := make([]JudgeInput, 0, len(req.Targets))
	for _, t := range req.Targets {
		targets = append(targets, JudgeInput{
			Role:      t.Role,
			MediaType: t.MediaType,
			Content:   t.Content,
			Source:    t.Source,
		})
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

	resp, err := g.Judge.Judge(ctx, JudgeRequest{
		Method:   "gate",
		Targets:  targets,
		Context:  context_,
		Producer: req.Producer,
	})
	if err != nil {
		return Evaluation{}, err
	}
	if err := checkGateResponseCoherence(resp); err != nil {
		return Evaluation{}, err
	}

	return Evaluation{
		Score: Score{
			Kind:   ScoreGate,
			Passed: resp.Passed,
		},
		Findings: resp.Findings,
	}, nil
}

func validateGatePromptContext(context_ []EvalInput) error {
	count := 0
	for _, c := range context_ {
		if c.Role != GatePromptRole {
			continue
		}
		count++
		if len(c.Content) == 0 || strings.TrimSpace(string(c.Content)) == "" {
			return fmt.Errorf("%w: %s context must be non-empty", ErrGateConfig, GatePromptRole)
		}
	}
	if count != 1 {
		return fmt.Errorf("%w: gate requires exactly one %s context, got %d", ErrGateConfig, GatePromptRole, count)
	}
	return nil
}

func checkGateResponseCoherence(resp JudgeResponse) error {
	if resp.Passed == nil {
		return fmt.Errorf("%w: gate response missing passed", ErrJudgeOutputInvalid)
	}
	if resp.Value != nil {
		return fmt.Errorf("%w: gate response must not set value", ErrJudgeOutputInvalid)
	}
	if resp.Max != nil {
		return fmt.Errorf("%w: gate response must not set max", ErrJudgeOutputInvalid)
	}
	if resp.Winner != WinnerNone {
		return fmt.Errorf("%w: gate response must not set winner", ErrJudgeOutputInvalid)
	}
	if resp.Confidence != nil {
		return fmt.Errorf("%w: gate response must not set confidence", ErrJudgeOutputInvalid)
	}
	for i, f := range resp.Findings {
		if err := validateFinding(i, f); err != nil {
			return fmt.Errorf("%w: %v", ErrJudgeOutputInvalid, err)
		}
	}
	return nil
}
