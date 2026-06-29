package registry

import (
	"errors"
	"strings"
	"testing"
)

// goldenDefaultRegistryYAML is the exact byte output expected from
// Default().YAML().  This locks what etude-init-command scaffolds verbatim.
const goldenDefaultRegistryYAML = `quorum: unanimous
seats:
  codex:
    provider: openai/gpt-5.5
    harness: codex
    invoke: codex exec --ephemeral -m gpt-5.5 -s read-only -
    mode: diff-only
    model_fallbacks:
      - gpt-5.4
      - gpt-5.3
  gemini:
    provider: google/gemini-3.1-pro-preview
    harness: gemini-cli
    invoke: gemini -m gemini-3.1-pro-preview -p
    mode: inline-no-tools
    model_fallbacks:
      - gemini-3-pro-preview
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
    mode: inline
tiers:
  L1:
    name: Full three-seat gate
    seats:
      - gemini
      - opus
      - codex
  L2:
    name: Strong two-seat gate
    seats:
      - opus
      - codex
  L3:
    name: Medium two-seat gate
    seats:
      - opus
      - codex
  L4:
    name: Light single-seat gate
    seats:
      - opus
`

// TestDefaultYAMLIsDeterministicAndExact asserts exact byte output from
// Default().YAML() and that two calls return identical bytes.
func TestDefaultYAMLIsDeterministicAndExact(t *testing.T) {
	first, err := Default().YAML()
	if err != nil {
		t.Fatalf("YAML returned error: %v", err)
	}
	if string(first) != goldenDefaultRegistryYAML {
		t.Fatalf("YAML mismatch\n got:\n%s\nwant:\n%s", first, goldenDefaultRegistryYAML)
	}
	second, err := Default().YAML()
	if err != nil {
		t.Fatalf("second YAML returned error: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("YAML bytes changed between calls\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestDefaultValidatesClean confirms the Default registry passes Validate.
func TestDefaultValidatesClean(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("Default().Validate() returned error: %v", err)
	}
}

// TestParseYAMLRoundTripsDefault verifies Default().YAML() parses back equal.
func TestParseYAMLRoundTripsDefault(t *testing.T) {
	b, err := Default().YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	got, err := ParseYAML(b)
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	want := Default()
	if got.EffectiveQuorum() != want.EffectiveQuorum() {
		t.Fatalf("Quorum mismatch: got %q want %q", got.EffectiveQuorum(), want.EffectiveQuorum())
	}
	if len(got.Seats) != len(want.Seats) {
		t.Fatalf("Seats count: got %d want %d", len(got.Seats), len(want.Seats))
	}
	if len(got.Tiers) != len(want.Tiers) {
		t.Fatalf("Tiers count: got %d want %d", len(got.Tiers), len(want.Tiers))
	}
	// Spot-check a seat.
	opusGot, ok := got.Seats["opus"]
	if !ok {
		t.Fatal("opus seat missing after round-trip")
	}
	opusWant := want.Seats["opus"]
	if opusGot.Provider != opusWant.Provider || opusGot.Harness != opusWant.Harness {
		t.Fatalf("opus seat mismatch: got %+v want %+v", opusGot, opusWant)
	}
	// Round-trip bytes must be identical.
	b2, err := got.YAML()
	if err != nil {
		t.Fatalf("re-encode error: %v", err)
	}
	if string(b2) != string(b) {
		t.Fatalf("bytes differ after parse→encode\nfirst:\n%s\nre-encode:\n%s", b, b2)
	}
}

// TestParseYAMLSeats asserts seats with all fields including model_fallbacks
// are parsed correctly.
func TestParseYAMLSeats(t *testing.T) {
	input := `quorum: unanimous
seats:
  codex:
    provider: openai/gpt-5.5
    harness: codex
    invoke: 'codex exec --ephemeral -m gpt-5.5 -s read-only -'
    mode: diff-only
    model_fallbacks:
      - gpt-5.4
      - gpt-5.3
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
    mode: inline
tiers:
  L1:
    name: Full gate
    seats:
      - opus
      - codex
`
	r, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	codex, ok := r.Seats["codex"]
	if !ok {
		t.Fatal("codex seat missing")
	}
	if codex.Provider != "openai/gpt-5.5" {
		t.Fatalf("codex provider = %q", codex.Provider)
	}
	if len(codex.ModelFallbacks) != 2 || codex.ModelFallbacks[0] != "gpt-5.4" {
		t.Fatalf("codex model_fallbacks = %v", codex.ModelFallbacks)
	}
	opus, ok := r.Seats["opus"]
	if !ok {
		t.Fatal("opus seat missing")
	}
	if opus.Mode != "inline" {
		t.Fatalf("opus mode = %q, want %q", opus.Mode, "inline")
	}
}

// TestParseYAMLTiers asserts tiers including optional name and use are parsed.
func TestParseYAMLTiers(t *testing.T) {
	input := `seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    name: Full three-seat gate
    seats:
      - opus
    use: Reserve for the riskiest changes.
  L4:
    seats:
      - opus
`
	r, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	l1, ok := r.Tiers["L1"]
	if !ok {
		t.Fatal("L1 tier missing")
	}
	if l1.Name != "Full three-seat gate" {
		t.Fatalf("L1 name = %q", l1.Name)
	}
	if l1.Use != "Reserve for the riskiest changes." {
		t.Fatalf("L1 use = %q", l1.Use)
	}
	if len(l1.Seats) != 1 || l1.Seats[0] != "opus" {
		t.Fatalf("L1 seats = %v", l1.Seats)
	}
	l4, ok := r.Tiers["L4"]
	if !ok {
		t.Fatal("L4 tier missing")
	}
	if l4.Name != "" {
		t.Fatalf("L4 name should be empty, got %q", l4.Name)
	}
}

// TestParseYAMLQuorum verifies quorum field parsing and EffectiveQuorum.
func TestParseYAMLQuorum(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantQ   string
		wantEff string
	}{
		{"absent quorum defaults unanimous", `seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`, "", "unanimous"},
		{"explicit unanimous", `quorum: unanimous
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`, "unanimous", "unanimous"},
		{"majority", `quorum: majority
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`, "majority", "majority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ParseYAML([]byte(tc.input))
			if err != nil {
				t.Fatalf("ParseYAML error: %v", err)
			}
			if r.Quorum != tc.wantQ {
				t.Fatalf("Quorum = %q, want %q", r.Quorum, tc.wantQ)
			}
			if r.EffectiveQuorum() != tc.wantEff {
				t.Fatalf("EffectiveQuorum() = %q, want %q", r.EffectiveQuorum(), tc.wantEff)
			}
		})
	}
}

