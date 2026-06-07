package bench

import (
	"math"
	"testing"
)

func TestScoreGatePredictionsConfusionMatrixAndRates(t *testing.T) {
	predictions := []GatePrediction{
		prediction("variant-a", "run-1", GateVerdictBlock, label("run-1", GateVerdictBlock)), // TP
		prediction("variant-a", "run-2", GateVerdictGO, label("run-2", GateVerdictBlock)),    // FN
		prediction("variant-a", "run-3", GateVerdictBlock, label("run-3", GateVerdictGO)),    // FP
		prediction("variant-a", "run-4", GateVerdictGO, label("run-4", GateVerdictGO)),       // TN
	}

	reports, err := ScoreGatePredictions(predictions)
	if err != nil {
		t.Fatalf("ScoreGatePredictions: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("len(reports) = %d, want 1", len(reports))
	}
	r := reports[0]
	if r.Total != 4 || r.Scored != 4 || r.Unlabeled != 0 {
		t.Fatalf("counts = total %d scored %d unlabeled %d, want 4/4/0", r.Total, r.Scored, r.Unlabeled)
	}
	if r.Matrix.TruePositive != 1 || r.Matrix.FalseNegative != 1 || r.Matrix.FalsePositive != 1 || r.Matrix.TrueNegative != 1 {
		t.Fatalf("matrix = %+v, want 1 in each bucket", r.Matrix)
	}
	if !almostEqualGate(r.WinRate, 0.5) {
		t.Fatalf("WinRate = %f, want 0.5", r.WinRate)
	}
	if !almostEqualGate(r.CatchRate, 0.5) {
		t.Fatalf("CatchRate = %f, want 0.5", r.CatchRate)
	}
	if !almostEqualGate(r.AvoidOverblockRate, 0.5) {
		t.Fatalf("AvoidOverblockRate = %f, want 0.5", r.AvoidOverblockRate)
	}
	if len(r.Cells) != 4 {
		t.Fatalf("len(Cells) = %d, want 4", len(r.Cells))
	}
	if r.Cells[0].Outcome != GateOutcomeTruePositive {
		t.Fatalf("first outcome = %q, want true_positive", r.Cells[0].Outcome)
	}
}

func TestScoreGatePredictionsGroupsByVariantInInputOrder(t *testing.T) {
	reports, err := ScoreGatePredictions([]GatePrediction{
		prediction("variant-b", "run-1", GateVerdictGO, label("run-1", GateVerdictGO)),
		prediction("variant-a", "run-1", GateVerdictBlock, label("run-1", GateVerdictBlock)),
		prediction("variant-b", "run-2", GateVerdictBlock, label("run-2", GateVerdictGO)),
	})
	if err != nil {
		t.Fatalf("ScoreGatePredictions: %v", err)
	}
	if len(reports) != 2 {
		t.Fatalf("len(reports) = %d, want 2", len(reports))
	}
	if reports[0].Variant != "variant-b" || reports[1].Variant != "variant-a" {
		t.Fatalf("variants = %q, %q; want variant-b, variant-a", reports[0].Variant, reports[1].Variant)
	}
	if reports[0].Total != 2 || reports[0].Scored != 2 {
		t.Fatalf("variant-b counts = total %d scored %d, want 2/2", reports[0].Total, reports[0].Scored)
	}
	if reports[1].Total != 1 || reports[1].Scored != 1 {
		t.Fatalf("variant-a counts = total %d scored %d, want 1/1", reports[1].Total, reports[1].Scored)
	}
}

func TestScoreGatePredictionsUnlabeledAndZeroRates(t *testing.T) {
	reports, err := ScoreGatePredictions([]GatePrediction{
		{
			Variant:   "variant-a",
			Key:       GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
			Predicted: GateVerdictGO,
			Label:     nil,
		},
	})
	if err != nil {
		t.Fatalf("ScoreGatePredictions: %v", err)
	}
	r := reports[0]
	if r.Total != 1 || r.Scored != 0 || r.Unlabeled != 1 {
		t.Fatalf("counts = total %d scored %d unlabeled %d, want 1/0/1", r.Total, r.Scored, r.Unlabeled)
	}
	if r.WinRate != 0 || r.CatchRate != 0 || r.AvoidOverblockRate != 0 {
		t.Fatalf("rates = %f/%f/%f, want all zero", r.WinRate, r.CatchRate, r.AvoidOverblockRate)
	}
	if math.IsNaN(r.WinRate) || math.IsNaN(r.CatchRate) || math.IsNaN(r.AvoidOverblockRate) {
		t.Fatal("rates must not be NaN")
	}
}

func TestScoreGatePredictionsWarningsDeduped(t *testing.T) {
	explicit := label("run-1", GateVerdictGO)
	explicit.Warnings = []string{"explicit warning"}
	proxy := label("run-2", GateVerdictBlock)
	proxy.Source = GateLabelSourceProgressionProxy
	proxy.Verified = false
	proxy.Warnings = []string{WarningProgressionProxyCircularity}

	reports, err := ScoreGatePredictions([]GatePrediction{
		{
			Variant:   "variant-a",
			Key:       explicit.Key,
			Predicted: GateVerdictGO,
			Label:     &explicit,
			Warnings:  []string{"explicit warning"},
		},
		{
			Variant:   "variant-a",
			Key:       proxy.Key,
			Predicted: GateVerdictBlock,
			Label:     &proxy,
		},
		{
			Variant:   "variant-a",
			Key:       proxy.Key,
			Predicted: GateVerdictBlock,
			Label:     &proxy,
		},
	})
	if err != nil {
		t.Fatalf("ScoreGatePredictions: %v", err)
	}
	warnings := reports[0].Warnings
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want 2 deduped warnings", warnings)
	}
	if warnings[0] != "explicit warning" || warnings[1] != WarningProgressionProxyCircularity {
		t.Fatalf("warnings = %v, want explicit then circularity", warnings)
	}
}

