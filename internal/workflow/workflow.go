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
	// Retros holds the optional retros: block from workflow.yaml.  Nil means
	// the block is absent (legacy / Default()).  A non-nil pointer means the
	// operator explicitly authored the block.  Use the accessor methods on
	// Workflow (OnRunCloseEnabled, etc.) to read effective values — those
	// methods apply the correct defaults and never require callers to nil-check
	// the pointer.
	Retros *RetrosConfig
}

// RetrosConfig holds the parsed retros: configuration block.  Fields use
// pointer types so that an omitted field is distinguishable from an explicit
// false/0 when encoding and when computing effective defaults via the accessor
// methods.
type RetrosConfig struct {
	// OnRunClose enables the on_run_close trigger.  Nil means omitted (default
	// ON per accessor); explicit false disables it.
	OnRunClose *bool
	// OnRepeatedGateBlock holds the on_repeated_gate_block sub-block.  Nil
	// means omitted (default OFF).
	OnRepeatedGateBlock *RepeatedGateBlock
	// OnFailedVerify enables the on_failed_verify trigger.  Nil = default OFF.
	OnFailedVerify *bool
	// OnBlockedState enables the on_blocked_state trigger.  Nil = default OFF.
	OnBlockedState *bool
	// PostBench enables the post_bench trigger.  Nil = default OFF.
	PostBench *bool
	// Generator is the path to the retro generator script.  Required when any
	// automated trigger is effectively enabled.
	Generator string
}

// RepeatedGateBlock holds the on_repeated_gate_block sub-block.
type RepeatedGateBlock struct {
	// Enabled activates the trigger.  Nil = default OFF.
	Enabled *bool
	// Threshold is the number of gate blocks before triggering.  Nil = default 3.
	Threshold *int
}

// OnRunCloseEnabled returns the effective value of the on_run_close trigger.
// True when the retros block is absent (nil), when the block is present but
// on_run_close is omitted, or when on_run_close is explicitly true.  Returns
// false only when on_run_close is explicitly set to false.
func (w Workflow) OnRunCloseEnabled() bool {
	if w.Retros == nil || w.Retros.OnRunClose == nil {
		return true
	}
	return *w.Retros.OnRunClose
}

// OnRepeatedGateBlockEnabled returns the effective value of the
// on_repeated_gate_block.enabled trigger.  Default OFF.
func (w Workflow) OnRepeatedGateBlockEnabled() bool {
	if w.Retros == nil || w.Retros.OnRepeatedGateBlock == nil || w.Retros.OnRepeatedGateBlock.Enabled == nil {
		return false
	}
	return *w.Retros.OnRepeatedGateBlock.Enabled
}

// RepeatedGateBlockThreshold returns the effective threshold for the
// on_repeated_gate_block trigger.  Default 3 when omitted.
func (w Workflow) RepeatedGateBlockThreshold() int {
	if w.Retros == nil || w.Retros.OnRepeatedGateBlock == nil || w.Retros.OnRepeatedGateBlock.Threshold == nil {
		return 3
	}
	return *w.Retros.OnRepeatedGateBlock.Threshold
}

// OnFailedVerifyEnabled returns the effective value of the on_failed_verify
// trigger.  Default OFF.
func (w Workflow) OnFailedVerifyEnabled() bool {
	if w.Retros == nil || w.Retros.OnFailedVerify == nil {
		return false
	}
	return *w.Retros.OnFailedVerify
}

// OnBlockedStateEnabled returns the effective value of the on_blocked_state
// trigger.  Default OFF.
func (w Workflow) OnBlockedStateEnabled() bool {
	if w.Retros == nil || w.Retros.OnBlockedState == nil {
		return false
	}
	return *w.Retros.OnBlockedState
}

// PostBenchEnabled returns the effective value of the post_bench trigger.
// Default OFF.
func (w Workflow) PostBenchEnabled() bool {
	if w.Retros == nil || w.Retros.PostBench == nil {
		return false
	}
	return *w.Retros.PostBench
}