// TestValidateRejectsBogusQuorum asserts unknown quorum value is rejected.
func TestValidateRejectsBogusQuorum(t *testing.T) {
	r := Default()
	r.Quorum = "supermajority"
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject bogus quorum")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestValidateRejectsTierWithUndefinedSeat asserts that a tier referencing a
// seat key that does not exist in Seats is rejected.
func TestValidateRejectsTierWithUndefinedSeat(t *testing.T) {
	r := Default()
	r.Tiers["L1"] = Tier{Seats: []string{"ghost", "opus", "codex"}}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject tier referencing undefined seat")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestValidateRejectsTierWithEmptySeats asserts that a tier with an empty
// seats list is rejected.
func TestValidateRejectsTierWithEmptySeats(t *testing.T) {
	r := Default()
	r.Tiers["L1"] = Tier{Seats: []string{}}
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject tier with empty seats")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestValidateRejectsSeatMissingProvider asserts a seat without provider is
// rejected.
func TestValidateRejectsSeatMissingProvider(t *testing.T) {
	r := Default()
	s := r.Seats["opus"]
	s.Provider = ""
	r.Seats["opus"] = s
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject seat with empty provider")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestValidateRejectsSeatMissingHarness asserts a seat without harness is
// rejected.
func TestValidateRejectsSeatMissingHarness(t *testing.T) {
	r := Default()
	s := r.Seats["opus"]
	s.Harness = ""
	r.Seats["opus"] = s
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject seat with empty harness")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestValidateRejectsSeatMissingInvoke asserts a seat without invoke is rejected.
func TestValidateRejectsSeatMissingInvoke(t *testing.T) {
	r := Default()
	s := r.Seats["opus"]
	s.Invoke = ""
	r.Seats["opus"] = s
	if err := r.Validate(); err == nil {
		t.Fatal("Validate should reject seat with empty invoke")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestKnownFieldsRejectsUnknownTopLevel asserts KnownFields(true) at the top
// level of the registry document.
func TestKnownFieldsRejectsUnknownTopLevel(t *testing.T) {
	input := `quorum: unanimous
unknown_key: surprise
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown top-level key")
	}
	if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestKnownFieldsRejectsUnknownInSeat asserts KnownFields(true) catches an
// unknown key inside a seat entry.
func TestKnownFieldsRejectsUnknownInSeat(t *testing.T) {
	input := `seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
    surprise: oops
tiers:
  L1:
    seats: [opus]
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside seat")
	}
	if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestKnownFieldsRejectsUnknownInTier asserts KnownFields(true) catches an
// unknown key inside a tier entry.
func TestKnownFieldsRejectsUnknownInTier(t *testing.T) {
	input := `seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
    color: red
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside tier")
	}
	if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestTrailingDocumentRejected asserts that a second YAML document after the
// registry is rejected, mirroring workflow.ParseYAML's ensureEOF behaviour.
func TestTrailingDocumentRejected(t *testing.T) {
	base := `quorum: unanimous
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`
	cases := map[string]string{
		"second document":  base + "---\nquorum: majority\n",
		"empty second doc": base + "---\n",
		"trailing scalar":  base + "---\ngarbage\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseYAML([]byte(input))
			if err == nil {
				t.Fatal("ParseYAML should reject trailing document")
			}
			if !errors.Is(err, ErrInvalidRegistry) {
				t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
			}
		})
	}
}

// TestYAMLRejectsInvalidRegistry asserts YAML() propagates Validate errors.
func TestYAMLRejectsInvalidRegistry(t *testing.T) {
	r := Default()
	r.Quorum = "bogus"
	if _, err := r.YAML(); err == nil {
		t.Fatal("YAML() should return error for invalid registry")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestMajorityQuorumOK asserts "majority" is a valid quorum value.
func TestMajorityQuorumOK(t *testing.T) {
	r := Default()
	r.Quorum = "majority"
	if err := r.Validate(); err != nil {
		t.Fatalf("majority quorum should be valid: %v", err)
	}
}

// TestEmptyQuorumDefaultsUnanimous asserts empty Quorum passes Validate and
// EffectiveQuorum returns "unanimous".
func TestEmptyQuorumDefaultsUnanimous(t *testing.T) {
	r := Default()
	r.Quorum = ""
	if err := r.Validate(); err != nil {
		t.Fatalf("empty quorum should be valid (defaults to unanimous): %v", err)
	}
	if got := r.EffectiveQuorum(); got != "unanimous" {
		t.Fatalf("EffectiveQuorum() = %q, want %q", got, "unanimous")
	}
}

// TestRegistryRoundTripByteStable asserts a minimal hand-crafted registry
// round-trips with exactly stable bytes.
func TestRegistryRoundTripByteStable(t *testing.T) {
	input := `seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
    mode: inline
tiers:
  L4:
    name: Light single-seat gate
    seats:
      - opus
`
	r1, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	b1, err := r1.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	if string(b1) != input {
		t.Fatalf("round-trip not byte-stable\ngot:\n%s\nwant:\n%s", b1, input)
	}
	r2, err := ParseYAML(b1)
	if err != nil {
		t.Fatalf("second ParseYAML error: %v", err)
	}
	b2, err := r2.YAML()
	if err != nil {
		t.Fatalf("second YAML error: %v", err)
	}
	if string(b2) != string(b1) {
		t.Fatalf("second round-trip bytes differ:\nfirst:\n%s\nsecond:\n%s", b1, b2)
	}
}

// TestParseYAMLRejectsDuplicateMappingKeys locks yaml.v3 behavior.
func TestParseYAMLRejectsDuplicateMappingKeys(t *testing.T) {
	input := `quorum: unanimous
quorum: majority
seats:
  opus:
    provider: anthropic/claude-opus
    harness: claude-code
    invoke: claude -p --model opus
tiers:
  L1:
    seats: [opus]
`
	if _, err := ParseYAML([]byte(input)); err == nil {
		t.Fatal("ParseYAML should reject duplicate mapping keys")
	} else if !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("error does not wrap ErrInvalidRegistry: %v", err)
	}
}

// TestEffectiveQuorumField verifies the EffectiveQuorum accessor for all variants.
func TestEffectiveQuorumField(t *testing.T) {
	cases := []struct {
		quorum string
		want   string
	}{
		{"", "unanimous"},
		{"unanimous", "unanimous"},
		{"majority", "majority"},
	}
	for _, tc := range cases {
		r := Registry{Quorum: tc.quorum}
		if got := r.EffectiveQuorum(); got != tc.want {
			t.Errorf("EffectiveQuorum(%q) = %q, want %q", tc.quorum, got, tc.want)
		}
	}
}

// TestParseYAMLRequiresNoMandatoryFields asserts that an empty registry (no
// quorum/seats/tiers) is valid (all fields optional at the top level).
func TestParseYAMLRequiresNoMandatoryFields(t *testing.T) {
	input := `{}`
	r, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error for empty registry: %v", err)
	}
	if r.EffectiveQuorum() != "unanimous" {
		t.Fatalf("EffectiveQuorum() = %q, want %q", r.EffectiveQuorum(), "unanimous")
	}
}

// TestDefaultYAMLSelfChecks asserts Default().YAML() can be re-parsed and the
// result passes Validate without error (self-check mirrors init.go behavior).
func TestDefaultYAMLSelfChecks(t *testing.T) {
	b, err := Default().YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	r, err := ParseYAML(b)
	if err != nil {
		t.Fatalf("ParseYAML self-check error: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("Validate after self-check error: %v", err)
	}
	// Verify the output contains all expected seat keys.
	for _, key := range []string{"codex", "gemini", "opus"} {
		if !strings.Contains(string(b), key+":") {
			t.Fatalf("YAML output missing seat %q:\n%s", key, b)
		}
	}
}
