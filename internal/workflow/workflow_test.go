package workflow

import (
	"errors"
	"strings"
	"testing"
)

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
    skill: dev-coder
  - name: test-plan
    produces: test-plan
    inputs:
      - plan
      - diff
    skill: dev-test-writer
    eval:
      method: rubric
      rubric: evals/test-plan-rubric.md
  - name: test
    produces: test-diff
    inputs:
      - test-plan
      - diff
    skill: dev-test-writer
  - name: review
    produces: review
    inputs:
      - diff
      - plan
    skill: dev-pr-reviewer
    eval:
      method: pairwise
  - name: docs
    produces: docs-diff
    inputs:
      - diff
    skill: dev-docs-writer
    optional: true
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
