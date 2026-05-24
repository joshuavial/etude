// Package workflow defines and validates the .etude/workflow.yaml schema.
// It provides read (ParseYAML) and write (YAML, Default) halves so the
// consumer (etude-init-command) can scaffold and parse the file without any
// circular dependency.
package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrInvalidWorkflow is the sentinel error returned by Validate and ParseYAML
// when the workflow does not satisfy the schema rules.
var ErrInvalidWorkflow = errors.New("invalid workflow")

// specialRoles is the closed set of input roles that do not need to be
// produced by an earlier stage.  "task" is the unit of work handed to the
// first stage; "repo-state" is the git checkout recorded in the manifest's
// git_sha field and is always available as an implicit input.
var specialRoles = map[string]bool{
	"task":       true,
	"repo-state": true,
}

// validEvalMethods is the closed set of eval method strings.
var validEvalMethods = map[string]bool{
	"rubric":    true,
	"pairwise":  true,
	"assertion": true,
}

// Workflow is the top-level model for .etude/workflow.yaml.
type Workflow struct {
	Name   string
	Stages []Stage
}

// Stage describes one ordered step in the workflow.
type Stage struct {
	// Name is the human-readable identifier for this stage (e.g. "plan").
	Name string
	// Produces is the role token that this stage's output artifact carries
	// (e.g. "plan", "diff").  Must be unique across all stages and must not
	// be a reserved special role ("task" or "repo-state").
	Produces string
	// Inputs lists the role tokens this stage consumes.  May be nil (zero
	// inputs is valid — the stage relies solely on implicit repo-state).
	// Every entry must be a special role (task, repo-state) or a role
	// produced by an earlier stage in declaration order.
	Inputs []string
	// Skill is the skill identifier that executes this stage.  Required.
	Skill string
	// Optional marks this stage as skippable in a run.  Defaults to false.
	Optional bool
	// Eval holds the evaluation configuration for this stage.  May be nil
	// (no eval configured).
	Eval *Eval
}

// Eval holds the evaluation configuration for a stage.
type Eval struct {
	// Method is one of "rubric", "pairwise", or "assertion".
	Method string
	// Rubric is a relative path to the rubric file.  Required when Method
	// is "rubric"; must be empty for all other methods.
	Rubric string
}

