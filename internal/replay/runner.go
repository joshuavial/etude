package replay

import (
	"context"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// Runner executes a single replay stage.
type Runner interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunInput holds a fully-materialized input artifact.
// Content is always fully-materialized bytes; pointer rejection is the
// CALLER's/materializer's responsibility (phase2.4), mirroring
// ResolvedInput.ReadContent's ErrPointerNotMaterialized — the runner
// never inspects storage type.
type RunInput struct {
	Role      string
	MediaType string
	Content   []byte // fully-materialized bytes
}

// RunRequest describes a single stage execution.
//
// WorktreeDir is a PRISTINE detached checkout at the recorded GitSHA
// (read-only for the skill).
//
// ScratchDir is an EXTERNAL scratch directory that MUST NOT be inside
// WorktreeDir. Phase2.4 materializes inputs there (e.g.
// <ScratchDir>/inputs/<NN>-<role>) and reads the skill's output from it.
//
// Inputs carries always fully-materialized content bytes; the runner never
// inspects storage type.
type RunRequest struct {
	// WorktreeDir is a PRISTINE detached checkout == recorded GitSHA
	// (read-only for the skill).
	WorktreeDir string
	// ScratchDir is an EXTERNAL scratch dir (NEVER inside WorktreeDir) where
	// phase2.4 materializes inputs + reads output.
	ScratchDir      string
	Inputs          []RunInput
	OutputRole      string
	OutputMediaType string // from the original stage's Output.MediaType
	Producer        runmanifest.Producer
}

// RunResult is the outcome of a stage execution.
//
// MediaType: if empty, Run() defaults it to req.OutputMediaType.
//
// Producer is the producer identity actually used; MAY differ from req.Producer
// (e.g. when the replay uses a newer skill version/model).
type RunResult struct {
	Output    []byte
	MediaType string               // if empty, Run defaults it to req.OutputMediaType
	Producer  runmanifest.Producer // producer identity actually used; MAY differ from req.Producer (e.g. when the replay uses a newer skill version/model)
}

// stubConcatSeparator is the separator used by StubRunner in CONCAT mode.
// Defined as a named constant so test assertions and behavior cannot drift.
const stubConcatSeparator = "\n"

// StubRunner is a test double that satisfies Runner without touching the
// filesystem or executing any external process.
//
// Modes:
//
//	(a) CANNED  — returns a preconfigured CannedOutput (+ optional CannedMediaType).
//	(b) CONCAT  — concatenates req.Inputs[i].Content in slice order joined by
//	              stubConcatSeparator.
//	(c) Err     — returns the injected Err unchanged.
//
// In all non-error cases StubRunner echoes req.Producer into RunResult.Producer
// (unless ProducerOverride is non-zero). The RunResult.MediaType == ""
// default (-> req.OutputMediaType) is applied inside Run.
type StubRunner struct {
	// CannedOutput, if non-nil, activates CANNED mode.
	CannedOutput []byte
	// CannedMediaType is the MediaType returned in CANNED mode (may be empty
	// to trigger the req.OutputMediaType default).
	CannedMediaType string
	// Concat activates CONCAT mode when CannedOutput is nil and Err is nil.
	Concat bool
	// Err, if non-nil, is returned immediately from Run.
	Err error
	// ProducerOverride, if non-zero, overrides the echoed Producer in RunResult.
	ProducerOverride runmanifest.Producer
}

// compile-time interface satisfaction assertion.
var _ Runner = (*StubRunner)(nil)

// Run implements Runner for StubRunner.
func (s *StubRunner) Run(_ context.Context, req RunRequest) (RunResult, error) {
	if s.Err != nil {
		return RunResult{}, s.Err
	}

	producer := req.Producer
	if s.ProducerOverride != (runmanifest.Producer{}) {
		producer = s.ProducerOverride
	}

	var output []byte
	var mediaType string

	switch {
	case s.CannedOutput != nil:
		output = s.CannedOutput
		mediaType = s.CannedMediaType
	case s.Concat:
		for i, inp := range req.Inputs {
			if i > 0 {
				output = append(output, stubConcatSeparator...)
			}
			output = append(output, inp.Content...)
		}
	}

	if mediaType == "" {
		mediaType = req.OutputMediaType
	}

	return RunResult{
		Output:    output,
		MediaType: mediaType,
		Producer:  producer,
	}, nil
}
