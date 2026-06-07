package bench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/joshuavial/etude/internal/runmanifest"
)

type GateVerdict string

const (
	GateVerdictGO    GateVerdict = "go"
	GateVerdictBlock GateVerdict = "block"
)

type GateLabelSource string

const (
	GateLabelSourceExplicit         GateLabelSource = "explicit"
	GateLabelSourceProgressionProxy GateLabelSource = "progression-proxy"
)

const WarningProgressionProxyCircularity = "progression proxy is circular: a gate that over-blocks can score well against its own historical blocks; prefer verified explicit labels."

var ErrInvalidGateLabel = errors.New("invalid gate label")

type GateLabelKey struct {
	RunID string
	Stage string
	Round int
}

type GateLabel struct {
	Key      GateLabelKey
	Expected GateVerdict
	Source   GateLabelSource
	Verified bool
	Note     string
	Warnings []string
}

type GateLabelSet struct {
	labels map[GateLabelKey]GateLabel
}

func NewGateLabelSet(labels []GateLabel) (GateLabelSet, error) {
	set := GateLabelSet{labels: make(map[GateLabelKey]GateLabel, len(labels))}
	for _, label := range labels {
		if label.Source == "" {
			label.Source = GateLabelSourceExplicit
		}
		if err := validateGateLabel(label); err != nil {
			return GateLabelSet{}, err
		}
		if _, exists := set.labels[label.Key]; exists {
			return GateLabelSet{}, fmt.Errorf("%w: duplicate label for run %q stage %q round %d", ErrInvalidGateLabel, label.Key.RunID, label.Key.Stage, label.Key.Round)
		}
		set.labels[label.Key] = label
	}
	return set, nil
}

func (s GateLabelSet) Lookup(key GateLabelKey) (GateLabel, bool) {
	if s.labels == nil {
		return GateLabel{}, false
	}
	label, ok := s.labels[key]
	return label, ok
}

func (s GateLabelSet) Len() int {
	return len(s.labels)
}

func ParseGateLabelsJSON(content []byte) (GateLabelSet, error) {
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.DisallowUnknownFields()

	var wire gateLabelsFileJSON
	if err := dec.Decode(&wire); err != nil {
		return GateLabelSet{}, fmt.Errorf("%w: decode: %v", ErrInvalidGateLabel, err)
	}
	if err := ensureGateLabelsEOF(dec); err != nil {
		return GateLabelSet{}, err
	}
	if wire.Version != 1 {
		return GateLabelSet{}, fmt.Errorf("%w: version must be 1, got %d", ErrInvalidGateLabel, wire.Version)
	}
	if wire.Labels == nil {
		return GateLabelSet{}, fmt.Errorf("%w: labels required", ErrInvalidGateLabel)
	}

	labels := make([]GateLabel, 0, len(wire.Labels))
	for i, item := range wire.Labels {
		label := GateLabel{
			Key: GateLabelKey{
				RunID: item.RunID,
				Stage: item.Stage,
				Round: item.Round,
			},
			Expected: item.Expected,
			Source:   GateLabelSourceExplicit,
			Verified: item.Verified,
			Note:     item.Note,
		}
		if err := validateGateLabel(label); err != nil {
			return GateLabelSet{}, fmt.Errorf("%w: labels[%d]: %v", ErrInvalidGateLabel, i, err)
		}
		labels = append(labels, label)
	}

	return NewGateLabelSet(labels)
}

type GateLabelResolver struct {
	Explicit            GateLabelSet
	UseProgressionProxy bool
}

func (r GateLabelResolver) Resolve(f Fixture) (GateLabel, bool, error) {
	key := GateLabelKey{
		RunID: f.Provenance.RunID,
		Stage: f.Provenance.Stage,
		Round: f.Provenance.Round,
	}
	if label, ok := r.Explicit.Lookup(key); ok {
		return label, true, nil
	}
	if !r.UseProgressionProxy {
		return GateLabel{}, false, nil
	}
	return labelFromProgressionProxy(key, f.Label)
}

