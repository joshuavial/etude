package eval

import (
	"context"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// ScoreKind discriminates the Score shape; MUST equal the EvalResult.Method.
type ScoreKind string

const (
	ScoreRubric    ScoreKind = "rubric"
	ScorePairwise  ScoreKind = "pairwise"
	ScoreAssertion ScoreKind = "assertion"
)

// Winner is the pairwise outcome enum. Empty for non-pairwise.
// Orientation is pinned: Targets[0]==A, Targets[1]==B; by convention
// A=original, B=replay, so bench computes win-rate deterministically.
type Winner string

const (
	WinnerNone Winner = "" // non-pairwise
	WinnerA    Winner = "A"
	WinnerB    Winner = "B"
	WinnerTie  Winner = "tie"
)

// Severity is the finding-severity enum.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// ArtifactSource is the durable back-link to a stored artifact. Commit is
// mandatory: pinning the immutable source commit keeps the link resolvable
// after future appends to the source run (inherits the replay_of lesson).
type ArtifactSource struct {
	RunID    string // IsValidRunID
	Stage    string // validateIdentifier
	Commit   string // isHexOID (40/64 lowercase hex)
	Artifact string // validSHA256 (64 lowercase hex)
}

// EvalInput is a fully-materialized artifact plus its provenance.
// Content is always fully-materialized bytes; pointer rejection is the
// caller's responsibility (mirrors replay.RunInput).
type EvalInput struct {
	Role      string
	MediaType string
	Content   []byte         // fully-materialized bytes
	Source    ArtifactSource // provenance back-link (persisted into targets[]/context[])
}

// RubricRef pins which rubric version was scored against.
// A rubric edit must not silently break comparability.
type RubricRef struct {
	Path    string // workflow-relative rubric path
	Version string // pinned rubric version
}

// AssertionCheck names a built-in deterministic check plus its args. Kind is
// resolved via a registry defined in the assertion-eval bead.
type AssertionCheck struct {
	Kind string
	Args map[string]string
}

// AssertionSpec is the concrete, persisted assertion config.
// Passed is not auditable without recording which checks ran.
type AssertionSpec struct {
	Checks []AssertionCheck
}

// Score uses pointers for numeric/bool fields so "absent" is distinct from
// "zero" under DisallowUnknownFields round-trips.
//
// Per-Kind coherence (enforced by Validate):
//
//	pairwise:  Kind=pairwise; Winner in {A,B,tie}; Value=Max=Passed=nil.
//	rubric:    Kind=rubric; Value in [0,Max], Max>0 (non-nil); Winner=""; Passed=nil.
//	assertion: Kind=assertion; Passed set (non-nil); Value=Max=nil; Winner="".
//
// bench win-rate over a pairwise cohort:
//
//	win_rate = (count(Winner==A) + 0.5*count(Winner==tie)) / total
//	where total = number of pairwise evals in the cohort.
//	When total==0 the result is undefined; the caller must guard.
type Score struct {
	Kind       ScoreKind
	Value      *float64 // rubric only
	Max        *float64 // rubric only
	Winner     Winner   // pairwise only
	Passed     *bool    // assertion only
	Confidence *float64 // optional; pairwise only (omitempty)
}

// Finding is one structured observation from an evaluator.
// Message is required; Pointer is optional (free-form locator into the artifact).
type Finding struct {
	Severity Severity
	Message  string
	Pointer  string
}

// Evaluation is the lightweight evaluator output. It carries no persistence
// identity (no eval_id, targets, context, or created). The caller builds the
// durable EvalResult from Evaluation plus request provenance plus a minted
// eval_id. Mirrors replay.RunResult / Manifest separation.
type Evaluation struct {
	Score    Score
	Findings []Finding
}

// EvalRequest describes one evaluation.
//
//   - Targets are the artifact(s) being scored: len 1 for rubric/assertion,
//     len 2 (A, B) for pairwise.
//   - Context are unscored inputs the evaluator may read (task/plan/diff); optional.
//
// Method is explicit — never inferred from which config pointer is set.
type EvalRequest struct {
	Method    string
	Targets   []EvalInput
	Context   []EvalInput
	Rubric    *RubricRef     // required iff Method=="rubric"; nil otherwise
	Assertion *AssertionSpec // required iff Method=="assertion"; nil otherwise
	Producer  runmanifest.Producer
}

// Evaluator scores Targets (optionally informed by Context) and returns a
// lightweight Evaluation. It does NOT persist: the caller builds the durable
// EvalResult from the Evaluation plus the request provenance plus a minted
// eval_id. Mirrors replay.Runner.Run / RunResult.
type Evaluator interface {
	Evaluate(ctx context.Context, req EvalRequest) (Evaluation, error)
}

// StubEvaluator is a test double satisfying Evaluator.
//   - Err, if non-nil, is returned immediately with a zero Evaluation.
//   - Otherwise Canned is returned verbatim.
//
// Mirrors StubRunner (internal/replay/runner.go).
type StubEvaluator struct {
	Canned Evaluation
	Err    error
}

// compile-time interface satisfaction assertion.
var _ Evaluator = (*StubEvaluator)(nil)

// Evaluate implements Evaluator for StubEvaluator.
func (s *StubEvaluator) Evaluate(_ context.Context, _ EvalRequest) (Evaluation, error) {
	if s.Err != nil {
		return Evaluation{}, s.Err
	}
	return s.Canned, nil
}
