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
