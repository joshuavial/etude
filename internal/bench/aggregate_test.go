package bench

import (
	"math"
	"testing"

	"github.com/joshuavial/etude/internal/eval"
)

// outcomeWithWinner builds a minimal BenchOutcome with the given winner.
func outcomeWithWinner(winner eval.Winner) BenchOutcome {
	return BenchOutcome{Winner: winner}
}

func TestAggregateAllB(t *testing.T) {
	outcomes := []BenchOutcome{
		outcomeWithWinner(eval.WinnerB),
		outcomeWithWinner(eval.WinnerB),
		outcomeWithWinner(eval.WinnerB),
	}
	r := Aggregate(outcomes)

	if r.Total != 3 {
		t.Errorf("Total = %d, want 3", r.Total)
	}
	if r.CountB != 3 {
		t.Errorf("CountB = %d, want 3", r.CountB)
	}
	if r.CountA != 0 {
		t.Errorf("CountA = %d, want 0", r.CountA)
	}
	if r.CountTie != 0 {
		t.Errorf("CountTie = %d, want 0", r.CountTie)
	}
	if !almostEqual(r.WinRateB, 1.0) {
		t.Errorf("WinRateB = %f, want 1.0", r.WinRateB)
	}
}

func TestAggregateAllA(t *testing.T) {
	outcomes := []BenchOutcome{
		outcomeWithWinner(eval.WinnerA),
		outcomeWithWinner(eval.WinnerA),
	}
	r := Aggregate(outcomes)

	if r.Total != 2 {
		t.Errorf("Total = %d, want 2", r.Total)
	}
	if r.CountA != 2 {
		t.Errorf("CountA = %d, want 2", r.CountA)
	}
	if !almostEqual(r.WinRateB, 0.0) {
		t.Errorf("WinRateB = %f, want 0.0", r.WinRateB)
	}
}

func TestAggregateAllTie(t *testing.T) {
	outcomes := []BenchOutcome{
		outcomeWithWinner(eval.WinnerTie),
		outcomeWithWinner(eval.WinnerTie),
	}
	r := Aggregate(outcomes)

	if r.Total != 2 {
		t.Errorf("Total = %d, want 2", r.Total)
	}
	if r.CountTie != 2 {
		t.Errorf("CountTie = %d, want 2", r.CountTie)
	}
	// tie contributes 0.5 each → 0.5
	if !almostEqual(r.WinRateB, 0.5) {
		t.Errorf("WinRateB = %f, want 0.5", r.WinRateB)
	}
}

func TestAggregateMix(t *testing.T) {
	// 2 B, 1 A, 1 tie → win_rate_B = (2 + 0.5*1) / 4 = 2.5/4 = 0.625
	outcomes := []BenchOutcome{
		outcomeWithWinner(eval.WinnerB),
		outcomeWithWinner(eval.WinnerB),
		outcomeWithWinner(eval.WinnerA),
		outcomeWithWinner(eval.WinnerTie),
	}
	r := Aggregate(outcomes)

	if r.Total != 4 {
		t.Errorf("Total = %d, want 4", r.Total)
	}
	if r.CountB != 2 {
		t.Errorf("CountB = %d, want 2", r.CountB)
	}
	if r.CountA != 1 {
		t.Errorf("CountA = %d, want 1", r.CountA)
	}
	if r.CountTie != 1 {
		t.Errorf("CountTie = %d, want 1", r.CountTie)
	}
	want := 2.5 / 4.0
	if !almostEqual(r.WinRateB, want) {
		t.Errorf("WinRateB = %f, want %f", r.WinRateB, want)
	}
}

func TestAggregateTotal0Guard(t *testing.T) {
	r := Aggregate(nil)
	if r.Total != 0 {
		t.Errorf("Total = %d, want 0", r.Total)
	}
	// WinRateB must be 0 (not NaN or divide-by-zero) when total==0.
	if r.WinRateB != 0 {
		t.Errorf("WinRateB = %f, want 0 when Total==0", r.WinRateB)
	}
	if r.CountA != 0 || r.CountB != 0 || r.CountTie != 0 {
		t.Errorf("counts non-zero for empty outcomes: A=%d B=%d tie=%d", r.CountA, r.CountB, r.CountTie)
	}
}

func TestAggregateOutcomesPreserved(t *testing.T) {
	outcomes := []BenchOutcome{
		{SourceRunID: "run-1", Winner: eval.WinnerB},
		{SourceRunID: "run-2", Winner: eval.WinnerA},
	}
	r := Aggregate(outcomes)
	if len(r.Outcomes) != 2 {
		t.Errorf("len(Outcomes) = %d, want 2", len(r.Outcomes))
	}
	if r.Outcomes[0].SourceRunID != "run-1" {
		t.Errorf("Outcomes[0].SourceRunID = %q, want run-1", r.Outcomes[0].SourceRunID)
	}
}

// almostEqual reports whether a and b differ by less than 1e-9.
func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
