// Package registry defines and validates the .etude/registry.yaml schema.
// It provides read (ParseYAML) and write (YAML, Default) halves so the
// consumer (etude-init-command) can scaffold and parse the file without any
// circular dependency.
package registry

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalidRegistry is the sentinel error returned by Validate and ParseYAML
// when the registry does not satisfy the schema rules.
var ErrInvalidRegistry = errors.New("invalid registry")

// Registry is the top-level model for .etude/registry.yaml.
type Registry struct {
	// Quorum is the quorum strategy for seat voting.  Empty means "unanimous"
	// (the default).  Valid values: "unanimous", "majority".
	Quorum string
	// Seats is the map of named seat definitions.
	Seats map[string]Seat
	// Tiers is the map of named tier presets.
	Tiers map[string]Tier
}

// Seat is a model/harness identity that participates in gate reviews.
type Seat struct {
	// Provider is the model provider and model identifier (required).
	Provider string
	// Harness is the CLI harness name used to invoke the seat (required).
	Harness string
	// Invoke is the canonical non-interactive invocation string (required).
	Invoke string
	// Mode is the per-seat execution constraint (optional, e.g. "inline",
	// "diff-only", "inline-no-tools").
	Mode string
	// ModelFallbacks is an ordered list of fallback model identifiers to try
	// if the primary model is unavailable.
	ModelFallbacks []string
}

// Tier is a named preset grouping one or more seats into a review panel.
type Tier struct {
	// Name is an optional human-readable label for the tier.
	Name string
	// Seats is the ordered list of seat keys that form this tier.  Required;
	// must be non-empty and every entry must resolve to a defined seat.
	Seats []string
	// Use is optional prose describing when to use this tier.
	Use string
}

// EffectiveQuorum returns the quorum value, defaulting to "unanimous" when
// Quorum is empty.
func (r Registry) EffectiveQuorum() string {
	if r.Quorum == "" {
		return "unanimous"
	}
	return r.Quorum
}

// Validate checks all well-formedness rules and returns a wrapped
// ErrInvalidRegistry on the first violation.
func (r Registry) Validate() error {
	if r.Quorum != "" && r.Quorum != "unanimous" && r.Quorum != "majority" {
		return fmt.Errorf("%w: quorum must be \"unanimous\" or \"majority\", got %q", ErrInvalidRegistry, r.Quorum)
	}
	for key, seat := range r.Seats {
		if err := validateIdentKey("seat", key); err != nil {
			return err
		}
		if strings.TrimSpace(seat.Provider) == "" {
			return fmt.Errorf("%w: seat[%q].provider required", ErrInvalidRegistry, key)
		}
		if strings.TrimSpace(seat.Harness) == "" {
			return fmt.Errorf("%w: seat[%q].harness required", ErrInvalidRegistry, key)
		}
		if strings.TrimSpace(seat.Invoke) == "" {
			return fmt.Errorf("%w: seat[%q].invoke required", ErrInvalidRegistry, key)
		}
	}
	for key, tier := range r.Tiers {
		if err := validateIdentKey("tier", key); err != nil {
			return err
		}
		if len(tier.Seats) == 0 {
			return fmt.Errorf("%w: tier[%q].seats must be non-empty", ErrInvalidRegistry, key)
		}
		for _, seatKey := range tier.Seats {
			if _, ok := r.Seats[seatKey]; !ok {
				return fmt.Errorf("%w: tier[%q] references undefined seat %q", ErrInvalidRegistry, key, seatKey)
			}
		}
	}
	return nil
}

// validateIdentKey checks that a map key matches the identifier charset
// [A-Za-z0-9_.-], mirroring the workflow stage-name rule.
func validateIdentKey(kind, key string) error {
	if key == "" {
		return fmt.Errorf("%w: %s key must not be empty", ErrInvalidRegistry, kind)
	}
	for _, r := range key {
		if !isIdentChar(r) {
			return fmt.Errorf("%w: invalid %s key %q (must match [A-Za-z0-9_.-])", ErrInvalidRegistry, kind, key)
		}
	}
	return nil
}

// isIdentChar returns true for [A-Za-z0-9_.-], the manifest identifier charset.
func isIdentChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
}

// YAML serializes the Registry to canonical YAML bytes.  Returns an error if
// the registry fails Validate.
func (r Registry) YAML() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(r.toYAML()); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseYAML decodes YAML bytes, maps them to the typed model, and validates.
// Unknown fields are rejected (mirrors manifest's DisallowUnknownFields).
func ParseYAML(content []byte) (Registry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	var doc registryYAML
	if err := dec.Decode(&doc); err != nil {
		return Registry{}, fmt.Errorf("%w: decode: %v", ErrInvalidRegistry, err)
	}
	if err := ensureEOF(dec); err != nil {
		return Registry{}, err
	}
	reg := doc.toRegistry()
	if err := reg.Validate(); err != nil {
		return Registry{}, err
	}
	return reg, nil
}

