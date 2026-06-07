package bench

import (
	"context"
	"testing"
)

// fakeCorpusSource is a minimal non-core CorpusSource implementation that
// proves the interface seam is satisfiable without core importing an adapter.
// It stands in for the future beads/Dolt adapter.
type fakeCorpusSource struct {
	fixtures []Fixture
	err      error
}

func (f *fakeCorpusSource) Fixtures(_ context.Context, _ CohortSelector) ([]Fixture, error) {
	return f.fixtures, f.err
}

// Compile-time assertion: fakeCorpusSource satisfies CorpusSource.
var _ CorpusSource = (*fakeCorpusSource)(nil)

// TestCorpusSourceSeam verifies that a non-core implementation can satisfy the
// CorpusSource interface without importing any core adapter package, and that
// the interface contract (Fixtures returns the injected fixtures) is upheld.
func TestCorpusSourceSeam(t *testing.T) {
	want := []Fixture{
		{
			Artifact:  []byte("plan text"),
			MediaType: "text/markdown",
			Provenance: Provenance{
				RunID:        "fake-run-1",
				Phase:        "plan",
				Round:        2,
				SourceCommit: "aaaa",
				Stage:        "plan",
			},
			Label: LabelHint{
				Status: GateHintPass,
				Rounds: 2,
				Seats: []SeatHint{
					{Seat: "opus", Verdict: "go", RequiredCount: 0},
				},
			},
		},
	}

	var src CorpusSource = &fakeCorpusSource{fixtures: want}
	got, err := src.Fixtures(context.Background(), CohortSelector{Stage: "plan", Last: 10})
	if err != nil {
		t.Fatalf("Fixtures: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("len(Fixtures) = %d, want %d", len(got), len(want))
	}
	g := got[0]
	w := want[0]
	if string(g.Artifact) != string(w.Artifact) {
		t.Errorf("Artifact = %q, want %q", g.Artifact, w.Artifact)
	}
	if g.MediaType != w.MediaType {
		t.Errorf("MediaType = %q, want %q", g.MediaType, w.MediaType)
	}
	if g.Provenance != w.Provenance {
		t.Errorf("Provenance = %+v, want %+v", g.Provenance, w.Provenance)
	}
	if g.Label.Status != w.Label.Status {
		t.Errorf("Label.Status = %q, want %q", g.Label.Status, w.Label.Status)
	}
	if g.Label.Rounds != w.Label.Rounds {
		t.Errorf("Label.Rounds = %d, want %d", g.Label.Rounds, w.Label.Rounds)
	}
	if len(g.Label.Seats) != len(w.Label.Seats) {
		t.Errorf("len(Label.Seats) = %d, want %d", len(g.Label.Seats), len(w.Label.Seats))
	}
}

// TestLabelHintZeroValue confirms that a zero-valued LabelHint is distinguishable
// (Status == "") so callers can tell when no gate history is available.
func TestLabelHintZeroValue(t *testing.T) {
	var hint LabelHint
	if hint.Status != "" {
		t.Errorf("zero LabelHint.Status = %q, want empty string", hint.Status)
	}
	if hint.Rounds != 0 {
		t.Errorf("zero LabelHint.Rounds = %d, want 0", hint.Rounds)
	}
	if len(hint.Seats) != 0 {
		t.Errorf("zero LabelHint.Seats = %v, want nil/empty", hint.Seats)
	}
}
