package workflow

import (
	"errors"
	"strings"
	"testing"
)

// ptrFloat64 returns a pointer to the given float64 value — helper for test data.
func ptrFloat64(f float64) *float64 { return &f }

// goldenDefaultYAML is the exact byte output expected from Default().YAML().
// This locks what etude-init-command scaffolds verbatim.
const goldenDefaultYAML = `name: default
stages:
  - name: plan
    produces: plan
    inputs:
      - task
    skill: dev-planner
    eval:
      method: rubric
      rubric: evals/plan-rubric.md
  - name: implement
    produces: diff
    inputs:
      - plan
      - repo-state
    skill: dev-executor
  - name: verify
    produces: verify
    inputs:
      - plan
      - diff
    skill: dev-qa
    eval:
      method: rubric
      rubric: evals/verify-rubric.md
  - name: docs
    produces: docs-diff
    inputs:
      - diff
    skill: dev-docs-writer
    optional: true
  - name: review
    produces: review
    inputs:
      - diff
      - plan
      - verify
    skill: dev-pr-reviewer
    eval:
      method: pairwise
`

// TestDefaultYAMLIsDeterministicAndExact asserts exact byte output from
// Default().YAML() — the golden-bytes test that locks what init scaffolds.
func TestDefaultYAMLIsDeterministicAndExact(t *testing.T) {
	first, err := Default().YAML()
	if err != nil {
		t.Fatalf("YAML returned error: %v", err)
	}
	if string(first) != goldenDefaultYAML {
		t.Fatalf("YAML mismatch\n got:\n%s\nwant:\n%s", first, goldenDefaultYAML)
	}
	second, err := Default().YAML()
	if err != nil {
		t.Fatalf("second YAML returned error: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("YAML bytes changed between calls\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestDefaultValidatesClean confirms the Default workflow passes Validate.
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
	if got.Name != want.Name {
		t.Fatalf("Name mismatch: got %q want %q", got.Name, want.Name)
	}
	if len(got.Stages) != len(want.Stages) {
		t.Fatalf("Stages count: got %d want %d", len(got.Stages), len(want.Stages))
	}
	for i, gs := range got.Stages {
		ws := want.Stages[i]
		if gs.Name != ws.Name || gs.Produces != ws.Produces || gs.Skill != ws.Skill || gs.Optional != ws.Optional {
			t.Fatalf("stage[%d] mismatch: got %+v want %+v", i, gs, ws)
		}
		if strings.Join(gs.Inputs, ",") != strings.Join(ws.Inputs, ",") {
			t.Fatalf("stage[%d] inputs mismatch: got %v want %v", i, gs.Inputs, ws.Inputs)
		}
		if (gs.Eval == nil) != (ws.Eval == nil) {
			t.Fatalf("stage[%d] eval nil mismatch", i)
		}
		if gs.Eval != nil && (gs.Eval.Method != ws.Eval.Method || gs.Eval.Rubric != ws.Eval.Rubric) {
			t.Fatalf("stage[%d] eval mismatch: got %+v want %+v", i, gs.Eval, ws.Eval)
		}
	}
}

// TestParseYAMLRejectsUnknownFields verifies KnownFields(true) is active.
func TestParseYAMLRejectsUnknownFields(t *testing.T) {
	input := `
name: default
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    unknown_field: surprise
`
	if _, err := ParseYAML([]byte(input)); err == nil {
		t.Fatal("ParseYAML should reject unknown field")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestParseYAMLRejectsTrailingDocuments ensures only a single YAML document is
// accepted — a second document or trailing data is rejected, mirroring
// runmanifest.ParseJSON's EOF strictness.
func TestParseYAMLRejectsTrailingDocuments(t *testing.T) {
	valid := `name: simple
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	cases := map[string]string{
		"second document":  valid + "---\nname: other\nstages:\n  - name: x\n    produces: x\n    skill: s\n",
		"empty second doc": valid + "---\n",
		"trailing scalar":  valid + "---\ngarbage\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseYAML([]byte(input)); err == nil {
				t.Fatal("ParseYAML should reject trailing document/data")
			} else if !errors.Is(err, ErrInvalidWorkflow) {
				t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
			}
		})
	}
}

// TestParseYAMLRejectsTopLevelUnknownFields ensures KnownFields catches top-level.
func TestParseYAMLRejectsTopLevelUnknownFields(t *testing.T) {
	input := `
name: default
extra: true
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	if _, err := ParseYAML([]byte(input)); err == nil {
		t.Fatal("ParseYAML should reject top-level unknown field")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestValidateRejectsInvalidWorkflows is the main table-driven rejection suite.
func TestValidateRejectsInvalidWorkflows(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Workflow)
	}{
		// Top-level rules
		{"empty name", func(w *Workflow) { w.Name = "" }},
		{"whitespace name", func(w *Workflow) { w.Name = "   " }},
		{"invalid name chars", func(w *Workflow) { w.Name = "My Workflow" }},
		{"no stages", func(w *Workflow) { w.Stages = nil }},

		// Per-stage required fields
		{"empty stage name", func(w *Workflow) { w.Stages[0].Name = "" }},
		{"whitespace stage name", func(w *Workflow) { w.Stages[0].Name = " " }},
		{"empty produces", func(w *Workflow) { w.Stages[0].Produces = "" }},
		{"whitespace produces", func(w *Workflow) { w.Stages[0].Produces = "  " }},
		{"empty skill", func(w *Workflow) { w.Stages[0].Skill = "" }},
		{"whitespace skill", func(w *Workflow) { w.Stages[0].Skill = "  " }},

		// Uniqueness
		{"duplicate stage name", func(w *Workflow) {
			w.Stages = append(w.Stages, Stage{Name: w.Stages[0].Name, Produces: "other", Skill: "s"})
		}},
		{"duplicate produces role", func(w *Workflow) {
			w.Stages = append(w.Stages, Stage{Name: "other", Produces: w.Stages[0].Produces, Skill: "s"})
		}},

		// Input role resolution
		{"input unknown role", func(w *Workflow) { w.Stages[0].Inputs = []string{"unknown-role"} }},
		{"input forward reference", func(w *Workflow) {
			// stage[0] consumes "diff", which only the later stage[1] produces.
			w.Stages = append(w.Stages, Stage{Name: "impl", Produces: "diff", Skill: "s"})
			w.Stages[0].Inputs = []string{"diff"}
		}},
		{"self-reference in inputs", func(w *Workflow) {
			// Stage lists its own produces in inputs — must be rejected because
			// the role is not registered until after inputs are validated.
			w.Stages[0].Inputs = []string{w.Stages[0].Produces}
		}},
		{"duplicate input role within stage", func(w *Workflow) {
			// Two identical entries in a single stage's inputs is a copy-paste
			// error; we reject it rather than silently tolerate it.
			w.Stages[0].Inputs = []string{"task", "task"}
		}},
		// validateRoleToken accepts the empty string (its rune loop finds nothing
		// to reject), so the explicit empty/whitespace guard is the sole rejector
		// of a blank input role — exercise it directly.
		{"empty input role", func(w *Workflow) { w.Stages[0].Inputs = []string{""} }},
		{"whitespace input role", func(w *Workflow) { w.Stages[0].Inputs = []string{"  "} }},

		// Eval method rules
		{"bad eval method", func(w *Workflow) { w.Stages[0].Eval = &Eval{Method: "guess"} }},
		{"rubric method missing path", func(w *Workflow) {
			w.Stages[0].Eval = &Eval{Method: "rubric", Rubric: ""}
		}},
		{"rubric method whitespace path", func(w *Workflow) {
			w.Stages[0].Eval = &Eval{Method: "rubric", Rubric: "  "}
		}},
		{"pairwise with rubric path", func(w *Workflow) {
			w.Stages[0].Eval = &Eval{Method: "pairwise", Rubric: "evals/rubric.md"}
		}},
		{"assertion with rubric path", func(w *Workflow) {
			w.Stages[0].Eval = &Eval{Method: "assertion", Rubric: "evals/rubric.md"}
		}},

		// Role charset
		{"produces with uppercase", func(w *Workflow) { w.Stages[0].Produces = "Plan" }},
		{"produces with dot", func(w *Workflow) { w.Stages[0].Produces = "plan.v1" }},
		{"produces with space", func(w *Workflow) { w.Stages[0].Produces = "my plan" }},

		// Stage-name charset (broader [A-Za-z0-9_.-] than roles, but still bounded)
		{"stage name with space", func(w *Workflow) { w.Stages[0].Name = "my stage" }},
		{"stage name with slash", func(w *Workflow) { w.Stages[0].Name = "a/b" }},

		// Reserved produces roles
		{"produces reserved role task", func(w *Workflow) { w.Stages[0].Produces = "task" }},
		{"produces reserved role repo-state", func(w *Workflow) { w.Stages[0].Produces = "repo-state" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := minimalWorkflow()
			tc.edit(&w)
			if err := w.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			} else if !errors.Is(err, ErrInvalidWorkflow) {
				t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
			}
		})
	}
}

// TestValidateRejectsReservedProducesRole is a focused test that checks the
// "reserved" guard specifically — the table harness only asserts err != nil
// + errors.Is(ErrInvalidWorkflow); this test also asserts the error message
// contains "reserved", catching a future regression where another guard
// shadows this one.
func TestValidateRejectsReservedProducesRole(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Workflow)
	}{
		{"first stage produces task", func(w *Workflow) { w.Stages[0].Produces = "task" }},
		{"first stage produces repo-state", func(w *Workflow) { w.Stages[0].Produces = "repo-state" }},
		{"second stage produces task", func(w *Workflow) {
			w.Stages = append(w.Stages, Stage{Name: "impl", Produces: "task", Skill: "s", Inputs: []string{"task"}})
		}},
		{"second stage produces repo-state", func(w *Workflow) {
			w.Stages = append(w.Stages, Stage{Name: "impl", Produces: "repo-state", Skill: "s", Inputs: []string{"task"}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := minimalWorkflow()
			tc.edit(&w)
			err := w.Validate()
			if err == nil {
				t.Fatal("Validate returned nil error")
			}
			if !errors.Is(err, ErrInvalidWorkflow) {
				t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
			}
			if !strings.Contains(err.Error(), "reserved") {
				t.Fatalf("error does not contain 'reserved': %v", err)
			}
		})
	}
}

// TestValidateAcceptsValidWorkflows covers acceptance cases.
func TestValidateAcceptsValidWorkflows(t *testing.T) {
	cases := []struct {
		name     string
		workflow Workflow
	}{
		{
			"minimal single stage no inputs no eval",
			Workflow{
				Name: "simple",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s"},
				},
			},
		},
		{
			"stage with special role task input",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s", Inputs: []string{"task"}},
				},
			},
		},
		{
			"stage with special role repo-state input",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "impl", Produces: "diff", Skill: "s", Inputs: []string{"repo-state"}},
				},
			},
		},
		{
			"stage with both special roles",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "impl", Produces: "diff", Skill: "s", Inputs: []string{"task", "repo-state"}},
				},
			},
		},
		{
			"optional stage",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "docs", Produces: "docs-diff", Skill: "s", Optional: true},
				},
			},
		},
		{
			"stage with no inputs (nil)",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s", Inputs: nil},
				},
			},
		},
		{
			"stage with no eval (nil)",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s", Eval: nil},
				},
			},
		},
		{
			"rubric eval with path",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s", Eval: &Eval{Method: "rubric", Rubric: "evals/rubric.md"}},
				},
			},
		},
		{
			"pairwise eval no rubric",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "review", Produces: "review", Skill: "s", Eval: &Eval{Method: "pairwise"}},
				},
			},
		},
		{
			"assertion eval no rubric",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "check", Produces: "check", Skill: "s", Eval: &Eval{Method: "assertion"}},
				},
			},
		},
		{
			"multi-stage with ordered references",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "plan", Produces: "plan", Skill: "s", Inputs: []string{"task"}},
					{Name: "impl", Produces: "diff", Skill: "s", Inputs: []string{"plan", "repo-state"}},
					{Name: "review", Produces: "review", Skill: "s", Inputs: []string{"diff", "plan"}},
				},
			},
		},
		{
			"custom workflow with reordered and dropped stages",
			Workflow{
				Name: "custom",
				Stages: []Stage{
					{Name: "impl", Produces: "diff", Skill: "s", Inputs: []string{"task", "repo-state"}},
					{Name: "review", Produces: "review", Skill: "s", Inputs: []string{"diff"}},
				},
			},
		},
		{
			"stage name with underscore and dot allowed",
			Workflow{
				Name: "w",
				Stages: []Stage{
					{Name: "manual_test.v2", Produces: "result", Skill: "s"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.workflow.Validate(); err != nil {
				t.Fatalf("Validate returned error: %v", err)
			}
		})
	}
}