// ensureEOF rejects trailing data or extra YAML documents after the first one,
// mirroring workflow.ParseYAML's strictness.
func ensureEOF(dec *yaml.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidRegistry, err)
	}
	return fmt.Errorf("%w: trailing data after first document", ErrInvalidRegistry)
}

// Default returns the canonical scaffold registry for etude init.  The seats
// and tiers are modeled on the prior tier/seat config in a concise form that
// users edit to configure their own reviewers.
func Default() Registry {
	return Registry{
		Quorum: "unanimous",
		Seats: map[string]Seat{
			"codex": {
				Provider:       "openai/gpt-5.5",
				Harness:        "codex",
				Invoke:         "codex exec --ephemeral -m gpt-5.5 -s read-only -",
				Mode:           "diff-only",
				ModelFallbacks: []string{"gpt-5.4", "gpt-5.3"},
			},
			"gemini": {
				Provider:       "google/gemini-3.1-pro-preview",
				Harness:        "gemini-cli",
				Invoke:         "gemini -m gemini-3.1-pro-preview -p",
				Mode:           "inline-no-tools",
				ModelFallbacks: []string{"gemini-3-pro-preview"},
			},
			"opus": {
				Provider: "anthropic/claude-opus",
				Harness:  "claude-code",
				Invoke:   "claude -p --model opus",
				Mode:     "inline",
			},
		},
		Tiers: map[string]Tier{
			"L1": {Name: "Full three-seat gate", Seats: []string{"gemini", "opus", "codex"}},
			"L2": {Name: "Strong two-seat gate", Seats: []string{"opus", "codex"}},
			"L3": {Name: "Medium two-seat gate", Seats: []string{"opus", "codex"}},
			"L4": {Name: "Light single-seat gate", Seats: []string{"opus"}},
		},
	}
}

// ---- YAML decode/encode layer -----------------------------------------------

type registryYAML struct {
	Quorum string              `yaml:"quorum,omitempty"`
	Seats  map[string]seatYAML `yaml:"seats,omitempty"`
	Tiers  map[string]tierYAML `yaml:"tiers,omitempty"`
}

type seatYAML struct {
	Provider       string   `yaml:"provider"`
	Harness        string   `yaml:"harness"`
	Invoke         string   `yaml:"invoke"`
	Mode           string   `yaml:"mode,omitempty"`
	ModelFallbacks []string `yaml:"model_fallbacks,omitempty"`
}

type tierYAML struct {
	Name  string   `yaml:"name,omitempty"`
	Seats []string `yaml:"seats"`
	Use   string   `yaml:"use,omitempty"`
}

func (r Registry) toYAML() registryYAML {
	out := registryYAML{Quorum: r.Quorum}
	if len(r.Seats) > 0 {
		out.Seats = make(map[string]seatYAML, len(r.Seats))
		for k, s := range r.Seats {
			out.Seats[k] = seatYAML{
				Provider:       s.Provider,
				Harness:        s.Harness,
				Invoke:         s.Invoke,
				Mode:           s.Mode,
				ModelFallbacks: s.ModelFallbacks,
			}
		}
	}
	if len(r.Tiers) > 0 {
		out.Tiers = make(map[string]tierYAML, len(r.Tiers))
		for k, t := range r.Tiers {
			out.Tiers[k] = tierYAML{
				Name:  t.Name,
				Seats: t.Seats,
				Use:   t.Use,
			}
		}
	}
	return out
}

func (d registryYAML) toRegistry() Registry {
	r := Registry{Quorum: d.Quorum}
	if len(d.Seats) > 0 {
		r.Seats = make(map[string]Seat, len(d.Seats))
		for k, s := range d.Seats {
			r.Seats[k] = Seat{
				Provider:       s.Provider,
				Harness:        s.Harness,
				Invoke:         s.Invoke,
				Mode:           s.Mode,
				ModelFallbacks: s.ModelFallbacks,
			}
		}
	}
	if len(d.Tiers) > 0 {
		r.Tiers = make(map[string]Tier, len(d.Tiers))
		for k, t := range d.Tiers {
			r.Tiers[k] = Tier{
				Name:  t.Name,
				Seats: t.Seats,
				Use:   t.Use,
			}
		}
	}
	return r
}
