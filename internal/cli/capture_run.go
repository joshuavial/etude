package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// captureRunSpec is the top-level YAML document for `etude capture-run <spec.yaml>`.
// Unknown fields are rejected via KnownFields(true) in parseCaptureRunSpec.
//
// Example:
//
//	run_id: my-run-2026-05-26
//	workflow: dev
//	workflow_version: dev-v1
//	git_sha: ""          # default = HEAD
//	harness: claude-code
//	harness_version: "1.0"
//	model: claude-opus-4-7
//	refs:
//	  pr: "123"
//	stages:
//	  - stage: plan
//	    skill:
//	      id: dev-planner
//	    inputs:
//	      - role: task
//	        path: task.md
//	    output:
//	      role: plan
//	      path: plan.md
//	  - stage: implement
//	    model: claude-sonnet-4-6   # per-stage override
//	    skill:
//	      id: dev-coder
//	    inputs:
//	      - role: plan
//	        path: plan.md
//	    output:
//	      role: diff
//	      path: changes.diff
type captureRunSpec struct {
	RunID           string                `yaml:"run_id"`
	Workflow        string                `yaml:"workflow"`
	WorkflowVersion string                `yaml:"workflow_version"`
	GitSHA          string                `yaml:"git_sha"`
	Harness         string                `yaml:"harness"`
	HarnessVersion  string                `yaml:"harness_version"`
	Model           string                `yaml:"model"`
	Refs            map[string]string     `yaml:"refs"`
	Stages          []captureRunStageSpec `yaml:"stages"`
}

type captureRunStageSpec struct {
	Stage          string                   `yaml:"stage"`
	ProducedBy     string                   `yaml:"produced_by"`
	GitSHA         string                   `yaml:"git_sha"`
	Skill          captureRunSkillSpec      `yaml:"skill"`
	Harness        string                   `yaml:"harness"`
	HarnessVersion string                   `yaml:"harness_version"`
	Model          string                   `yaml:"model"`
	Inputs         []captureRunArtifactSpec `yaml:"inputs"`
	Output         captureRunArtifactSpec   `yaml:"output"`
}

type captureRunSkillSpec struct {
	ID      string `yaml:"id"`
	Repo    string `yaml:"repo"`
	Version string `yaml:"version"`
}

type captureRunArtifactSpec struct {
	Role string `yaml:"role"`
	Path string `yaml:"path"`
}

func parseCaptureRunSpec(content []byte) (captureRunSpec, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	var spec captureRunSpec
	if err := dec.Decode(&spec); err != nil {
		return captureRunSpec{}, fmt.Errorf("parse spec: %w", err)
	}
	// Reject trailing documents, mirroring workflow.ParseYAML.
	// Distinguish between EOF (clean single doc), nil (second doc present),
	// and any other error (malformed trailing content).
	var extra interface{}
	err := dec.Decode(&extra)
	if errors.Is(err, io.EOF) {
		// Clean end of stream — exactly one document, as expected.
		return spec, nil
	}
	if err == nil {
		return captureRunSpec{}, fmt.Errorf("parse spec: multiple YAML documents; expected one")
	}
	return captureRunSpec{}, fmt.Errorf("parse spec: %w", err)
}

type captureRunRunner struct {
	now    func() time.Time
	store  refstore.Store
	stdout io.Writer
}

func newCaptureRunCommand(out, errOut io.Writer) *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:           "capture-run <spec.yaml>",
		Short:         "Capture a multi-stage run from a YAML spec in one operation",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := captureRunRunner{
				now:    time.Now,
				store:  refstore.New(""),
				stdout: out,
			}
			return runner.run(cmd.Context(), args[0], message)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().StringVar(&message, "message", "", "commit message for the run ref")
	return cmd
}