// TestParseYAMLAcceptsOmittedInputsAndEval ensures omitting inputs and eval
// fields in YAML results in nil slices/pointers, not errors.
func TestParseYAMLAcceptsOmittedInputsAndEval(t *testing.T) {
	input := `
name: simple
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Inputs != nil {
		t.Fatalf("expected nil inputs, got %v", w.Stages[0].Inputs)
	}
	if w.Stages[0].Eval != nil {
		t.Fatalf("expected nil eval, got %v", w.Stages[0].Eval)
	}
}

// TestParseYAMLAcceptsExplicitEmptyInputs ensures an explicit empty inputs list
// (`inputs: []`) is accepted just like an omitted one. yaml.v3 decodes `[]` to a
// non-nil empty slice; Validate must treat zero-length inputs as valid, and the
// encoder's omitempty must drop it on the way back out.
func TestParseYAMLAcceptsExplicitEmptyInputs(t *testing.T) {
	input := `name: simple
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    inputs: []
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if len(w.Stages[0].Inputs) != 0 {
		t.Fatalf("expected zero-length inputs, got %v", w.Stages[0].Inputs)
	}
	// Re-encoding must drop the empty list (omitempty), matching the
	// omitted-inputs form so the two are indistinguishable on disk.
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	if strings.Contains(string(out), "inputs") {
		t.Fatalf("empty inputs should be omitted on encode, got:\n%s", out)
	}
}

