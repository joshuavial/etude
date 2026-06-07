package bench

import (
	"errors"
	"testing"
)

func TestParseGateLabelsJSONValid(t *testing.T) {
	set, err := ParseGateLabelsJSON([]byte(`{
		"version": 1,
		"labels": [
			{
				"run_id": "etude-aul.2",
				"stage": "plan",
				"round": 1,
				"expected": "block",
				"verified": true,
				"note": "missed regression"
			}
		]
	}`))
	if err != nil {
		t.Fatalf("ParseGateLabelsJSON: %v", err)
	}
	if set.Len() != 1 {
		t.Fatalf("Len = %d, want 1", set.Len())
	}
	label, ok := set.Lookup(GateLabelKey{RunID: "etude-aul.2", Stage: "plan", Round: 1})
	if !ok {
		t.Fatal("label not found")
	}
	if label.Expected != GateVerdictBlock || label.Source != GateLabelSourceExplicit || !label.Verified {
		t.Fatalf("label = %+v, want explicit verified block", label)
	}
	if label.Note != "missed regression" {
		t.Fatalf("Note = %q, want missed regression", label.Note)
	}
}

func TestParseGateLabelsJSONRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{
			name: "unknown top-level field",
			json: `{"version":1,"labels":[],"extra":true}`,
		},
		{
			name: "unknown label field",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"plan","round":1,"expected":"go","extra":true}]}`,
		},
		{
			name: "unsupported version",
			json: `{"version":2,"labels":[]}`,
		},
		{
			name: "missing labels",
			json: `{"version":1}`,
		},
		{
			name: "duplicate key",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"plan","round":1,"expected":"go"},{"run_id":"run-1","stage":"plan","round":1,"expected":"block"}]}`,
		},
		{
			name: "uppercase verdict",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"plan","round":1,"expected":"GO"}]}`,
		},
		{
			name: "bad verdict",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"plan","round":1,"expected":"maybe"}]}`,
		},
		{
			name: "bad run id",
			json: `{"version":1,"labels":[{"run_id":".bad","stage":"plan","round":1,"expected":"go"}]}`,
		},
		{
			name: "bad stage",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"bad/stage","round":1,"expected":"go"}]}`,
		},
		{
			name: "non-positive round",
			json: `{"version":1,"labels":[{"run_id":"run-1","stage":"plan","round":0,"expected":"go"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseGateLabelsJSON([]byte(tc.json))
			if !errors.Is(err, ErrInvalidGateLabel) {
				t.Fatalf("want ErrInvalidGateLabel, got %v", err)
			}
		})
	}
}

func TestGateLabelResolverExplicitWinsOverProxy(t *testing.T) {
	explicit, err := NewGateLabelSet([]GateLabel{{
		Key:      GateLabelKey{RunID: "run-1", Stage: "plan", Round: 2},
		Expected: GateVerdictGO,
		Source:   GateLabelSourceExplicit,
		Verified: true,
	}})
	if err != nil {
		t.Fatalf("NewGateLabelSet: %v", err)
	}
	resolver := GateLabelResolver{Explicit: explicit, UseProgressionProxy: true}

	label, ok, err := resolver.Resolve(proxyFixture("run-1", 2, GateHintPass, 3))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("want label")
	}
	if label.Source != GateLabelSourceExplicit || label.Expected != GateVerdictGO || !label.Verified {
		t.Fatalf("label = %+v, want explicit verified go", label)
	}
}

func TestGateLabelSetRejectsVerifiedProgressionProxy(t *testing.T) {
	_, err := NewGateLabelSet([]GateLabel{{
		Key:      GateLabelKey{RunID: "run-1", Stage: "plan", Round: 1},
		Expected: GateVerdictBlock,
		Source:   GateLabelSourceProgressionProxy,
		Verified: true,
	}})
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("want ErrInvalidGateLabel, got %v", err)
	}
}

