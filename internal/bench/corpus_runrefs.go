package bench

import (
	"context"
	"fmt"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
)

// RunRefsSource implements CorpusSource over a refstore.Store.
//
// It delivers ONE plan-TEXT fixture per qualifying run — the final captured
// plan artifact — annotated with the multi-round gate progression as LabelHint
// values. It does NOT reconstruct per-round plan text: dogfood capture
// overwrites the plan stage with the final artifact, so only one plan text
// exists per run ref. Per-round plan texts require the separate beads/Dolt
// adapter (a future bead), which core must not import.
//
// This is the built-in default; adapter selection (mirror of resolveRunner)
// lands in aul.4. Until then, intra-core callers construct RunRefsSource
// directly and pass it where a CorpusSource is expected.
type RunRefsSource struct {
	Store refstore.Store
}

// Fixtures implements CorpusSource.
//
// It calls SelectCohort with sel.Stage and sel.Last to enumerate eligible runs,
// then for each CohortRun:
//  1. Reads the plan output bytes via Store.ReadCommitFile.
//  2. Reads the manifest to harvest GateAttempts for the phase matching the
//     stage name, building a LabelHint from the final round's status and seats.
func (r RunRefsSource) Fixtures(ctx context.Context, sel CohortSelector) ([]Fixture, error) {
	cohort, err := SelectCohort(ctx, r.Store, sel.Stage, sel.Last)
	if err != nil {
		return nil, fmt.Errorf("corpus runrefs: select cohort: %w", err)
	}

	fixtures := make([]Fixture, 0, len(cohort.Selected))
	for _, cr := range cohort.Selected {
		f, err := r.fixtureFromRun(ctx, sel.Stage, cr)
		if err != nil {
			return nil, fmt.Errorf("corpus runrefs: run %s: %w", cr.RunID, err)
		}
		fixtures = append(fixtures, f)
	}
	return fixtures, nil
}

// fixtureFromRun builds a single Fixture for the given CohortRun.
func (r RunRefsSource) fixtureFromRun(ctx context.Context, phase string, cr CohortRun) (Fixture, error) {
	// Read the plan artifact bytes from the pinned commit.
	artifactBytes, err := r.Store.ReadCommitFile(ctx, cr.Commit, cr.Stage.Output.Path)
	if err != nil {
		return Fixture{}, fmt.Errorf("read output artifact: %w", err)
	}

	// Re-read the manifest from the same pinned commit to harvest GateAttempts.
	// (cohort.go already parsed it for classification, but does not expose it;
	// re-reading from the same commit OID is correct and consistent.)
	manifestBytes, err := r.Store.ReadCommitFile(ctx, cr.Commit, "manifest.json")
	if err != nil {
		return Fixture{}, fmt.Errorf("read manifest: %w", err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return Fixture{}, fmt.Errorf("parse manifest: %w", err)
	}

	// Harvest using cr.Stage.Name (the matched stage name) rather than the
	// sel.Stage/phase parameter so the filter matches the contract directly.
	hint := harvestLabelHint(cr.Stage.Name, manifest.Gates)

	return Fixture{
		Artifact:  artifactBytes,
		MediaType: cr.Stage.Output.MediaType,
		Provenance: Provenance{
			RunID:        cr.RunID,
			Phase:        phase,
			Round:        hint.FinalRound, // highest recorded Round number, 0 when no gates
			SourceCommit: cr.Commit,
			Stage:        cr.Stage.Name,
		},
		Label: hint,
	}, nil
}

// harvestLabelHint builds a LabelHint from the GateAttempts matching phase.
// Rounds is the count of matching attempts; FinalRound is the highest Round
// field observed (not the count — they diverge for gapped or non-contiguous
// round sequences). Returns a zero-valued LabelHint when no gates match.
func harvestLabelHint(phase string, gates []runmanifest.GateAttempt) LabelHint {
	// Collect all gate attempts for this phase.
	var phaseGates []runmanifest.GateAttempt
	for _, g := range gates {
		if g.Phase == phase {
			phaseGates = append(phaseGates, g)
		}
	}
	if len(phaseGates) == 0 {
		return LabelHint{}
	}

	// Find the gate attempt with the highest round number.
	// Equal-round tie-break keeps first-seen (stable for well-formed manifests
	// where Validate enforces unique (phase, round) pairs).
	latest := phaseGates[0]
	for _, g := range phaseGates[1:] {
		if g.Round > latest.Round {
			latest = g
		}
	}

	seats := make([]SeatHint, 0, len(latest.Seats))
	for _, s := range latest.Seats {
		seats = append(seats, SeatHint{
			Seat:          s.Seat,
			Verdict:       string(s.Verdict),
			RequiredCount: len(s.Required),
		})
	}

	return LabelHint{
		Status:     GateHintStatus(latest.Status),
		Rounds:     len(phaseGates),
		FinalRound: latest.Round,
		Seats:      seats,
	}
}
