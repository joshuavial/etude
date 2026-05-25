package eval

import (
	"context"
	"errors"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// Sentinel errors for Judge implementations.
var (
	// ErrJudgeNotConfigured is returned when the Judge has no command configured.
	ErrJudgeNotConfigured = errors.New("judge not configured")
	// ErrJudgeFailed is returned when the judge process exits with a non-zero status.
	ErrJudgeFailed = errors.New("judge failed")
	// ErrJudgeOutputMissing is returned when the judge does not write the output file.
	ErrJudgeOutputMissing = errors.New("judge output missing")
	// ErrJudgeOutputNotRegular is returned when the output path is not a regular file (e.g. symlink).
	ErrJudgeOutputNotRegular = errors.New("judge output is not a regular file")
	// ErrJudgeOutputInvalid is returned when the judge output cannot be decoded or fails
	// per-method validation (missing value/max for rubric, Max<=0, Value>Max, winner set
	// for rubric, bad severity, unknown field, trailing data, etc.).
	ErrJudgeOutputInvalid = errors.New("judge output invalid")
)

// JudgeInput is one fully-materialised artifact passed to a Judge.
// Content is always fully-materialized bytes; pointer rejection is the
// caller's responsibility (mirrors EvalInput / replay.RunInput).
type JudgeInput struct {
	Role      string
	MediaType string
	Content   []byte         // fully-materialized bytes
	Source    ArtifactSource // provenance back-link
}

// JudgeRequest describes a single judge invocation.
//
// Method drives per-method output-shape validation in both ExecJudge and
// RubricEvaluator:
//
//	"rubric"   — Targets len 1; Rubric non-nil; judge must return Value+Max.
//	"pairwise" — Targets len 2 (A=Targets[0], B=Targets[1]); Rubric nil;
//	             judge must return Winner; Value/Max absent (future bead).
//
// Targets ordering is preserved: ExecJudge materialises them as
// 00-target-<role>, 01-target-<role>, … so pairwise judges see A before B.
// Context inputs are materialised similarly as 00-context-<role>, etc.
type JudgeRequest struct {
	// Method is the eval method ("rubric" | "pairwise"). Drives output validation.
	Method string
	// Targets are the ordered artifacts being judged. len 1 for rubric, len 2 for pairwise.
	Targets []JudgeInput
	// Context are optional unscored inputs (task description, plan, diff, etc.).
	Context []JudgeInput
	// Rubric holds the rubric file bytes the judge scores against. Nil for pairwise.
	Rubric []byte
	// Producer carries model/skill/harness identity for the judge invocation.
	Producer runmanifest.Producer
}

// JudgeResponse is the method-neutral verdict returned by a Judge.
//
// Rubric judges set Value+Max (Winner="" / Confidence=nil).
// Pairwise judges (future bead) set Winner and optionally Confidence
// (Value/Max remain nil).
//
// Pointer fields distinguish "absent" from "zero" under JSON round-trips,
// mirroring eval.Score's per-Kind coherence.
type JudgeResponse struct {
	Value      *float64 // rubric only
	Max        *float64 // rubric only
	Winner     Winner   // pairwise only ('' | A | B | tie)
	Confidence *float64 // pairwise only, optional
	Findings   []Finding
}

// Judge scores one or more artifacts according to a method-tagged JudgeRequest.
// Mirrors replay.Runner.
type Judge interface {
	Judge(ctx context.Context, req JudgeRequest) (JudgeResponse, error)
}

// StubJudge is a test double that satisfies Judge without touching the
// filesystem or executing any external process.
//
//   - Err, if non-nil, is returned immediately with a zero JudgeResponse.
//   - Otherwise Canned is returned verbatim.
//
// Mirrors StubRunner / StubEvaluator.
type StubJudge struct {
	Canned JudgeResponse
	Err    error
}

// compile-time interface satisfaction assertion.
var _ Judge = (*StubJudge)(nil)

// Judge implements Judge for StubJudge.
func (s *StubJudge) Judge(_ context.Context, _ JudgeRequest) (JudgeResponse, error) {
	if s.Err != nil {
		return JudgeResponse{}, s.Err
	}
	return s.Canned, nil
}