func (r captureRunRunner) run(ctx context.Context, specPath, message string) error {
	// Read the spec file.
	specBytes, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec %s: %w", specPath, err)
	}

	spec, err := parseCaptureRunSpec(specBytes)
	if err != nil {
		return err
	}

	// Validate run_id — required in v1.
	if strings.TrimSpace(spec.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if !runmanifest.IsValidRunID(spec.RunID) {
		return fmt.Errorf("invalid run_id %q", spec.RunID)
	}

	// Apply run-level defaults.
	workflow := spec.Workflow
	if workflow == "" {
		workflow = defaultWorkflow
	}
	workflowVersion := spec.WorkflowVersion
	if workflowVersion == "" {
		workflowVersion = defaultWorkflowVersion
	}

	// Resolve run-level git_sha (default = HEAD).
	runGitSHA := strings.TrimSpace(spec.GitSHA)
	if runGitSHA == "" {
		runGitSHA, err = currentHEAD(ctx)
		if err != nil {
			return err
		}
	} else if err := validateGitSHA(runGitSHA); err != nil {
		return err
	}

	// Validate refs map.
	if err := validateRefsMap(spec.Refs); err != nil {
		return err
	}

	// Require at least one stage.
	if len(spec.Stages) == 0 {
		return fmt.Errorf("at least one stage is required")
	}

	// Spec file directory — artifact paths resolve relative to it.
	specDir := filepath.Dir(specPath)

	// Anchor on an absolute lexical base so filepath.Rel never errors due to a
	// relative vs. absolute mismatch (happens when specDir == "." because the
	// user ran `etude capture-run spec.yaml` from inside the spec dir).
	absSpecDir, err := filepath.Abs(specDir)
	if err != nil {
		return fmt.Errorf("resolve spec directory %s: %w", specDir, err)
	}
	// Resolve the real (symlink-free) spec dir once; used for realpath confinement.
	// specDir always exists here because os.ReadFile(specPath) succeeded above.
	realSpecDir, err := filepath.EvalSymlinks(absSpecDir)
	if err != nil {
		return fmt.Errorf("resolve spec directory %s: %w", absSpecDir, err)
	}

	// Build all stages + accumulate artifact files across stages.
	artifactStore := artifactstore.New()
	now := r.now().UTC()
	stages := make([]runmanifest.Stage, 0, len(spec.Stages))

	for i, stageSpec := range spec.Stages {
		stage, err := r.buildStage(i, stageSpec, spec, runGitSHA, absSpecDir, realSpecDir, artifactStore, now)
		if err != nil {
			return err
		}
		stages = append(stages, stage)
	}

	manifest := runmanifest.Manifest{
		RunID:           spec.RunID,
		Workflow:        workflow,
		WorkflowVersion: workflowVersion,
		Created:         now,
		Refs:            spec.Refs,
		Stages:          stages,
	}
	if manifest.Refs == nil {
		manifest.Refs = map[string]string{}
	}

	files := artifactStore.Files()

	if message == "" {
		message = fmt.Sprintf("capture-run: create run %s (%d stages)", spec.RunID, len(stages))
	}

	// Create-only write: ExpectedOld="" means fail if the ref already exists.
	written, err := runmanifest.WriteManifestTree(ctx, r.store, "refs/etude/runs/", manifest, files, refstore.WriteOptions{
		Message: message,
	})
	if err != nil {
		if errors.Is(err, refstore.ErrRefExists) {
			return fmt.Errorf("run %q already exists; use a different run_id or delete the existing run (create-only, will not clobber)", spec.RunID)
		}
		return err
	}

	ref := "refs/etude/runs/" + spec.RunID
	_, err = fmt.Fprintf(r.stdout, "captured %s\nref %s\n", written, ref)
	return err
}

func (r captureRunRunner) buildStage(
	index int,
	stageSpec captureRunStageSpec,
	runSpec captureRunSpec,
	runGitSHA string,
	absSpecDir string,
	realSpecDir string,
	store *artifactstore.Store,
	now time.Time,
) (runmanifest.Stage, error) {
	// Validate stage name.
	if err := validateCLIIdentifier("stage", stageSpec.Stage); err != nil {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d]: %w", index, err)
	}

	// Validate skill.id — required.
	if strings.TrimSpace(stageSpec.Skill.ID) == "" {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): skill.id is required", index, stageSpec.Stage)
	}

	// produced_by: default "original".
	producedBy := stageSpec.ProducedBy
	if producedBy == "" {
		producedBy = defaultProducedBy
	}

	// per-stage git_sha: fall back to run-level.
	gitSHA := strings.TrimSpace(stageSpec.GitSHA)
	if gitSHA == "" {
		gitSHA = runGitSHA
	} else if err := validateGitSHA(gitSHA); err != nil {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): %w", index, stageSpec.Stage, err)
	}

	// Skill: per-stage id required; repo/version fall back to run-level defaults.
	skillRepo := stageSpec.Skill.Repo
	if skillRepo == "" {
		skillRepo = defaultSkillRepo
	}
	skillVersion := stageSpec.Skill.Version
	if skillVersion == "" {
		skillVersion = defaultSkillVersion
	}
	skill := runmanifest.Skill{
		ID:      stageSpec.Skill.ID,
		Repo:    skillRepo,
		Version: skillVersion,
	}

	// Harness: per-stage overrides run-level.
	harness := stageSpec.Harness
	if harness == "" {
		harness = runSpec.Harness
	}
	harnessVersion := stageSpec.HarnessVersion
	if harnessVersion == "" {
		harnessVersion = runSpec.HarnessVersion
	}

	// Model: per-stage overrides run-level.
	model := stageSpec.Model
	if model == "" {
		model = runSpec.Model
	}

	// Build inputs.
	inputs := make([]runmanifest.ArtifactRef, 0, len(stageSpec.Inputs))
	for _, inputSpec := range stageSpec.Inputs {
		if inputSpec.Path == "" {
			return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): input role %q has no path", index, stageSpec.Stage, inputSpec.Role)
		}
		absPath, err := resolveArtifactPath(absSpecDir, realSpecDir, inputSpec.Path)
		if err != nil {
			return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): input: %w", index, stageSpec.Stage, err)
		}
		artifact, err := addFileArtifactFromPath(store, inputSpec.Role, absPath)
		if err != nil {
			return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): input %q: %w", index, stageSpec.Stage, inputSpec.Role, err)
		}
		inputs = append(inputs, artifact)
	}

	// Build output — exactly one required.
	if stageSpec.Output.Path == "" {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): output path is required", index, stageSpec.Stage)
	}
	absOutputPath, err := resolveArtifactPath(absSpecDir, realSpecDir, stageSpec.Output.Path)
	if err != nil {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): output: %w", index, stageSpec.Stage, err)
	}
	output, err := addFileArtifactFromPath(store, stageSpec.Output.Role, absOutputPath)
	if err != nil {
		return runmanifest.Stage{}, fmt.Errorf("stage[%d] (%s): output: %w", index, stageSpec.Stage, err)
	}

	return runmanifest.Stage{
		Name:       stageSpec.Stage,
		ProducedBy: producedBy,
		GitSHA:     gitSHA,
		Skill:      skill,
		Producer: runmanifest.Producer{
			Harness: runmanifest.Harness{
				Name:    harness,
				Version: harnessVersion,
			},
			Model: model,
			Skill: skill,
		},
		Inputs:    inputs,
		Output:    output,
		Timestamp: now,
	}, nil
}

