package bench

import "fmt"

type GateOutcome string

const (
	GateOutcomeTruePositive  GateOutcome = "true_positive"
	GateOutcomeFalseNegative GateOutcome = "false_negative"
	GateOutcomeFalsePositive GateOutcome = "false_positive"
	GateOutcomeTrueNegative  GateOutcome = "true_negative"
)

type GatePrediction struct {
	Variant   string
	Key       GateLabelKey
	Predicted GateVerdict
	Label     *GateLabel
	Warnings  []string
}

type GateScoredCell struct {
	Variant   string
	Key       GateLabelKey
	Expected  GateVerdict
	Predicted GateVerdict
	Source    GateLabelSource
	Verified  bool
	Outcome   GateOutcome
}

type GateConfusionMatrix struct {
	TruePositive  int
	FalseNegative int
	FalsePositive int
	TrueNegative  int
}

type GateVariantReport struct {
	Variant            string
	Total              int
	Scored             int
	Unlabeled          int
	Matrix             GateConfusionMatrix
	WinRate            float64
	CatchRate          float64
	AvoidOverblockRate float64
	Warnings           []string
	Cells              []GateScoredCell
}

func ScoreGatePredictions(predictions []GatePrediction) ([]GateVariantReport, error) {
	order := make([]string, 0)
	byVariant := make(map[string]*GateVariantReport)
	warningSets := make(map[string]map[string]bool)

	for i, p := range predictions {
		if p.Variant == "" {
			return nil, fmt.Errorf("%w: predictions[%d] variant required", ErrInvalidGateLabel, i)
		}
		if !p.Predicted.valid() {
			return nil, fmt.Errorf("%w: predictions[%d] predicted must be %q or %q, got %q", ErrInvalidGateLabel, i, GateVerdictGO, GateVerdictBlock, p.Predicted)
		}

		report := byVariant[p.Variant]
		if report == nil {
			report = &GateVariantReport{Variant: p.Variant}
			byVariant[p.Variant] = report
			warningSets[p.Variant] = make(map[string]bool)
			order = append(order, p.Variant)
		}
		report.Total++
		addWarnings(report, warningSets[p.Variant], p.Warnings)

		if p.Label == nil {
			report.Unlabeled++
			continue
		}
		if err := validateGateLabel(*p.Label); err != nil {
			return nil, fmt.Errorf("%w: predictions[%d] label: %v", ErrInvalidGateLabel, i, err)
		}
		if p.Key != p.Label.Key {
			return nil, fmt.Errorf("%w: predictions[%d] key does not match label key", ErrInvalidGateLabel, i)
		}
		addWarnings(report, warningSets[p.Variant], p.Label.Warnings)
		if p.Label.Source == GateLabelSourceProgressionProxy {
			addWarnings(report, warningSets[p.Variant], []string{WarningProgressionProxyCircularity})
		}

		outcome := classifyGateOutcome(p.Label.Expected, p.Predicted)
		switch outcome {
		case GateOutcomeTruePositive:
			report.Matrix.TruePositive++
		case GateOutcomeFalseNegative:
			report.Matrix.FalseNegative++
		case GateOutcomeFalsePositive:
			report.Matrix.FalsePositive++
		case GateOutcomeTrueNegative:
			report.Matrix.TrueNegative++
		}
		report.Scored++
		report.Cells = append(report.Cells, GateScoredCell{
			Variant:   p.Variant,
			Key:       p.Key,
			Expected:  p.Label.Expected,
			Predicted: p.Predicted,
			Source:    p.Label.Source,
			Verified:  p.Label.Verified,
			Outcome:   outcome,
		})
	}

	reports := make([]GateVariantReport, 0, len(order))
	for _, variant := range order {
		report := byVariant[variant]
		finalizeGateRates(report)
		reports = append(reports, *report)
	}
	return reports, nil
}

func classifyGateOutcome(expected, predicted GateVerdict) GateOutcome {
	switch {
	case expected == GateVerdictBlock && predicted == GateVerdictBlock:
		return GateOutcomeTruePositive
	case expected == GateVerdictBlock && predicted == GateVerdictGO:
		return GateOutcomeFalseNegative
	case expected == GateVerdictGO && predicted == GateVerdictBlock:
		return GateOutcomeFalsePositive
	default:
		return GateOutcomeTrueNegative
	}
}

func finalizeGateRates(report *GateVariantReport) {
	if report.Scored > 0 {
		report.WinRate = float64(report.Matrix.TruePositive+report.Matrix.TrueNegative) / float64(report.Scored)
	}

	catchDenom := report.Matrix.TruePositive + report.Matrix.FalseNegative
	if catchDenom > 0 {
		report.CatchRate = float64(report.Matrix.TruePositive) / float64(catchDenom)
	}

	avoidDenom := report.Matrix.TrueNegative + report.Matrix.FalsePositive
	if avoidDenom > 0 {
		report.AvoidOverblockRate = float64(report.Matrix.TrueNegative) / float64(avoidDenom)
	}
}

func addWarnings(report *GateVariantReport, seen map[string]bool, warnings []string) {
	for _, warning := range warnings {
		if warning == "" || seen[warning] {
			continue
		}
		seen[warning] = true
		report.Warnings = append(report.Warnings, warning)
	}
}