// TestParseYAMLRejectsDuplicateMappingKeys locks the yaml.v3 behaviour of
// rejecting a repeated mapping key (e.g. two `name:` keys) and confirms the
// error is wrapped in ErrInvalidWorkflow rather than leaking a raw yaml error.
func TestParseYAMLRejectsDuplicateMappingKeys(t *testing.T) {
	input := `name: one
name: two
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	if _, err := ParseYAML([]byte(input)); err == nil {
		t.Fatal("ParseYAML should reject duplicate mapping keys")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestParseYAMLCustomWorkflow verifies a non-default workflow round-trips cleanly.
func TestParseYAMLCustomWorkflow(t *testing.T) {
	input := `
name: custom
stages:
  - name: impl
    produces: diff
    inputs:
      - task
      - repo-state
    skill: dev-coder
  - name: review
    produces: review
    inputs:
      - diff
    skill: dev-reviewer
    optional: true
    eval:
      method: pairwise
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Name != "custom" {
		t.Fatalf("Name = %q", w.Name)
	}
	if len(w.Stages) != 2 {
		t.Fatalf("Stages count = %d", len(w.Stages))
	}
	if !w.Stages[1].Optional {
		t.Fatal("stage[1] should be optional")
	}
	if w.Stages[1].Eval == nil || w.Stages[1].Eval.Method != "pairwise" {
		t.Fatalf("stage[1] eval = %v", w.Stages[1].Eval)
	}
}

// TestYAMLRejectsInvalidWorkflow checks that YAML() propagates Validate errors.
func TestYAMLRejectsInvalidWorkflow(t *testing.T) {
	w := Workflow{Name: "", Stages: nil}
	if _, err := w.YAML(); err == nil {
		t.Fatal("YAML should return error for invalid workflow")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestYAMLRoundTripsCustomWorkflow exercises the encoder on a NON-default shape
// (optional stage, pairwise eval, multi-stage refs): encode -> parse -> encode
// must be stable, confirming YAML() handles custom workflows, not just Default().
func TestYAMLRoundTripsCustomWorkflow(t *testing.T) {
	want := Workflow{
		Name: "custom",
		Stages: []Stage{
			{Name: "impl", Produces: "diff", Skill: "dev-coder", Inputs: []string{"task", "repo-state"}},
			{Name: "review", Produces: "review", Skill: "dev-reviewer", Inputs: []string{"diff"}, Optional: true, Eval: &Eval{Method: "pairwise"}},
		},
	}
	first, err := want.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	got, err := ParseYAML(first)
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	second, err := got.YAML()
	if err != nil {
		t.Fatalf("re-encode error: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("custom workflow not stable across encode/parse/encode\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestParseYAMLCoercesOptionalBool documents (and regression-guards) yaml.v3's
// YAML 1.1 boolean leniency: a quoted `optional: "yes"` is coerced to true. This
// is lenient but well-formed; the test pins the behavior so a future yaml lib
// change is caught rather than silently altering schema semantics.
func TestParseYAMLCoercesOptionalBool(t *testing.T) {
	input := `name: w
stages:
  - name: docs
    produces: docs-diff
    skill: dev-docs-writer
    optional: "yes"
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if !w.Stages[0].Optional {
		t.Fatal(`optional: "yes" should coerce to true`)
	}
}

// ptrBool returns a pointer to the given bool value — helper for test data.
func ptrBool(b bool) *bool { return &b }

// ptrInt returns a pointer to the given int value — helper for test data.
func ptrInt(i int) *int { return &i }

// minimalRetrosYAML is the smallest YAML snippet that includes a valid retros
// block (on_run_close is true by default so a generator is required).
const minimalRetrosYAML = `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
`

// ---- retros: block tests -----------------------------------------------

// TestRetrosAbsentBlockNilAndDefaults asserts that a workflow without a retros:
// block leaves Retros nil and returns sensible defaults from all accessors.
func TestRetrosAbsentBlockNilAndDefaults(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Retros != nil {
		t.Fatalf("expected Retros == nil for absent block, got %+v", w.Retros)
	}
	if !w.OnRunCloseEnabled() {
		t.Fatal("OnRunCloseEnabled() should return true when block absent")
	}
	if w.OnRepeatedGateBlockEnabled() {
		t.Fatal("OnRepeatedGateBlockEnabled() should return false when block absent")
	}
	if w.OnFailedVerifyEnabled() {
		t.Fatal("OnFailedVerifyEnabled() should return false when block absent")
	}
	if w.OnBlockedStateEnabled() {
		t.Fatal("OnBlockedStateEnabled() should return false when block absent")
	}
	if w.PostBenchEnabled() {
		t.Fatal("PostBenchEnabled() should return false when block absent")
	}
	if w.RetroGenerator() != "" {
		t.Fatalf("RetroGenerator() should be empty when block absent, got %q", w.RetroGenerator())
	}
}

// TestRetrosAbsentBlockLegacyRoundTrip asserts that ParseYAML(legacy).YAML()
// emits NO retros: block — the round-trip is byte-stable.
func TestRetrosAbsentBlockLegacyRoundTrip(t *testing.T) {
	legacy := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(legacy))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	if strings.Contains(string(out), "retros") {
		t.Fatalf("re-encoded legacy workflow should not contain 'retros', got:\n%s", out)
	}
}

// TestRetrosPresentBlockOmittedOnRunClose checks that a present block with
// on_run_close omitted keeps Retros != nil AND defaults on_run_close to true.
func TestRetrosPresentBlockOmittedOnRunClose(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Retros == nil {
		t.Fatal("expected Retros != nil for present block")
	}
	if !w.OnRunCloseEnabled() {
		t.Fatal("OnRunCloseEnabled() should default true when block present but on_run_close omitted")
	}
}

// TestRetrosExplicitOnRunCloseFalse asserts an explicit on_run_close: false is
// honored.
func TestRetrosExplicitOnRunCloseFalse(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.OnRunCloseEnabled() {
		t.Fatal("OnRunCloseEnabled() should return false when explicitly set to false")
	}
}

// TestRetrosAutomatedTriggerWithoutGeneratorErrors asserts Validate returns
// ErrInvalidWorkflow when an automated trigger is enabled but no generator is set.
func TestRetrosAutomatedTriggerWithoutGeneratorErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{
			"on_run_close default true no generator",
			`name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros: {}
`,
		},
		{
			"post_bench true no generator",
			`name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  post_bench: true
`,
		},
		{
			"on_failed_verify true no generator",
			`name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_failed_verify: true
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseYAML([]byte(tc.input))
			if err == nil {
				t.Fatal("ParseYAML should return error when automated trigger enabled with no generator")
			}
			if !errors.Is(err, ErrInvalidWorkflow) {
				t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
			}
		})
	}
}

// TestRetrosAutomatedTriggerWithGeneratorOk asserts a present block with an
// automated trigger and a generator passes Validate.
func TestRetrosAutomatedTriggerWithGeneratorOk(t *testing.T) {
	w, err := ParseYAML([]byte(minimalRetrosYAML))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate error: %v", err)
	}
}

// TestRetrosAbsentBlockNeverErrors asserts a workflow WITHOUT a retros: block
// never triggers the generator-required error even though on_run_close is
// effectively true — validation only applies when the block is present.
func TestRetrosAbsentBlockNeverErrors(t *testing.T) {
	w := minimalWorkflow()
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate returned error for workflow without retros block: %v", err)
	}
}