// Validate checks all well-formedness rules and returns a wrapped
// ErrInvalidWorkflow on the first violation.
func (w Workflow) Validate() error {
	if strings.TrimSpace(w.Name) == "" {
		return fmt.Errorf("%w: name required", ErrInvalidWorkflow)
	}
	// The workflow name is deliberately held to the strict role-token charset
	// ([a-z0-9-]) rather than the broader manifest identifier charset. The name
	// flows into the manifest's "workflow" field, so the schema layer is the
	// stricter producer: anything valid here is valid downstream.
	if err := validateRoleToken("name", w.Name); err != nil {
		return err
	}
	if len(w.Stages) == 0 {
		return fmt.Errorf("%w: at least one stage required", ErrInvalidWorkflow)
	}

	seenStageNames := make(map[string]bool, len(w.Stages))
	seenProducesRoles := make(map[string]bool, len(w.Stages))

	// producedSoFar tracks roles available to the current stage's inputs.
	// It is built incrementally so forward references are caught.
	producedSoFar := make(map[string]bool, len(w.Stages))

	for i, s := range w.Stages {
		prefix := fmt.Sprintf("stage[%d]", i)

		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("%w: %s name required", ErrInvalidWorkflow, prefix)
		}
		if err := validateStageName(prefix+".name", s.Name); err != nil {
			return err
		}
		if seenStageNames[s.Name] {
			return fmt.Errorf("%w: duplicate stage name %q", ErrInvalidWorkflow, s.Name)
		}
		seenStageNames[s.Name] = true

		if strings.TrimSpace(s.Produces) == "" {
			return fmt.Errorf("%w: %s produces required", ErrInvalidWorkflow, prefix)
		}
		if err := validateRoleToken(prefix+".produces", s.Produces); err != nil {
			return err
		}
		// A reserved special role (task, repo-state) is an implicit input
		// available to every stage; a stage producing one is meaningless, so
		// reject it rather than let it slip through as a no-op.
		if specialRoles[s.Produces] {
			return fmt.Errorf("%w: %s produces role %q is reserved", ErrInvalidWorkflow, prefix, s.Produces)
		}
		if seenProducesRoles[s.Produces] {
			return fmt.Errorf("%w: duplicate produces role %q", ErrInvalidWorkflow, s.Produces)
		}
		seenProducesRoles[s.Produces] = true

		if strings.TrimSpace(s.Skill) == "" {
			return fmt.Errorf("%w: %s skill required", ErrInvalidWorkflow, prefix)
		}

		// Validate inputs before registering this stage's produces role so
		// that self-references are also rejected.
		seenInputRoles := make(map[string]bool, len(s.Inputs))
		for j, inp := range s.Inputs {
			if strings.TrimSpace(inp) == "" {
				return fmt.Errorf("%w: %s input[%d] role required", ErrInvalidWorkflow, prefix, j)
			}
			if err := validateRoleToken(fmt.Sprintf("%s.input[%d]", prefix, j), inp); err != nil {
				return err
			}
			// Duplicate inputs within a stage are rejected: an input appearing
			// twice gives no additional information and most likely indicates a
			// copy-paste error.
			if seenInputRoles[inp] {
				return fmt.Errorf("%w: %s duplicate input role %q", ErrInvalidWorkflow, prefix, inp)
			}
			seenInputRoles[inp] = true

			if !specialRoles[inp] && !producedSoFar[inp] {
				return fmt.Errorf("%w: %s input role %q is not a special role and is not produced by an earlier stage", ErrInvalidWorkflow, prefix, inp)
			}
		}

		// Register this stage's output role for subsequent stages.
		producedSoFar[s.Produces] = true

		if s.Eval != nil {
			if err := validateEval(prefix+".eval", s.Eval); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEval(prefix string, e *Eval) error {
	if !validEvalMethods[e.Method] {
		return fmt.Errorf("%w: %s method %q is not one of rubric, pairwise, assertion", ErrInvalidWorkflow, prefix, e.Method)
	}
	switch e.Method {
	case "rubric":
		if strings.TrimSpace(e.Rubric) == "" {
			return fmt.Errorf("%w: %s rubric path required for method rubric", ErrInvalidWorkflow, prefix)
		}
	default: // pairwise, assertion
		if strings.TrimSpace(e.Rubric) != "" {
			return fmt.Errorf("%w: %s rubric path must not be set for method %q", ErrInvalidWorkflow, prefix, e.Method)
		}
	}
	return nil
}

// validateStageName applies the manifest identifier charset to stage names:
// [A-Za-z0-9_.-] — the same set runmanifest.IsValidIdentifier enforces. The
// rule is kept as a local per-rune predicate (isIdentChar) rather than calling
// that exported helper: it is a whole-string check, and adding a sibling
// workflow->runmanifest import to dedupe one trivial fixed charset is not worth
// the coupling.
func validateStageName(field, value string) error {
	for _, r := range value {
		if !isIdentChar(r) {
			return fmt.Errorf("%w: invalid %s %q", ErrInvalidWorkflow, field, value)
		}
	}
	return nil
}

// validateRoleToken applies a stricter charset to role tokens (produces,
// inputs, workflow name).  Brief examples use only lowercase letters and
// hyphens (e.g. "plan", "repo-state", "docs-diff").  We permit the same
// lowercase-and-hyphen set here rather than the broader identifier set used
// for stage names.  This is a deliberate, documented choice: role tokens are
// artifact role labels that travel across package boundaries and into
// manifests; keeping them lowercase-only avoids case-folding ambiguity.
// The period (.) is NOT permitted in role tokens because role tokens do not
// encode hierarchy or file extensions.
func validateRoleToken(field, value string) error {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return fmt.Errorf("%w: invalid %s %q (role tokens must match [a-z0-9-])", ErrInvalidWorkflow, field, value)
		}
	}
	return nil
}

// isIdentChar returns true for [A-Za-z0-9_.-], the manifest identifier charset.
// Kept local (see validateStageName) rather than reusing runmanifest's exported
// whole-string IsValidIdentifier, to avoid a sibling-package import for one rule.
func isIdentChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.'
}

