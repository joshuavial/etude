// Package bench provides cohort selection and corpus sourcing for the etude
// bench command.
//
// Decoupling contract: this file defines the CorpusSource interface and its
// supporting value types as a pure contract. No I/O or storage logic lives
// here. The built-in default implementation (RunRefsSource) lives in
// corpus_runrefs.go and yields ONE final-plan-TEXT fixture per qualifying run,
// with multi-round gate-progression data carried solely as LabelHint values.
// The limitation — only the last-captured plan text per run, not per-round plan
// text — is intentional and stated explicitly: per-round plan texts require the
// separate beads/Dolt adapter (a future bead), which core must not import.
//
// Adapter selection (mirror of replay.Runner / resolveRunner) lands in aul.4;
// until then, intra-core callers construct or receive RunRefsSource directly.
package bench

import "context"

// CorpusSource yields Fixtures for the gate bench pipeline.
// Implementations may source fixtures from run refs (RunRefsSource), a
// beads/Dolt history store, or any other backend — core only defines the seam.
type CorpusSource interface {
	Fixtures(ctx context.Context, sel CohortSelector) ([]Fixture, error)
}

// CohortSelector carries the selection axes for corpus retrieval.
// It is a small tracker-neutral value type so that non-run-ref adapters can
// implement their own selection logic without inheriting run-ref semantics.
type CohortSelector struct {
	// Stage is the workflow stage name to filter on (e.g. "plan").
	Stage string
	// Last is the maximum number of fixtures to return (must be positive).
	Last int
}

// Fixture is one corpus entry: an artifact plus its provenance and an optional
// label hint derived from gate history.
type Fixture struct {
	// Artifact holds the raw artifact bytes (e.g. plan text).
	Artifact []byte
	// MediaType is the MIME type of Artifact (e.g. "text/markdown").
	MediaType string
	// Provenance records where the fixture came from.
	Provenance Provenance
	// Label is an optional weak hint from the source's observable gate history.
	// It is a HINT only — scoring logic is aul.3. May be zero-valued.
	Label LabelHint
}

// Provenance records the origin of a Fixture.
type Provenance struct {
	// RunID is the source run identifier.
	RunID string
	// Phase is the workflow phase name (e.g. "plan").
	Phase string
	// Round is the gate round from which the hint was derived.
	// For RunRefsSource this is the round number of the final (highest) gate
	// attempt; it is not the round at which the artifact was produced.
	Round int
	// SourceCommit is the resolved git commit OID of the run ref at fixture
	// read time; it pins the snapshot consistently.
	SourceCommit string
	// Stage is the matched runmanifest.Stage name (same as Phase for plan stages).
	Stage string
}

// LabelHint is an optional weak hint derived from the source's observable gate
// history. It is a passthrough of observable facts only; no scoring logic here.
// A zero-valued LabelHint (Status == "") means no hint is available.
type LabelHint struct {
	// Status is the aggregate gate status of the most recent gate attempt for
	// this phase (e.g. "pass", "rerun"). Empty when no gates are recorded.
	Status GateHintStatus
	// Rounds is the total number of gate attempts recorded for this phase.
	// For RunRefsSource, Rounds > 1 means the artifact required reruns before
	// passing (or is still in rerun state).
	Rounds int
	// FinalRound is the Round field of the highest-round GateAttempt for this
	// phase. This is distinct from Rounds (attempt count): with a gapped,
	// non-contiguous, or partial round sequence they diverge. Zero when no
	// gates are recorded.
	FinalRound int
	// Seats holds per-seat verdict summaries from the final gate attempt.
	Seats []SeatHint
}

// GateHintStatus mirrors runmanifest.GateStatus but is defined here so the
// contract file has no dependency on the manifest package.
type GateHintStatus string

const (
	GateHintPass      GateHintStatus = "pass"
	GateHintRerun     GateHintStatus = "rerun"
	GateHintEscalated GateHintStatus = "escalated"
)

// SeatHint is a per-seat verdict summary within a LabelHint.
type SeatHint struct {
	// Seat is the reviewer seat identifier (e.g. "opus", "codex").
	Seat string
	// Verdict is the seat's verdict (e.g. "go", "block").
	Verdict string
	// RequiredCount is the number of required-change items surfaced by this seat.
	RequiredCount int
}