func GateVerdictFromPassed(passed *bool) (GateVerdict, error) {
	if passed == nil {
		return "", fmt.Errorf("%w: gate passed verdict required", ErrInvalidGateLabel)
	}
	if *passed {
		return GateVerdictGO, nil
	}
	return GateVerdictBlock, nil
}

func labelFromProgressionProxy(key GateLabelKey, hint LabelHint) (GateLabel, bool, error) {
	if hint.Status == "" {
		return GateLabel{}, false, nil
	}
	if err := validateGateLabelKey(key); err != nil {
		return GateLabel{}, false, err
	}

	var expected GateVerdict
	switch hint.Status {
	case GateHintPass:
		if hint.FinalRound <= 0 {
			return GateLabel{}, false, fmt.Errorf("%w: progression proxy pass requires final round", ErrInvalidGateLabel)
		}
		if key.Round > hint.FinalRound {
			return GateLabel{}, false, fmt.Errorf("%w: fixture round %d exceeds final round %d", ErrInvalidGateLabel, key.Round, hint.FinalRound)
		}
		if key.Round == hint.FinalRound {
			expected = GateVerdictGO
		} else {
			expected = GateVerdictBlock
		}
	case GateHintRerun, GateHintEscalated:
		if hint.FinalRound <= 0 {
			return GateLabel{}, false, fmt.Errorf("%w: progression proxy %s requires final round", ErrInvalidGateLabel, hint.Status)
		}
		if key.Round > hint.FinalRound {
			return GateLabel{}, false, fmt.Errorf("%w: fixture round %d exceeds final round %d", ErrInvalidGateLabel, key.Round, hint.FinalRound)
		}
		expected = GateVerdictBlock
	default:
		return GateLabel{}, false, fmt.Errorf("%w: unsupported progression status %q", ErrInvalidGateLabel, hint.Status)
	}

	return GateLabel{
		Key:      key,
		Expected: expected,
		Source:   GateLabelSourceProgressionProxy,
		Verified: false,
		Warnings: []string{WarningProgressionProxyCircularity},
	}, true, nil
}

func validateGateLabel(label GateLabel) error {
	if err := validateGateLabelKey(label.Key); err != nil {
		return err
	}
	if !label.Expected.valid() {
		return fmt.Errorf("%w: expected must be %q or %q, got %q", ErrInvalidGateLabel, GateVerdictGO, GateVerdictBlock, label.Expected)
	}
	switch label.Source {
	case GateLabelSourceExplicit, GateLabelSourceProgressionProxy:
	default:
		return fmt.Errorf("%w: unsupported source %q", ErrInvalidGateLabel, label.Source)
	}
	if label.Source == GateLabelSourceProgressionProxy && label.Verified {
		return fmt.Errorf("%w: progression proxy labels must not be verified", ErrInvalidGateLabel)
	}
	return nil
}

func validateGateLabelKey(key GateLabelKey) error {
	if !runmanifest.IsValidRunID(key.RunID) {
		return fmt.Errorf("%w: invalid run_id %q", ErrInvalidGateLabel, key.RunID)
	}
	if !runmanifest.IsValidIdentifier(key.Stage) {
		return fmt.Errorf("%w: invalid stage %q", ErrInvalidGateLabel, key.Stage)
	}
	if key.Round <= 0 {
		return fmt.Errorf("%w: round must be positive, got %d", ErrInvalidGateLabel, key.Round)
	}
	return nil
}

func (v GateVerdict) valid() bool {
	return v == GateVerdictGO || v == GateVerdictBlock
}

func ensureGateLabelsEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidGateLabel, err)
	}
	return fmt.Errorf("%w: trailing data", ErrInvalidGateLabel)
}

type gateLabelsFileJSON struct {
	Version int                  `json:"version"`
	Labels  []gateLabelEntryJSON `json:"labels"`
}

type gateLabelEntryJSON struct {
	RunID    string      `json:"run_id"`
	Stage    string      `json:"stage"`
	Round    int         `json:"round"`
	Expected GateVerdict `json:"expected"`
	Verified bool        `json:"verified"`
	Note     string      `json:"note,omitempty"`
}