// addFileArtifactFromPath reads an absolute file path and adds it to the artifact store.
// This is the path-absolute variant of addFileArtifact used by capture-run (where
// artifact paths in the spec are relative to the spec file's directory).
func addFileArtifactFromPath(store *artifactstore.Store, role, absPath string) (runmanifest.ArtifactRef, error) {
	if role == "" {
		return runmanifest.ArtifactRef{}, fmt.Errorf("role is required")
	}
	content, err := os.ReadFile(absPath)
	if err != nil {
		return runmanifest.ArtifactRef{}, fmt.Errorf("read %s: %w", absPath, err)
	}
	artifact, err := store.AddContent(role, inferMediaType(absPath), content)
	if err != nil {
		return runmanifest.ArtifactRef{}, err
	}
	return runmanifest.ArtifactFromManifestArtifact(artifact), nil
}

// resolveArtifactPath resolves a spec-relative artifact path against the
// directory that contains the spec file, so spec files are self-contained
// and portable as a directory.
//
// absSpecDir is the absolute (but not yet symlink-resolved) spec directory;
// realSpecDir is its realpath (symlinks resolved). Both must be absolute paths.
//
// Absolute paths are rejected — every artifact path must be relative to the
// spec dir. Relative paths that escape the spec dir via ".." are also rejected.
// A symlink inside the spec dir that resolves to a target outside the spec dir
// is also rejected (realpath confinement). Subdirectory paths (e.g.
// "artifacts/plan.md") are allowed. A symlink whose realpath stays inside the
// spec dir is accepted.
func resolveArtifactPath(absSpecDir, realSpecDir, relPath string) (string, error) {
	// Fast path: reject absolute artifact paths immediately, no FS touch.
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("path %q must be relative to the spec directory", relPath)
	}

	// Resolve against the absolute lexical base so `resolved` is always absolute.
	resolved := filepath.Clean(filepath.Join(absSpecDir, relPath))

	// Lexical confinement: cheap pre-check before any symlink follow.
	sep := string(os.PathSeparator)
	rel, err := filepath.Rel(absSpecDir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+sep) {
		return "", fmt.Errorf("path %q escapes the spec directory", relPath)
	}

	// Realpath confinement: follow symlinks and re-check against the real spec dir.
	realResolved, err := filepath.EvalSymlinks(resolved)
	if err != nil {
		// A genuinely-missing file is a user typo, not an escape — a non-existent
		// path cannot read anything outside the spec dir, so report it plainly
		// instead of masking it as a confinement violation. Any other EvalSymlinks
		// failure is denied by default (conservative, uniform security boundary).
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("artifact path %q does not exist", relPath)
		}
		return "", fmt.Errorf("path %q escapes the spec directory", relPath)
	}
	realRel, err := filepath.Rel(realSpecDir, realResolved)
	if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+sep) {
		return "", fmt.Errorf("path %q escapes the spec directory", relPath)
	}

	// Return the realpath so os.ReadFile doesn't re-follow the attacker-controlled
	// symlink name (TOCTOU mitigation).
	return realResolved, nil
}

// validateRefsMap validates the top-level refs map in the spec.
func validateRefsMap(refs map[string]string) error {
	for key, value := range refs {
		if err := validateCLIIdentifier("ref key", key); err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("ref %q value is required", key)
		}
	}
	return nil
}