// TestRetrosRepeatedGateBlockThresholdDefaults asserts that when
// on_repeated_gate_block is enabled with no threshold, the accessor returns 3
// and Validate passes.
func TestRetrosRepeatedGateBlockThresholdDefaults(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
  on_repeated_gate_block:
    enabled: true
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.RepeatedGateBlockThreshold() != 3 {
		t.Fatalf("expected default threshold 3, got %d", w.RepeatedGateBlockThreshold())
	}
}

// TestRetrosRepeatedGateBlockThresholdZeroErrors asserts threshold: 0 is
// rejected.
func TestRetrosRepeatedGateBlockThresholdZeroErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
  on_repeated_gate_block:
    enabled: true
    threshold: 0
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject threshold: 0")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosRepeatedGateBlockThresholdValidWhenPositive asserts threshold >= 1
// passes Validate.
func TestRetrosRepeatedGateBlockThresholdValidWhenPositive(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
  on_repeated_gate_block:
    enabled: true
    threshold: 5
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.RepeatedGateBlockThreshold() != 5 {
		t.Fatalf("expected threshold 5, got %d", w.RepeatedGateBlockThreshold())
	}
}

// TestRetrosKnownFieldsRejectsUnknownInRetros asserts that an unknown key
// inside retros: is rejected by KnownFields.
func TestRetrosKnownFieldsRejectsUnknownInRetros(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
  unknown_key: oops
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside retros:")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosKnownFieldsRejectsUnknownInRepeatedGateBlock asserts that an
// unknown key inside on_repeated_gate_block: is rejected by KnownFields.
func TestRetrosKnownFieldsRejectsUnknownInRepeatedGateBlock(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  generator: ./retro.sh
  on_repeated_gate_block:
    enabled: true
    unknown_key: oops
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside on_repeated_gate_block:")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosFullBlockRoundTrip parses a fully-specified retros block, re-encodes
// it, and parses again — all three must be equal (parse→encode→parse stable).
func TestRetrosFullBlockRoundTrip(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  on_repeated_gate_block:
    enabled: true
    threshold: 5
  on_failed_verify: true
  on_blocked_state: true
  post_bench: true
  generator: ./retro.sh
`
	w1, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("first ParseYAML error: %v", err)
	}
	encoded, err := w1.YAML()
	if err != nil {
		t.Fatalf("YAML encode error: %v", err)
	}
	w2, err := ParseYAML(encoded)
	if err != nil {
		t.Fatalf("second ParseYAML error: %v", err)
	}
	// Structural equality of the retros block.
	if w1.Retros == nil || w2.Retros == nil {
		t.Fatal("expected both Retros to be non-nil")
	}
	if w1.OnRunCloseEnabled() != w2.OnRunCloseEnabled() {
		t.Fatalf("OnRunCloseEnabled mismatch: %v vs %v", w1.OnRunCloseEnabled(), w2.OnRunCloseEnabled())
	}
	if w1.OnRepeatedGateBlockEnabled() != w2.OnRepeatedGateBlockEnabled() {
		t.Fatalf("OnRepeatedGateBlockEnabled mismatch")
	}
	if w1.RepeatedGateBlockThreshold() != w2.RepeatedGateBlockThreshold() {
		t.Fatalf("RepeatedGateBlockThreshold mismatch: %d vs %d", w1.RepeatedGateBlockThreshold(), w2.RepeatedGateBlockThreshold())
	}
	if w1.OnFailedVerifyEnabled() != w2.OnFailedVerifyEnabled() {
		t.Fatalf("OnFailedVerifyEnabled mismatch")
	}
	if w1.OnBlockedStateEnabled() != w2.OnBlockedStateEnabled() {
		t.Fatalf("OnBlockedStateEnabled mismatch")
	}
	if w1.PostBenchEnabled() != w2.PostBenchEnabled() {
		t.Fatalf("PostBenchEnabled mismatch")
	}
	if w1.RetroGenerator() != w2.RetroGenerator() {
		t.Fatalf("RetroGenerator mismatch: %q vs %q", w1.RetroGenerator(), w2.RetroGenerator())
	}
	// Byte stability: re-encoding the second parse must equal the first encode.
	reEncoded, err := w2.YAML()
	if err != nil {
		t.Fatalf("re-encode error: %v", err)
	}
	if string(reEncoded) != string(encoded) {
		t.Fatalf("round-trip bytes differ\nfirst encode:\n%s\nre-encode:\n%s", encoded, reEncoded)
	}
}

// TestRetrosManualOnlyNoGeneratorRequired asserts that a present block with all
// automated triggers explicitly off does NOT require a generator.
func TestRetrosManualOnlyNoGeneratorRequired(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  on_failed_verify: false
  on_blocked_state: false
  post_bench: false
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate should pass with all triggers off and no generator: %v", err)
	}
}

// ---- present-null / present-absent distinction tests -----------------------
// These tests regression-guard the fix for the codex-identified bug where a
// bare/null retros: key was silently treated as absent (Retros == nil).

// TestRetrosPresentNullBareKeyIsPresent asserts that a bare `retros:` key
// (which yaml.v3 decodes as a !!null scalar) is treated as PRESENT, not
// absent.  Because on_run_close defaults to true and no generator is provided,
// Validate must return ErrInvalidWorkflow.
func TestRetrosPresentNullBareKeyIsPresent(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should return error: present-null retros has on_run_close=true default but no generator")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosPresentNullExplicitNullIsPresent asserts that `retros: null` is
// treated as PRESENT (same as bare retros:), not absent.  Validate must error
// for the same reason as above.
func TestRetrosPresentNullExplicitNullIsPresent(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros: null
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should return error: retros: null is present, on_run_close=true default but no generator")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosPresentEmptyMapErrors confirms that `retros: {}` (empty mapping)
// is still present and still errors due to missing generator.
// This was already tested in TestRetrosAutomatedTriggerWithoutGeneratorErrors
// but is spelled out explicitly here as a companion to the present-null cases.
func TestRetrosPresentEmptyMapErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros: {}
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should return error: retros: {} has on_run_close=true default but no generator")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRetrosAbsentIsNilAndByteStable is the canonical absent-key regression
// guard: a workflow without a retros: key must have Retros == nil, must
// return true from OnRunCloseEnabled (absent ≡ "default on"), and must
// round-trip without emitting a retros: key.
func TestRetrosAbsentIsNilAndByteStable(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Retros != nil {
		t.Fatalf("expected Retros == nil for absent retros: key, got %+v", w.Retros)
	}
	if !w.OnRunCloseEnabled() {
		t.Fatal("OnRunCloseEnabled() must return true when retros block is absent")
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML() error: %v", err)
	}
	if strings.Contains(string(out), "retros") {
		t.Fatalf("re-encoded absent-retros workflow must not contain 'retros', got:\n%s", out)
	}
}

// minimalWorkflow returns the smallest valid workflow for mutation tests.
func minimalWorkflow() Workflow {
	return Workflow{
		Name: "w",
		Stages: []Stage{
			{
				Name:     "plan",
				Produces: "plan",
				Skill:    "dev-planner",
				Inputs:   []string{"task"},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Runner tests
// ---------------------------------------------------------------------------

// TestRunnerParseName verifies runner: {name: opus} is parsed and validated.
func TestRunnerParseName(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner:
      name: opus
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Runner == nil {
		t.Fatal("expected Runner != nil")
	}
	if w.Stages[0].Runner.Name != "opus" {
		t.Fatalf("Runner.Name = %q, want %q", w.Stages[0].Runner.Name, "opus")
	}
	if w.Stages[0].Runner.Command != "" {
		t.Fatalf("Runner.Command should be empty, got %q", w.Stages[0].Runner.Command)
	}
}

// TestRunnerParseCommand verifies runner: {command: "make test"} is parsed.
func TestRunnerParseCommand(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner:
      command: make test
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Runner == nil {
		t.Fatal("expected Runner != nil")
	}
	if w.Stages[0].Runner.Command != "make test" {
		t.Fatalf("Runner.Command = %q, want %q", w.Stages[0].Runner.Command, "make test")
	}
}

// TestRunnerBothSetErrors asserts that setting both name and command is rejected.
func TestRunnerBothSetErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner:
      name: opus
      command: make test
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject runner with both name and command")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRunnerKnownFieldsRejectsUnknown asserts KnownFields(true) rejects an
// unknown key inside a runner block.
func TestRunnerKnownFieldsRejectsUnknown(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner:
      name: opus
      surprise: yes
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside runner:")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRunnerAbsentIsNil asserts that a stage with no runner: key has Runner==nil.
func TestRunnerAbsentIsNil(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Runner != nil {
		t.Fatalf("expected Runner==nil for absent key, got %+v", w.Stages[0].Runner)
	}
}

// TestRunnerPresentNullErrors asserts that a bare runner: key (null value) is
// treated as present and fails validation (no name or command).
func TestRunnerPresentNullErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner:
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should error: present-null runner is present but empty")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestRunnerPresentEmptyErrors asserts that runner: {} is treated as present
// and fails validation.
func TestRunnerPresentEmptyErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    runner: {}
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should error: runner: {} is present but has no name/command")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Gate tests
// ---------------------------------------------------------------------------

// TestGateParseFullBlock parses a gate with all fields set.
func TestGateParseFullBlock(t *testing.T) {
	threshold := 0.8
	rounds := 2
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      checks:
        - command: make lint
        - name: custom-check
      seats:
        - opus
        - codex
      pass_threshold: 0.8
      max_rounds: 2
      abstraction: review at design altitude
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	g := w.Stages[0].Gate
	if g == nil {
		t.Fatal("expected Gate != nil")
	}
	if len(g.Checks) != 2 {
		t.Fatalf("Checks count = %d, want 2", len(g.Checks))
	}
	if g.Checks[0].Command != "make lint" {
		t.Fatalf("Checks[0].Command = %q", g.Checks[0].Command)
	}
	if g.Checks[1].Name != "custom-check" {
		t.Fatalf("Checks[1].Name = %q", g.Checks[1].Name)
	}
	if len(g.Seats) != 2 || g.Seats[0] != "opus" || g.Seats[1] != "codex" {
		t.Fatalf("Seats = %v", g.Seats)
	}
	if g.PassThreshold == nil || *g.PassThreshold != threshold {
		t.Fatalf("PassThreshold = %v, want %v", g.PassThreshold, threshold)
	}
	if g.MaxRounds == nil || *g.MaxRounds != rounds {
		t.Fatalf("MaxRounds = %v, want %v", g.MaxRounds, rounds)
	}
	if g.Abstraction != "review at design altitude" {
		t.Fatalf("Abstraction = %q", g.Abstraction)
	}
	_ = threshold
}

// TestGateParseTier parses a gate with a tier ref.
func TestGateParseTier(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      tier: L2
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Gate == nil {
		t.Fatal("expected Gate != nil")
	}
	if w.Stages[0].Gate.Tier != "L2" {
		t.Fatalf("Tier = %q, want %q", w.Stages[0].Gate.Tier, "L2")
	}
}

// TestGateSeatsAndTierErrors asserts that setting both seats and tier is rejected.
func TestGateSeatsAndTierErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      seats: [opus]
      tier: L2
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject gate with both seats and tier")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateEmptyErrors asserts that a gate with no checks/seats/tier is rejected.
func TestGateEmptyErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate: {}
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject empty gate")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateEmptyChecksListErrors asserts that checks: [] with no seats/tier is
// rejected — an explicit empty list is treated as unset.
func TestGateEmptyChecksListErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      checks: []
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject gate with only empty checks: []")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGatePassThresholdZeroErrors asserts pass_threshold: 0 is rejected.
func TestGatePassThresholdZeroErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      seats: [opus]
      pass_threshold: 0
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject pass_threshold: 0")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGatePassThresholdNegativeErrors asserts a negative pass_threshold is rejected.
func TestGatePassThresholdNegativeErrors(t *testing.T) {
	w := minimalWorkflow()
	pt := -0.5
	w.Stages[0].Gate = &GateConfig{Seats: []string{"opus"}, PassThreshold: &pt}
	if err := w.Validate(); err == nil {
		t.Fatal("Validate should reject negative pass_threshold")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGatePassThresholdAboveOneErrors asserts pass_threshold > 1 is rejected.
func TestGatePassThresholdAboveOneErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      seats: [opus]
      pass_threshold: 1.5
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject pass_threshold: 1.5")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateMaxRoundsZeroErrors asserts max_rounds: 0 is rejected.
func TestGateMaxRoundsZeroErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      seats: [opus]
      max_rounds: 0
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject max_rounds: 0")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateMaxRoundsNegativeErrors asserts a negative max_rounds is rejected.
func TestGateMaxRoundsNegativeErrors(t *testing.T) {
	w := minimalWorkflow()
	mr := -1
	w.Stages[0].Gate = &GateConfig{Seats: []string{"opus"}, MaxRounds: &mr}
	if err := w.Validate(); err == nil {
		t.Fatal("Validate should reject negative max_rounds")
	} else if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateCheckBothNameAndCommandErrors asserts that a check with both name
// and command set is rejected.
func TestGateCheckBothNameAndCommandErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      checks:
        - name: my-check
          command: make check
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject check with both name and command")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateKnownFieldsRejectsUnknownInGate asserts unknown key inside gate: is rejected.
func TestGateKnownFieldsRejectsUnknownInGate(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      seats: [opus]
      bogus_key: oops
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside gate:")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateKnownFieldsRejectsUnknownInCheck asserts unknown key inside a check
// entry is rejected.
func TestGateKnownFieldsRejectsUnknownInCheck(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
      checks:
        - command: make lint
          weird_field: yes
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside gate.checks entry")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestGateAbsentIsNil asserts that a stage with no gate: key has Gate==nil.
func TestGateAbsentIsNil(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.Stages[0].Gate != nil {
		t.Fatalf("expected Gate==nil for absent key, got %+v", w.Stages[0].Gate)
	}
}

// TestGatePresentNullErrors asserts that a bare gate: key (null value) is
// treated as present and fails validation.
func TestGatePresentNullErrors(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    gate:
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should error: present-null gate is present but empty")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DefaultRunner (workflow-level) tests
// ---------------------------------------------------------------------------

// TestDefaultRunnerParse verifies default_runner at workflow level is parsed.
func TestDefaultRunnerParse(t *testing.T) {
	input := `name: w
default_runner:
  name: opus
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.DefaultRunner == nil {
		t.Fatal("expected DefaultRunner != nil")
	}
	if w.DefaultRunner.Name != "opus" {
		t.Fatalf("DefaultRunner.Name = %q, want %q", w.DefaultRunner.Name, "opus")
	}
}

// TestDefaultRunnerBothSetErrors asserts that setting both name and command in
// default_runner is rejected.
func TestDefaultRunnerBothSetErrors(t *testing.T) {
	input := `name: w
default_runner:
  name: opus
  command: make run
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject default_runner with both name and command")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestDefaultRunnerAbsentIsNil asserts that a workflow without default_runner:
// has DefaultRunner==nil.
func TestDefaultRunnerAbsentIsNil(t *testing.T) {
	w, err := ParseYAML([]byte(`name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.DefaultRunner != nil {
		t.Fatalf("expected DefaultRunner==nil, got %+v", w.DefaultRunner)
	}
}

// TestDefaultRunnerPresentNullErrors asserts bare default_runner: is present
// and fails validation.
func TestDefaultRunnerPresentNullErrors(t *testing.T) {
	input := `name: w
default_runner:
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should error: present-null default_runner")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestDefaultRunnerPresentEmptyErrors asserts default_runner: {} is present
// and fails validation.
func TestDefaultRunnerPresentEmptyErrors(t *testing.T) {
	input := `name: w
default_runner: {}
stages:
  - name: plan
    produces: plan
    skill: dev-planner
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should error: default_runner: {} has no name/command")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Effective-default accessor tests
// ---------------------------------------------------------------------------

// TestGateEffectiveDefaultsWhenUnset verifies accessor defaults for nil GateConfig.
func TestGateEffectiveDefaultsWhenUnset(t *testing.T) {
	var g *GateConfig
	if got := g.EffectivePassThreshold(); got != 1.0 {
		t.Fatalf("EffectivePassThreshold() = %g, want 1.0", got)
	}
	if got := g.EffectiveMaxRounds(); got != 3 {
		t.Fatalf("EffectiveMaxRounds() = %d, want 3", got)
	}
}

// TestGateEffectiveDefaultsWhenFieldsNil verifies accessor defaults when
// the GateConfig is non-nil but the fields are nil.
func TestGateEffectiveDefaultsWhenFieldsNil(t *testing.T) {
	g := &GateConfig{Seats: []string{"opus"}}
	if got := g.EffectivePassThreshold(); got != 1.0 {
		t.Fatalf("EffectivePassThreshold() = %g, want 1.0", got)
	}
	if got := g.EffectiveMaxRounds(); got != 3 {
		t.Fatalf("EffectiveMaxRounds() = %d, want 3", got)
	}
}

// TestGateEffectiveDefaultsWhenExplicit verifies explicit values are returned.
func TestGateEffectiveDefaultsWhenExplicit(t *testing.T) {
	pt := 0.75
	mr := 5
	g := &GateConfig{
		Seats:         []string{"opus"},
		PassThreshold: &pt,
		MaxRounds:     &mr,
	}
	if got := g.EffectivePassThreshold(); got != 0.75 {
		t.Fatalf("EffectivePassThreshold() = %g, want 0.75", got)
	}
	if got := g.EffectiveMaxRounds(); got != 5 {
		t.Fatalf("EffectiveMaxRounds() = %d, want 5", got)
	}
}

// ---------------------------------------------------------------------------
// AC#3: arbitrary stage names (no phase hardcoding)
// ---------------------------------------------------------------------------

// TestArbitraryStageNamesWithRunnerAndGate asserts that a workflow with stage
// names that don't match the default phase names (plan/implement/etc.) parses
// and validates cleanly when runner + gate are present.
func TestArbitraryStageNamesWithRunnerAndGate(t *testing.T) {
	input := `name: research-workflow
stages:
  - name: research
    produces: research
    inputs:
      - task
    skill: researcher
    runner:
      name: opus
    gate:
      tier: L2
  - name: fact-check
    produces: fact-check
    inputs:
      - research
    skill: fact-checker
    runner:
      command: make fact-check
  - name: draft
    produces: draft
    inputs:
      - fact-check
    skill: writer
    gate:
      seats:
        - opus
        - codex
      pass_threshold: 1.0
      max_rounds: 3
      abstraction: "review prose accuracy and tone"
  - name: tone-police
    produces: final
    inputs:
      - draft
    skill: tone-editor
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	if len(w.Stages) != 4 {
		t.Fatalf("expected 4 stages, got %d", len(w.Stages))
	}
	if w.Stages[0].Runner == nil || w.Stages[0].Runner.Name != "opus" {
		t.Fatalf("stage[0].Runner.Name mismatch: %+v", w.Stages[0].Runner)
	}
	if w.Stages[0].Gate == nil || w.Stages[0].Gate.Tier != "L2" {
		t.Fatalf("stage[0].Gate.Tier mismatch: %+v", w.Stages[0].Gate)
	}
}

// ---------------------------------------------------------------------------
// Byte-stable round-trip tests
// ---------------------------------------------------------------------------

// TestLegacyStageNoRunnerOrGateRoundTrip asserts that a legacy stage (skill
// only, no runner/gate/default_runner) round-trips byte-stable and the output
// contains none of the new keys.
func TestLegacyStageNoRunnerOrGateRoundTrip(t *testing.T) {
	legacy := `name: w
stages:
  - name: plan
    produces: plan
    inputs:
      - task
    skill: dev-planner
`
	w, err := ParseYAML([]byte(legacy))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	if string(out) != legacy {
		t.Fatalf("legacy round-trip not byte-stable\ngot:\n%s\nwant:\n%s", out, legacy)
	}
	for _, key := range []string{"runner:", "gate:", "default_runner:"} {
		if strings.Contains(string(out), key) {
			t.Fatalf("re-encoded legacy workflow must not contain %q:\n%s", key, out)
		}
	}
}

// TestGoldenDefaultYAMLLegacyRoundTrip is the companion to TestLegacyStageNoRunnerOrGateRoundTrip
// for the full Default() workflow: it must round-trip byte-stable and contain
// none of the new keys (Default() is deliberately unchanged).
func TestGoldenDefaultYAMLLegacyRoundTrip(t *testing.T) {
	b, err := Default().YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	w, err := ParseYAML(b)
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("re-encode error: %v", err)
	}
	if string(out) != string(b) {
		t.Fatalf("Default() round-trip not byte-stable\nfirst:\n%s\nre-encode:\n%s", b, out)
	}
	for _, key := range []string{"runner:", "gate:", "default_runner:"} {
		if strings.Contains(string(out), key) {
			t.Fatalf("Default() re-encode must not contain %q:\n%s", key, out)
		}
	}
}

// TestNewShapeWorkflowRoundTrip asserts that a workflow with runner, gate, and
// default_runner round-trips parse→encode→parse→encode byte-stable.
func TestNewShapeWorkflowRoundTrip(t *testing.T) {
	pt := 1.0
	mr := 3
	w := Workflow{
		Name:          "research-workflow",
		DefaultRunner: &Runner{Name: "opus"},
		Stages: []Stage{
			{
				Name:     "plan",
				Produces: "plan",
				Inputs:   []string{"task"},
				Skill:    "dev-planner",
				Runner:   &Runner{Name: "opus"},
				Gate: &GateConfig{
					Tier:          "L2",
					PassThreshold: &pt,
					MaxRounds:     &mr,
					Abstraction:   "review at design altitude",
				},
			},
			{
				Name:     "implement",
				Produces: "diff",
				Inputs:   []string{"plan", "repo-state"},
				Skill:    "dev-coder",
				Runner:   &Runner{Command: "make implement"},
				Gate: &GateConfig{
					Seats:     []string{"opus", "codex"},
					MaxRounds: &mr,
				},
			},
		},
	}
	first, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML error: %v", err)
	}
	w2, err := ParseYAML(first)
	if err != nil {
		t.Fatalf("first ParseYAML error: %v", err)
	}
	second, err := w2.YAML()
	if err != nil {
		t.Fatalf("second YAML error: %v", err)
	}
	if string(second) != string(first) {
		t.Fatalf("new-shape round-trip not byte-stable\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	// Verify the new keys are present.
	for _, key := range []string{"runner:", "gate:", "default_runner:"} {
		if !strings.Contains(string(first), key) {
			t.Fatalf("encoded new-shape workflow must contain %q:\n%s", key, first)
		}
	}
}

// ---------------------------------------------------------------------------
// retros.nudge
// ---------------------------------------------------------------------------

// TestNudgeAbsentDefaultsOff asserts that a workflow with no nudge: block is
// disabled by default (NudgeEnabled() == false), and that NudgeThreshold()
// defaults to 3.
func TestNudgeAbsentDefaultsOff(t *testing.T) {
	w := minimalWorkflow()
	if w.NudgeEnabled() {
		t.Fatalf("nudge enabled for workflow without retros block")
	}
	if got := w.NudgeThreshold(); got != 3 {
		t.Fatalf("default NudgeThreshold = %d, want 3", got)
	}
}

// TestNudgePresentRetrosOmittedNudgeBlock asserts a present retros block with
// no nudge: sub-block leaves nudge disabled.
func TestNudgePresentRetrosOmittedNudgeBlock(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.NudgeEnabled() {
		t.Fatalf("nudge enabled when sub-block omitted")
	}
	if got := w.NudgeThreshold(); got != 3 {
		t.Fatalf("NudgeThreshold = %d, want 3", got)
	}
}

// TestNudgeExplicitEnabled asserts retros.nudge.enabled: true activates the
// nudge and that an explicit threshold replaces the default.
func TestNudgeExplicitEnabled(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  nudge:
    enabled: true
    threshold: 5
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if !w.NudgeEnabled() {
		t.Fatal("expected NudgeEnabled() == true")
	}
	if got := w.NudgeThreshold(); got != 5 {
		t.Fatalf("NudgeThreshold = %d, want 5", got)
	}
}

// TestNudgeEnabledWithoutThresholdDefaultsTo3 asserts enabling the nudge
// without a threshold uses the default 3.
func TestNudgeEnabledWithoutThresholdDefaultsTo3(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  nudge:
    enabled: true
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if !w.NudgeEnabled() {
		t.Fatal("expected NudgeEnabled() == true")
	}
	if got := w.NudgeThreshold(); got != 3 {
		t.Fatalf("NudgeThreshold = %d, want 3", got)
	}
}

// TestNudgeThresholdZeroRejectedWhenEnabled asserts threshold: 0 alongside
// enabled: true is rejected.
func TestNudgeThresholdZeroRejectedWhenEnabled(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  nudge:
    enabled: true
    threshold: 0
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject nudge threshold: 0 when enabled")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// TestNudgeThresholdZeroAcceptedWhenDisabled asserts threshold: 0 alongside
// enabled: false is inert (the validator only fires when the nudge is on).
func TestNudgeThresholdZeroAcceptedWhenDisabled(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  nudge:
    enabled: false
    threshold: 0
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML error: %v", err)
	}
	if w.NudgeEnabled() {
		t.Fatal("nudge enabled despite enabled: false")
	}
}

// TestNudgeKnownFieldsRejectsUnknownInNudgeBlock asserts that an unknown key
// inside retros.nudge is rejected by KnownFields.
func TestNudgeKnownFieldsRejectsUnknownInNudgeBlock(t *testing.T) {
	input := `name: w
stages:
  - name: plan
    produces: plan
    skill: dev-planner
retros:
  on_run_close: false
  nudge:
    enabled: true
    threshold: 3
    surprise: yes
`
	_, err := ParseYAML([]byte(input))
	if err == nil {
		t.Fatal("ParseYAML should reject unknown key inside nudge:")
	}
	if !errors.Is(err, ErrInvalidWorkflow) {
		t.Fatalf("error does not wrap ErrInvalidWorkflow: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EnvAllowlist tests
// ---------------------------------------------------------------------------

// TestWorkflowEnvAllowlist_Parse verifies env_allowlist parses from YAML.
func TestWorkflowEnvAllowlist_Parse(t *testing.T) {
	input := `name: w
env_allowlist: [FOO, BAR]
stages:
  - name: plan
    produces: plan
    skill: dev-planner
    inputs: [task]
`
	w, err := ParseYAML([]byte(input))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(w.EnvAllowlist) != 2 || w.EnvAllowlist[0] != "FOO" || w.EnvAllowlist[1] != "BAR" {
		t.Errorf("EnvAllowlist = %v, want [FOO BAR]", w.EnvAllowlist)
	}
}

// TestWorkflowEnvAllowlist_ValidationRejects verifies invalid names are rejected by Validate.
func TestWorkflowEnvAllowlist_ValidationRejects(t *testing.T) {
	cases := []struct {
		name  string
		names []string
	}{
		{"contains equals", []string{"FOO=bar"}},
		{"empty string", []string{""}},
		{"starts with digit", []string{"1FOO"}},
		{"reserved PATH", []string{"PATH"}},
		{"reserved ETUDE_INPUTS_DIR", []string{"ETUDE_INPUTS_DIR"}},
		{"reserved ETUDE_OUTPUT_FILE", []string{"ETUDE_OUTPUT_FILE"}},
		{"duplicate", []string{"FOO", "FOO"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := minimalWorkflow()
			w.EnvAllowlist = tc.names
			if err := w.Validate(); err == nil {
				t.Errorf("expected error for %v, got nil", tc.names)
			}
		})
	}
}

// TestWorkflowEnvAllowlist_AbsentStaysAbsent verifies no env_allowlist key in YAML
// output when the field is nil.
func TestWorkflowEnvAllowlist_AbsentStaysAbsent(t *testing.T) {
	w := minimalWorkflow()
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	if strings.Contains(string(out), "env_allowlist") {
		t.Errorf("env_allowlist key should be absent when nil:\n%s", out)
	}
}

// TestWorkflowEnvAllowlist_RoundTrip verifies env_allowlist survives YAML().ParseYAML()
// and that two encodings produce identical bytes.
func TestWorkflowEnvAllowlist_RoundTrip(t *testing.T) {
	w := minimalWorkflow()
	w.EnvAllowlist = []string{"FOO", "BAR_BAZ"}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	got, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML round-trip: %v", err)
	}
	if len(got.EnvAllowlist) != 2 || got.EnvAllowlist[0] != "FOO" || got.EnvAllowlist[1] != "BAR_BAZ" {
		t.Errorf("round-trip EnvAllowlist = %v, want [FOO BAR_BAZ]", got.EnvAllowlist)
	}
	out2, err := got.YAML()
	if err != nil {
		t.Fatalf("YAML second encode: %v", err)
	}
	if string(out) != string(out2) {
		t.Errorf("YAML not byte-stable:\nfirst:\n%s\nsecond:\n%s", out, out2)
	}
}

// TestNudgeRoundTripExplicitConfig asserts a workflow with an explicit
// retros.nudge block survives YAML().ParseYAML() losslessly.
func TestNudgeRoundTripExplicitConfig(t *testing.T) {
	enabled := true
	threshold := 7
	disabled := false
	w := minimalWorkflow()
	w.Retros = &RetrosConfig{
		OnRunClose: &disabled, // silence the on_run_close default so we don't need a generator
		Nudge: &NudgeConfig{
			Enabled:   &enabled,
			Threshold: &threshold,
		},
	}
	if err := w.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	out, err := w.YAML()
	if err != nil {
		t.Fatalf("YAML: %v", err)
	}
	got, err := ParseYAML(out)
	if err != nil {
		t.Fatalf("ParseYAML round-trip: %v", err)
	}
	if !got.NudgeEnabled() {
		t.Fatal("round-trip lost enabled: true")
	}
	if got.NudgeThreshold() != 7 {
		t.Fatalf("round-trip threshold = %d, want 7", got.NudgeThreshold())
	}
	if !strings.Contains(string(out), "nudge:") {
		t.Fatalf("encoded YAML missing nudge: block\n%s", out)
	}
}