func TestScoreGatePredictionsRejectsInvalidPrediction(t *testing.T) {
	cases := []struct {
		name string
		p    GatePrediction
	}{
		{
			name: "empty variant",
			p: GatePrediction{
				Key:       GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
				Predicted: GateVerdictGO,
			},
		},
		{
			name: "bad predicted",
			p: GatePrediction{
				Variant:   "variant-a",
				Key:       GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
				Predicted: "maybe",
			},
		},
		{
			name: "bad label",
			p: GatePrediction{
				Variant:   "variant-a",
				Key:       GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
				Predicted: GateVerdictGO,
				Label: &GateLabel{
					Key:      GateLabelKey{RunID: "run-1", Stage: "plan", Round: 0},
					Expected: GateVerdictGO,
					Source:   GateLabelSourceExplicit,
				},
			},
		},
		{
			name: "prediction key mismatch",
			p: GatePrediction{
				Variant:   "variant-a",
				Key:       GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
				Predicted: GateVerdictGO,
				Label: &GateLabel{
					Key:      GateLabelKey{RunID: "run-2", Stage: "plan", Round: 1},
					Expected: GateVerdictGO,
					Source:   GateLabelSourceExplicit,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ScoreGatePredictions([]GatePrediction{tc.p}); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func prediction(variant, runID string, predicted GateVerdict, label GateLabel) GatePrediction {
	return GatePrediction{
		Variant:   variant,
		Key:       label.Key,
		Predicted: predicted,
		Label:     &label,
	}
}

func label(runID string, expected GateVerdict) GateLabel {
	return GateLabel{
		Key: GateLabelKey{
			RunID: runID,
			Stage: "plan",
			Round: 1,
		},
		Expected: expected,
		Source:   GateLabelSourceExplicit,
		Verified: true,
	}
}

func almostEqualGate(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