// YAML serializes the Workflow to canonical YAML bytes.  Returns an error if
// the workflow fails Validate.
func (w Workflow) YAML() ([]byte, error) {
	if err := w.Validate(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(w.toYAML()); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ParseYAML decodes YAML bytes, maps them to the typed model, and validates.
// Unknown fields are rejected (mirrors manifest's DisallowUnknownFields).
func ParseYAML(content []byte) (Workflow, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	var doc workflowYAML
	if err := dec.Decode(&doc); err != nil {
		return Workflow{}, fmt.Errorf("%w: decode: %v", ErrInvalidWorkflow, err)
	}
	if err := ensureEOF(dec); err != nil {
		return Workflow{}, err
	}
	w := doc.toWorkflow()
	if err := w.Validate(); err != nil {
		return Workflow{}, err
	}
	return w, nil
}

// ensureEOF rejects trailing data or extra YAML documents after the first one,
// mirroring runmanifest.ParseJSON's strictness: a workflow file must contain
// exactly one document and nothing else.
func ensureEOF(dec *yaml.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("%w: trailing data: %v", ErrInvalidWorkflow, err)
	}
	return fmt.Errorf("%w: trailing data after first document", ErrInvalidWorkflow)
}

// Default returns the canonical 6-stage workflow from BRIEF.md §4.1.
// This is the workflow that etude-init-command scaffolds via Default().YAML().
func Default() Workflow {
	rubricEval := func(path string) *Eval { return &Eval{Method: "rubric", Rubric: path} }
	return Workflow{
		Name: "default",
		Stages: []Stage{
			{
				Name:     "plan",
				Produces: "plan",
				Inputs:   []string{"task"},
				Skill:    "dev-planner",
				Eval:     rubricEval("evals/plan-rubric.md"),
			},
			{
				Name:     "implement",
				Produces: "diff",
				Inputs:   []string{"plan", "repo-state"},
				Skill:    "dev-coder",
			},
			{
				Name:     "test-plan",
				Produces: "test-plan",
				Inputs:   []string{"plan", "diff"},
				Skill:    "dev-test-writer",
				Eval:     rubricEval("evals/test-plan-rubric.md"),
			},
			{
				Name:     "test",
				Produces: "test-diff",
				Inputs:   []string{"test-plan", "diff"},
				Skill:    "dev-test-writer",
			},
			{
				Name:     "review",
				Produces: "review",
				Inputs:   []string{"diff", "plan"},
				Skill:    "dev-pr-reviewer",
				Eval:     &Eval{Method: "pairwise"},
			},
			{
				Name:     "docs",
				Produces: "docs-diff",
				Inputs:   []string{"diff"},
				Skill:    "dev-docs-writer",
				Optional: true,
			},
		},
	}
}

// ---- YAML decode/encode layer -----------------------------------------------

// workflowYAML is the internal struct used for YAML decode/encode, with
// yaml struct tags.  It is the counterpart to manifestJSON in runmanifest.
type workflowYAML struct {
	Name   string      `yaml:"name"`
	Stages []stageYAML `yaml:"stages"`
}

type stageYAML struct {
	Name     string    `yaml:"name"`
	Produces string    `yaml:"produces"`
	Inputs   []string  `yaml:"inputs,omitempty"`
	Skill    string    `yaml:"skill"`
	Optional bool      `yaml:"optional,omitempty"`
	Eval     *evalYAML `yaml:"eval,omitempty"`
}

type evalYAML struct {
	Method string `yaml:"method"`
	Rubric string `yaml:"rubric,omitempty"`
}

func (w Workflow) toYAML() workflowYAML {
	stages := make([]stageYAML, 0, len(w.Stages))
	for _, s := range w.Stages {
		sy := stageYAML{
			Name:     s.Name,
			Produces: s.Produces,
			Inputs:   s.Inputs,
			Skill:    s.Skill,
			Optional: s.Optional,
		}
		if s.Eval != nil {
			sy.Eval = &evalYAML{Method: s.Eval.Method, Rubric: s.Eval.Rubric}
		}
		stages = append(stages, sy)
	}
	return workflowYAML{Name: w.Name, Stages: stages}
}

func (d workflowYAML) toWorkflow() Workflow {
	stages := make([]Stage, 0, len(d.Stages))
	for _, s := range d.Stages {
		st := Stage{
			Name:     s.Name,
			Produces: s.Produces,
			Inputs:   s.Inputs,
			Skill:    s.Skill,
			Optional: s.Optional,
		}
		if s.Eval != nil {
			st.Eval = &Eval{Method: s.Eval.Method, Rubric: s.Eval.Rubric}
		}
		stages = append(stages, st)
	}
	return Workflow{Name: d.Name, Stages: stages}
}