// RetroGenerator returns the generator script path, or "" when unset.
func (w Workflow) RetroGenerator() string {
	if w.Retros == nil {
		return ""
	}
	return w.Retros.Generator
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

	if err := validateRetros(w); err != nil {
		return err
	}

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

// validateRetros checks the retros block when present.  An absent block (nil)
// is always valid — legacy and Default() workflows never fail here.
func validateRetros(w Workflow) error {
	if w.Retros == nil {
		return nil
	}
	// generator is required when any automated trigger is effectively enabled.
	anyAutomated := w.OnRunCloseEnabled() ||
		w.OnRepeatedGateBlockEnabled() ||
		w.OnFailedVerifyEnabled() ||
		w.OnBlockedStateEnabled() ||
		w.PostBenchEnabled()
	if anyAutomated && strings.TrimSpace(w.RetroGenerator()) == "" {
		return fmt.Errorf("%w: retros.generator is required when any automated trigger is enabled", ErrInvalidWorkflow)
	}
	// threshold must be >= 1 when the trigger is enabled and explicitly set.
	if w.OnRepeatedGateBlockEnabled() {
		rgb := w.Retros.OnRepeatedGateBlock
		if rgb != nil && rgb.Threshold != nil && *rgb.Threshold < 1 {
			return fmt.Errorf("%w: retros.on_repeated_gate_block.threshold must be >= 1", ErrInvalidWorkflow)
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
	w, err := doc.toWorkflow()
	if err != nil {
		return Workflow{}, err
	}
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
//
// Retros is a yaml.Node rather than *retrosYAML so that presence detection is
// possible: a zero Node (Kind == 0) means the key was absent; any non-zero Node
// means the key was present, even when its value is bare/null.  This lets
// present-null behave identically to present-empty ({}) rather than silently
// collapsing to absent.  The omitempty tag suppresses a zero Node on re-encode
// so absent → nil Workflow.Retros → no retros: key in the output.
type workflowYAML struct {
	Name   string      `yaml:"name"`
	Stages []stageYAML `yaml:"stages"`
	Retros yaml.Node   `yaml:"retros,omitempty"`
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

// retrosYAML is the decode/encode counterpart to RetrosConfig.  Pointer types
// preserve the omitted-vs-explicit distinction for both encoding (omitempty
// drops nil pointers) and the accessor methods.
type retrosYAML struct {
	OnRunClose          *bool                  `yaml:"on_run_close,omitempty"`
	OnRepeatedGateBlock *repeatedGateBlockYAML `yaml:"on_repeated_gate_block,omitempty"`
	OnFailedVerify      *bool                  `yaml:"on_failed_verify,omitempty"`
	OnBlockedState      *bool                  `yaml:"on_blocked_state,omitempty"`
	PostBench           *bool                  `yaml:"post_bench,omitempty"`
	Generator           string                 `yaml:"generator,omitempty"`
}

// repeatedGateBlockYAML is the nested decode/encode struct for
// on_repeated_gate_block.  A dedicated struct ensures KnownFields(true) rejects
// unknown keys at this level too.
type repeatedGateBlockYAML struct {
	Enabled   *bool `yaml:"enabled,omitempty"`
	Threshold *int  `yaml:"threshold,omitempty"`
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
	out := workflowYAML{Name: w.Name, Stages: stages}
	if w.Retros != nil {
		ry := &retrosYAML{
			OnRunClose:     w.Retros.OnRunClose,
			OnFailedVerify: w.Retros.OnFailedVerify,
			OnBlockedState: w.Retros.OnBlockedState,
			PostBench:      w.Retros.PostBench,
			Generator:      w.Retros.Generator,
		}
		if w.Retros.OnRepeatedGateBlock != nil {
			ry.OnRepeatedGateBlock = &repeatedGateBlockYAML{
				Enabled:   w.Retros.OnRepeatedGateBlock.Enabled,
				Threshold: w.Retros.OnRepeatedGateBlock.Threshold,
			}
		}
		// Encode the retrosYAML struct to a yaml.Node so it can be embedded
		// in workflowYAML.Retros (which is now a yaml.Node for presence
		// detection).  yaml.Node.Encode populates the receiver directly as a
		// mapping node (Kind==MappingNode) — no document wrapper is added, so
		// the result can be assigned to out.Retros directly.
		var retrosNode yaml.Node
		if err := retrosNode.Encode(ry); err == nil {
			out.Retros = retrosNode
		}
	}
	return out
}

// decodeRetrosNode converts a yaml.Node captured for the retros: key into a
// *retrosYAML suitable for toWorkflow.  Returns (nil, nil) when the node is
// the zero value (key was absent).  Returns a zero-value *retrosYAML (present
// but empty) for a null scalar, mirroring the "present means present" rule.
// Returns a populated *retrosYAML for a mapping node.  Unknown keys inside the
// block are still rejected because the inner decode uses a KnownFields(true)
// decoder (achieved by re-marshalling the node to bytes and decoding again).
func decodeRetrosNode(node yaml.Node) (*retrosYAML, error) {
	// Kind == 0: the retros: key was entirely absent in the YAML document.
	if node.Kind == 0 {
		return nil, nil
	}
	// Scalar with !!null tag: the key was present but had a bare/null value
	// (e.g. `retros:` or `retros: null`).  Treat as present-empty, equivalent
	// to `retros: {}`.  A zero retrosYAML already has all-nil fields, which is
	// what we want — no further decoding needed, and there are no keys to
	// validate against KnownFields.
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return &retrosYAML{}, nil
	}
	// Any other node (mapping, etc.): re-marshal to bytes and decode through a
	// KnownFields(true) decoder so unknown keys are still rejected, exactly as
	// they were before this change.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	var ry retrosYAML
	if err := dec.Decode(&ry); err != nil {
		return nil, err
	}
	return &ry, nil
}

func (d workflowYAML) toWorkflow() (Workflow, error) {
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
	w := Workflow{Name: d.Name, Stages: stages}
	ry, err := decodeRetrosNode(d.Retros)
	if err != nil {
		return Workflow{}, fmt.Errorf("%w: decode retros: %v", ErrInvalidWorkflow, err)
	}
	if ry != nil {
		rc := &RetrosConfig{
			OnRunClose:     ry.OnRunClose,
			OnFailedVerify: ry.OnFailedVerify,
			OnBlockedState: ry.OnBlockedState,
			PostBench:      ry.PostBench,
			Generator:      ry.Generator,
		}
		if ry.OnRepeatedGateBlock != nil {
			rc.OnRepeatedGateBlock = &RepeatedGateBlock{
				Enabled:   ry.OnRepeatedGateBlock.Enabled,
				Threshold: ry.OnRepeatedGateBlock.Threshold,
			}
		}
		w.Retros = rc
	}
	return w, nil
}
