package bench

import "github.com/joshuavial/etude/internal/eval"

// Report is the aggregated result of a bench run over a cohort.
// It carries the win-rate headline and per-run detail for report rendering.
type Report struct {
	// Stage is the benchmarked stage name.
	Stage string
	// CountA is the number of evals where the original (A) won.
	CountA int
	// CountB is the number of evals where the replay (B) won.
	CountB int
	// CountTie is the number of tied evals.
	CountTie int
	// Total is the number of successful evaluations (len(Outcomes)).
	Total int
	// WinRateB is the win rate of the replay (new skill): (CountB + 0.5*CountTie) / Total.
	//
	// NOTE: eval.go documents win_rate_A = (A + 0.5*tie) / total (orienting toward
	// the original/A side). WinRateB is intentionally its COMPLEMENT (1 - win_rate_A
	// when there are no missing evals) so the headline answers "how often does the NEW
	// skill (replay/B) beat the original?". A high WinRateB is good for the new skill.
	// Do NOT change this to win_rate_A — that would invert the displayed orientation.
	WinRateB float64
	// Outcomes holds the successful BenchOutcomes that were aggregated.
	Outcomes []BenchOutcome
	// Failures holds runs that errored during BenchRun (reported separately).
	Failures []BenchFailure
	// Skipped holds runs that were ineligible for the cohort.
	Skipped []SkippedRun
}

// BenchFailure records a single BenchRun error for reporting.
type BenchFailure struct {
	SourceRunID string
	Err         error
}

// Aggregate computes the win-rate Report over a slice of BenchOutcomes.
// The outcomes must carry canonical winners (A=original, B=replay) as returned
// by BenchRun; the pairwise presentation swap has already been back-mapped.
//
// When outcomes is empty, Report.Total == 0 and WinRateB is 0. The caller is
// responsible for treating total==0 as an error (no successful evaluations).
func Aggregate(outcomes []BenchOutcome) Report {
	r := Report{Total: len(outcomes), Outcomes: outcomes}
	for _, o := range outcomes {
		switch o.Winner {
		case eval.WinnerA:
			r.CountA++
		case eval.WinnerB:
			r.CountB++
		case eval.WinnerTie:
			r.CountTie++
		}
	}
	if r.Total > 0 {
		r.WinRateB = (float64(r.CountB) + 0.5*float64(r.CountTie)) / float64(r.Total)
	}
	return r
}