func TestGateLabelResolverProxyDisabledOrUnavailable(t *testing.T) {
	resolver := GateLabelResolver{UseProgressionProxy: false}
	if _, ok, err := resolver.Resolve(proxyFixture("run-1", 1, GateHintPass, 1)); err != nil || ok {
		t.Fatalf("proxy disabled: ok=%v err=%v, want unlabeled nil", ok, err)
	}

	resolver.UseProgressionProxy = true
	f := proxyFixture("run-1", 1, "", 0)
	if _, ok, err := resolver.Resolve(f); err != nil || ok {
		t.Fatalf("zero hint: ok=%v err=%v, want unlabeled nil", ok, err)
	}
}

func TestGateLabelResolverProgressionProxy(t *testing.T) {
	cases := []struct {
		name       string
		round      int
		status     GateHintStatus
		finalRound int
		want       GateVerdict
	}{
		{name: "final pass is clean", round: 3, status: GateHintPass, finalRound: 3, want: GateVerdictGO},
		{name: "earlier round before final pass is issue", round: 2, status: GateHintPass, finalRound: 3, want: GateVerdictBlock},
		{name: "rerun is issue", round: 1, status: GateHintRerun, finalRound: 1, want: GateVerdictBlock},
		{name: "escalated is issue", round: 1, status: GateHintEscalated, finalRound: 1, want: GateVerdictBlock},
	}

	resolver := GateLabelResolver{UseProgressionProxy: true}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, ok, err := resolver.Resolve(proxyFixture("run-1", tc.round, tc.status, tc.finalRound))
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !ok {
				t.Fatal("want label")
			}
			if label.Expected != tc.want {
				t.Fatalf("Expected = %q, want %q", label.Expected, tc.want)
			}
			if label.Source != GateLabelSourceProgressionProxy || label.Verified {
				t.Fatalf("label source/verified = %+v, want unverified proxy", label)
			}
			if len(label.Warnings) != 1 || label.Warnings[0] != WarningProgressionProxyCircularity {
				t.Fatalf("Warnings = %v, want circularity warning", label.Warnings)
			}
		})
	}
}

func TestGateLabelResolverProgressionProxyRejectsInconsistentRound(t *testing.T) {
	resolver := GateLabelResolver{UseProgressionProxy: true}
	_, _, err := resolver.Resolve(proxyFixture("run-1", 4, GateHintPass, 3))
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("want ErrInvalidGateLabel, got %v", err)
	}

	_, _, err = resolver.Resolve(proxyFixture("run-1", 4, GateHintRerun, 3))
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("rerun: want ErrInvalidGateLabel, got %v", err)
	}

	_, _, err = resolver.Resolve(proxyFixture("run-1", 4, GateHintEscalated, 3))
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("escalated: want ErrInvalidGateLabel, got %v", err)
	}

	_, _, err = resolver.Resolve(proxyFixture("run-1", 1, GateHintRerun, 0))
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("rerun missing final round: want ErrInvalidGateLabel, got %v", err)
	}

	_, _, err = resolver.Resolve(proxyFixture("run-1", 1, GateHintEscalated, 0))
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("escalated missing final round: want ErrInvalidGateLabel, got %v", err)
	}
}

func TestGateVerdictFromPassed(t *testing.T) {
	passed := true
	got, err := GateVerdictFromPassed(&passed)
	if err != nil || got != GateVerdictGO {
		t.Fatalf("true => %q err=%v, want go nil", got, err)
	}

	passed = false
	got, err = GateVerdictFromPassed(&passed)
	if err != nil || got != GateVerdictBlock {
		t.Fatalf("false => %q err=%v, want block nil", got, err)
	}

	_, err = GateVerdictFromPassed(nil)
	if !errors.Is(err, ErrInvalidGateLabel) {
		t.Fatalf("nil: want ErrInvalidGateLabel, got %v", err)
	}
}

func proxyFixture(runID string, round int, status GateHintStatus, finalRound int) Fixture {
	return Fixture{
		Provenance: Provenance{
			RunID: runID,
			Stage: "plan",
			Round: round,
		},
		Label: LabelHint{
			Status:     status,
			Rounds:     finalRound,
			FinalRound: finalRound,
		},
	}
}
