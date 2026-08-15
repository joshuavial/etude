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

	"github.com/joshuavial/etude/internal/ident"
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
	// Mode is the per-seat execution constraint. It is optional; valid values
	// are "inline", "diff-only", and "inline-no-tools".
	Mode string
	// ModelFallbacks is an ordered list of fallback model identifiers to try
	// if the primary model is unavailable.
	ModelFallbacks []string
	// InvocationFallbacks is an ordered list of alternate harness commands to
	// try when the primary invocation cannot run. Retry policy belongs to the
	// consumer; this field only supplies the canonical candidates.
	InvocationFallbacks []SeatInvocation
}

// SeatInvocation is one concrete harness command for a seat. The provider and
// model identity remain those of the owning Seat.
type SeatInvocation struct {
	// Harness is the CLI harness name used by this candidate (required).
	Harness string
	// Invoke is the canonical non-interactive invocation string (required).
	Invoke string
	// Mode overrides the seat execution constraint for this candidate when set.
	// It accepts the same closed set as Seat.Mode.
	Mode string
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

// Invocations returns the primary seat invocation followed by configured
// fallback invocations in retry order. The returned slice is independent of
// the Seat's backing slice and is safe for the caller to modify.
func (s Seat) Invocations() []SeatInvocation {
	invocations := make([]SeatInvocation, 0, 1+len(s.InvocationFallbacks))
	invocations = append(invocations, SeatInvocation{
		Harness: s.Harness,
		Invoke:  s.Invoke,
		Mode:    s.Mode,
	})
	for _, fallback := range s.InvocationFallbacks {
		if fallback.Mode == "" {
			fallback.Mode = s.Mode
		}
		invocations = append(invocations, fallback)
	}
	return invocations
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
		if err := validateSeatMode(fmt.Sprintf("seat[%q].mode", key), seat.Mode); err != nil {
			return err
		}
		for i, fallback := range seat.InvocationFallbacks {
			if strings.TrimSpace(fallback.Harness) == "" {
				return fmt.Errorf("%w: seat[%q].invocation_fallbacks[%d].harness required", ErrInvalidRegistry, key, i)
			}
			if strings.TrimSpace(fallback.Invoke) == "" {
				return fmt.Errorf("%w: seat[%q].invocation_fallbacks[%d].invoke required", ErrInvalidRegistry, key, i)
			}
			if err := validateSeatMode(fmt.Sprintf("seat[%q].invocation_fallbacks[%d].mode", key, i), fallback.Mode); err != nil {
				return err
			}
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

func validateSeatMode(field, mode string) error {
	switch mode {
	case "", "inline", "diff-only", "inline-no-tools":
		return nil
	default:
		return fmt.Errorf("%w: %s must be one of inline, diff-only, inline-no-tools, got %q", ErrInvalidRegistry, field, mode)
	}
}

// validateIdentKey checks that a map key matches the identifier charset
// [A-Za-z0-9_.-], mirroring the workflow stage-name rule.
func validateIdentKey(kind, key string) error {
	if key == "" {
		return fmt.Errorf("%w: %s key must not be empty", ErrInvalidRegistry, kind)
	}
	if !ident.IsValid(key) {
		return fmt.Errorf("%w: invalid %s key %q (must match [A-Za-z0-9_.-])", ErrInvalidRegistry, kind, key)
	}
	return nil
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
				Invoke:         `codex exec --ephemeral -m gpt-5.5 -c model_reasoning_effort="xhigh" -s read-only -`,
				Mode:           "diff-only",
				ModelFallbacks: []string{"gpt-5.4", "gpt-5.3", "gpt-5.2"},
			},
			"dev": {
				Provider: "anthropic/claude-opus",
				Harness:  "claude-code",
				Invoke:   "claude -p --model opus",
			},
			"gemini": {
				Provider:       "google/gemini-3.1-pro-preview",
				Harness:        "gemini-cli",
				Invoke:         "env -u GOOGLE_CLOUD_PROJECT_ID -u GOOGLE_CLOUD_PROJECT -u CLOUDSDK_CORE_PROJECT gemini --skip-trust -m gemini-3.1-pro-preview -p",
				Mode:           "inline-no-tools",
				ModelFallbacks: []string{"gemini-3-pro-preview", "gemini-3-flash-preview", "gemini-2.5-pro"},
			},
			"opus": {
				Provider: "anthropic/claude-opus",
				Harness:  "claude-code",
				Invoke:   "claude -p --model opus",
				Mode:     "inline",
				InvocationFallbacks: []SeatInvocation{{
					Harness: "agy",
					Invoke:  "agy --model opus --print",
					Mode:    "inline",
				}},
			},
		},
		Tiers: map[string]Tier{
			"L1": {
				Name:  "Full three-seat gate",
				Seats: []string{"gemini", "opus", "codex"},
				Use:   "Heaviest; reserve for the riskiest L1 surfaces or when escalating: storage format / ref-namespace / git-plumbing changes that could lose or corrupt data, or break backward compatibility.",
			},
			"L2": {
				Name:  "Strong two-seat gate",
				Seats: []string{"opus", "codex"},
				Use:   "The heavy-QA panel. Default for the PLAN and VERIFY phase gates and for any change touching product/CLI behavior, schema/format, refs/etude/*, or docs claiming NEW shipped behavior.",
			},
			"L3": {
				Name:  "Medium two-seat gate",
				Seats: []string{"opus", "codex"},
				Use:   "The IMPLEMENT-phase gate, and low-risk localized refactors / validation tightening / test strengthening on an already-gated component.",
			},
			"L4": {
				Name:  "Light single-seat gate",
				Seats: []string{"opus"},
				Use:   "DOCS and FINAL REVIEW phase gates, and changes with no shipping-code change (test-only additions or docs/planning-only changes).",
			},
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
	Provider            string               `yaml:"provider"`
	Harness             string               `yaml:"harness"`
	Invoke              string               `yaml:"invoke"`
	Mode                string               `yaml:"mode,omitempty"`
	ModelFallbacks      []string             `yaml:"model_fallbacks,omitempty"`
	InvocationFallbacks []seatInvocationYAML `yaml:"invocation_fallbacks,omitempty"`
}

type seatInvocationYAML struct {
	Harness string `yaml:"harness"`
	Invoke  string `yaml:"invoke"`
	Mode    string `yaml:"mode,omitempty"`
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
			seat := seatYAML{
				Provider:       s.Provider,
				Harness:        s.Harness,
				Invoke:         s.Invoke,
				Mode:           s.Mode,
				ModelFallbacks: s.ModelFallbacks,
			}
			if len(s.InvocationFallbacks) > 0 {
				seat.InvocationFallbacks = make([]seatInvocationYAML, len(s.InvocationFallbacks))
				for i, fallback := range s.InvocationFallbacks {
					seat.InvocationFallbacks[i] = seatInvocationYAML{
						Harness: fallback.Harness,
						Invoke:  fallback.Invoke,
						Mode:    fallback.Mode,
					}
				}
			}
			out.Seats[k] = seat
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
			seat := Seat{
				Provider:       s.Provider,
				Harness:        s.Harness,
				Invoke:         s.Invoke,
				Mode:           s.Mode,
				ModelFallbacks: s.ModelFallbacks,
			}
			if len(s.InvocationFallbacks) > 0 {
				seat.InvocationFallbacks = make([]SeatInvocation, len(s.InvocationFallbacks))
				for i, fallback := range s.InvocationFallbacks {
					seat.InvocationFallbacks[i] = SeatInvocation{
						Harness: fallback.Harness,
						Invoke:  fallback.Invoke,
						Mode:    fallback.Mode,
					}
				}
			}
			r.Seats[k] = seat
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
