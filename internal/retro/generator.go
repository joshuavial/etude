package retro

import (
	"context"

	"github.com/joshuavial/etude/internal/runmanifest"
)

// Generator produces a retro body given a set of subject artifacts.
type Generator interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResult, error)
}

// SubjectArtifact carries the resolved artifacts for a single subject run.
// Output holds the stage output artifact bytes; Inputs holds the stage input
// artifacts. All bytes are fully-materialized (pointer artifacts are rejected
// before GenerateRequest is built).
type SubjectArtifact struct {
	// RunID is the subject run's id.
	RunID string
	// OutputRole is the role of the stage output artifact.
	OutputRole string
	// OutputContent is the fully-materialized bytes of the stage output.
	OutputContent []byte
	// Inputs holds the stage input artifacts, fully materialized.
	Inputs []SubjectInput
}

// SubjectInput is a single input artifact from a subject run's stage.
type SubjectInput struct {
	Role    string
	Content []byte
}

// GenerateRequest describes a retro generation invocation.
type GenerateRequest struct {
	// Subjects holds one entry per --subject-run, ordered as provided.
	Subjects []SubjectArtifact
	// Scope is the retro scope (e.g. "cohort", "run").
	Scope string
	// Trigger is the trigger that prompted the retro (e.g. "manual").
	Trigger string
	// Producer carries the producer identity for provenance.
	Producer runmanifest.Producer
}

// GenerateResult is the outcome of a retro generation.
type GenerateResult struct {
	// Body is the retro markdown body.
	Body      []byte
	MediaType string
	// Producer is the producer identity actually used; MAY differ from req.Producer.
	Producer runmanifest.Producer
}

// stubConcatSeparator is the separator used by StubGenerator in CONCAT mode.
const stubConcatSeparator = "\n"

// StubGenerator is a test double that satisfies Generator without touching the
// filesystem or executing any external process.
//
// Modes:
//
//	(a) CANNED  — returns CannedBody (+ optional CannedMediaType).
//	(b) CONCAT  — concatenates all subject OutputContent bytes joined by stubConcatSeparator.
//	(c) Err     — returns the injected Err unchanged.
//
// In all non-error cases StubGenerator echoes req.Producer into GenerateResult.Producer
// (unless ProducerOverride is non-zero).
type StubGenerator struct {
	// CannedBody, if non-nil, activates CANNED mode.
	CannedBody []byte
	// CannedMediaType is the MediaType returned in CANNED mode (may be empty).
	CannedMediaType string
	// Concat activates CONCAT mode when CannedBody is nil and Err is nil.
	Concat bool
	// Err, if non-nil, is returned immediately from Generate.
	Err error
	// ProducerOverride, if non-zero, overrides the echoed Producer.
	ProducerOverride runmanifest.Producer
}

// compile-time interface satisfaction assertion.
var _ Generator = (*StubGenerator)(nil)

// Generate implements Generator for StubGenerator.
func (s *StubGenerator) Generate(_ context.Context, req GenerateRequest) (GenerateResult, error) {
	if s.Err != nil {
		return GenerateResult{}, s.Err
	}

	producer := req.Producer
	if s.ProducerOverride != (runmanifest.Producer{}) {
		producer = s.ProducerOverride
	}

	var body []byte
	var mediaType string

	switch {
	case s.CannedBody != nil:
		body = s.CannedBody
		mediaType = s.CannedMediaType
	case s.Concat:
		for i, subj := range req.Subjects {
			if i > 0 {
				body = append(body, stubConcatSeparator...)
			}
			body = append(body, subj.OutputContent...)
		}
	}

	return GenerateResult{
		Body:      body,
		MediaType: mediaType,
		Producer:  producer,
	}, nil
}
