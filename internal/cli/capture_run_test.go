package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshuavial/etude/internal/refstore"
)

// writeSpecFile writes content to dir/name and returns the absolute path.
func writeSpecFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	writeFile(t, dir, name, content)
	return dir + "/" + name
}

// TestCaptureRunHappyPath3Stages verifies a 3-stage spec produces ONE run with
// all stages, correct skill/model/inputs/output/refs, run-level defaults, and
// per-stage overrides.
func TestCaptureRunHappyPath3Stages(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "task.md", "# task\n")
	writeFile(t, repo, "plan.md", "# plan\n")
	writeFile(t, repo, "diff.txt", "diff content\n")
	writeFile(t, repo, "review.md", "# review\n")
	chdir(t, repo)

	spec := `
run_id: multi-run-1
workflow: dev
workflow_version: dev-v1
harness: claude-code
harness_version: "1.0"
model: claude-opus-4-7
refs:
  pr: "42"
stages:
  - stage: plan
    skill:
      id: dev-planner
    inputs:
      - role: task
        path: task.md
    output:
      role: plan
      path: plan.md
  - stage: implement
    model: claude-sonnet-4-6
    skill:
      id: dev-coder
      repo: myrepo
      version: v2
    inputs:
      - role: plan
        path: plan.md
    output:
      role: diff
      path: diff.txt
  - stage: review
    skill:
      id: dev-reviewer
    inputs:
      - role: plan
        path: plan.md
      - role: diff
        path: diff.txt
    output:
      role: review
      path: review.md
`
	specPath := writeSpecFile(t, repo, "run.yaml", spec)

	stdout, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run returned error: %v\nstderr: %s", err, stderr)
	}
	if !strings.Contains(stdout, "captured ") || !strings.Contains(stdout, "ref refs/etude/runs/multi-run-1") {
		t.Fatalf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}

	manifest := readRunManifest(t, repo, "multi-run-1")

	// Run-level metadata.
	if manifest.RunID != "multi-run-1" {
		t.Fatalf("RunID = %q", manifest.RunID)
	}
	if manifest.Workflow != "dev" || manifest.WorkflowVersion != "dev-v1" {
		t.Fatalf("workflow = %q/%q", manifest.Workflow, manifest.WorkflowVersion)
	}
	if manifest.Refs["pr"] != "42" {
		t.Fatalf("refs = %#v", manifest.Refs)
	}
	if manifest.Created.IsZero() {
		t.Fatal("Created is zero")
	}

	// Exactly 3 stages.
	if len(manifest.Stages) != 3 {
		t.Fatalf("stages count = %d, want 3", len(manifest.Stages))
	}

	// Stage 0: plan — run-level model.
	s0 := manifest.Stages[0]
	if s0.Name != "plan" {
		t.Fatalf("stage[0].Name = %q", s0.Name)
	}
	if s0.ProducedBy != defaultProducedBy {
		t.Fatalf("stage[0].ProducedBy = %q", s0.ProducedBy)
	}
	if s0.Skill.ID != "dev-planner" || s0.Skill.Repo != defaultSkillRepo || s0.Skill.Version != defaultSkillVersion {
		t.Fatalf("stage[0].Skill = %#v", s0.Skill)
	}
	if s0.Producer.Skill != s0.Skill {
		t.Fatalf("stage[0] mirror invariant violated: Skill=%#v Producer.Skill=%#v", s0.Skill, s0.Producer.Skill)
	}
	if s0.Producer.Model != "claude-opus-4-7" {
		t.Fatalf("stage[0].Producer.Model = %q, want run-level default", s0.Producer.Model)
	}
	if s0.Producer.Harness.Name != "claude-code" {
		t.Fatalf("stage[0].Producer.Harness.Name = %q", s0.Producer.Harness.Name)
	}
	if s0.Output.Role != "plan" || s0.Output.MediaType != "text/markdown; charset=utf-8" {
		t.Fatalf("stage[0].Output = %#v", s0.Output)
	}
	if len(s0.Inputs) != 1 || s0.Inputs[0].Role != "task" {
		t.Fatalf("stage[0].Inputs = %#v", s0.Inputs)
	}

	// Stage 1: implement — per-stage model override + per-stage skill repo/version.
	s1 := manifest.Stages[1]
	if s1.Name != "implement" {
		t.Fatalf("stage[1].Name = %q", s1.Name)
	}
	if s1.Producer.Model != "claude-sonnet-4-6" {
		t.Fatalf("stage[1].Producer.Model = %q, want per-stage override", s1.Producer.Model)
	}
	if s1.Skill.ID != "dev-coder" || s1.Skill.Repo != "myrepo" || s1.Skill.Version != "v2" {
		t.Fatalf("stage[1].Skill = %#v", s1.Skill)
	}

	// Stage 2: review — 2 inputs.
	s2 := manifest.Stages[2]
	if len(s2.Inputs) != 2 {
		t.Fatalf("stage[2] inputs = %d, want 2", len(s2.Inputs))
	}

	// Verify all artifacts are readable back.
	store := refstore.New(repo)
	for _, stage := range manifest.Stages {
		if _, err := store.ReadFile(context.Background(), "refs/etude/runs/multi-run-1", stage.Output.Path); err != nil {
			t.Fatalf("output artifact %s not readable: %v", stage.Output.Path, err)
		}
		for _, inp := range stage.Inputs {
			if _, err := store.ReadFile(context.Background(), "refs/etude/runs/multi-run-1", inp.Path); err != nil {
				t.Fatalf("input artifact %s not readable: %v", inp.Path, err)
			}
		}
	}
}

// TestCaptureRunGitSHADefaultAndOverride verifies HEAD default + per-stage override.
func TestCaptureRunGitSHADefaultAndOverride(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	sha := strings.Repeat("a", 40)
	spec := `
run_id: sha-test
workflow: manual
workflow_version: manual-v1
stages:
  - stage: plan
    git_sha: ` + sha + `
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "sha-run.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "sha-test")
	if manifest.Stages[0].GitSHA != sha {
		t.Fatalf("stage git_sha = %q, want %q", manifest.Stages[0].GitSHA, sha)
	}
}

// TestCaptureRunRunLevelGitSHAAppliedToAllStages verifies that a run-level git_sha
// is applied to all stages that don't override it.
func TestCaptureRunRunLevelGitSHAAppliedToAllStages(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out1.md", "one\n")
	writeFile(t, repo, "out2.md", "two\n")
	chdir(t, repo)

	sha := strings.Repeat("b", 40)
	spec := `
run_id: runlevel-sha
git_sha: ` + sha + `
stages:
  - stage: plan
    skill:
      id: planner
    output:
      role: plan
      path: out1.md
  - stage: implement
    skill:
      id: coder
    output:
      role: diff
      path: out2.md
`
	specPath := writeSpecFile(t, repo, "runlevel-sha.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "runlevel-sha")
	for i, stage := range manifest.Stages {
		if stage.GitSHA != sha {
			t.Fatalf("stage[%d].GitSHA = %q, want %q", i, stage.GitSHA, sha)
		}
	}
}

// TestCaptureRunKnownFieldsRejectsUnknownField verifies that KnownFields(true)
// rejects a YAML spec with an unknown field.
func TestCaptureRunKnownFieldsRejectsUnknownField(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	spec := `
run_id: bad-spec
unknown_field: should-be-rejected
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "bad.yaml", spec)
	_, _, err := execute("capture-run", specPath)
	if err == nil {
		t.Fatal("capture-run returned nil error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown_field") && !strings.Contains(err.Error(), "parse spec") {
		t.Fatalf("error %q does not mention unknown field or parse spec", err.Error())
	}
}

// TestCaptureRunErrorCases tests the full set of error paths.
func TestCaptureRunErrorCases(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	validStageYAML := `  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`

	cases := []struct {
		name     string
		specYAML string
		want     string
		args     []string // override args; if nil, use a temp spec file
	}{
		{
			name: "missing spec file",
			args: []string{"capture-run", "/nonexistent/path/spec.yaml"},
			want: "read spec",
		},
		{
			name:     "missing run_id",
			specYAML: "workflow: manual\nworkflow_version: manual-v1\nstages:\n" + validStageYAML,
			want:     "run_id is required",
		},
		{
			name:     "invalid run_id",
			specYAML: "run_id: \"bad/run/id\"\nstages:\n" + validStageYAML,
			want:     "invalid run_id",
		},
		{
			name:     "stage missing skill.id",
			specYAML: "run_id: r1\nstages:\n  - stage: plan\n    skill:\n      id: \"\"\n    output:\n      role: plan\n      path: out.md\n",
			want:     "skill.id is required",
		},
		{
			name:     "output missing path",
			specYAML: "run_id: r1\nstages:\n  - stage: plan\n    skill:\n      id: planner\n    output:\n      role: plan\n      path: \"\"\n",
			want:     "output path is required",
		},
		{
			// A genuinely-missing artifact path is reported as a plain "does not
			// exist" typo error, not masked as a confinement violation (EvalSymlinks
			// fails with os.ErrNotExist, which the resolver distinguishes).
			name:     "nonexistent artifact path",
			specYAML: "run_id: r1\nstages:\n  - stage: plan\n    skill:\n      id: planner\n    output:\n      role: plan\n      path: missing-file.md\n",
			want:     "does not exist",
		},
		{
			name:     "zero stages",
			specYAML: "run_id: r1\nstages: []\n",
			want:     "at least one stage is required",
		},
		{
			name:     "malformed YAML",
			specYAML: "run_id: [\nbad yaml",
			want:     "parse spec",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if args == nil {
				specPath := writeSpecFile(t, repo, "test-spec.yaml", tc.specYAML)
				args = []string{"capture-run", specPath}
			}
			_, stderr, err := execute(args...)
			if err == nil {
				t.Fatal("capture-run returned nil error")
			}
			combined := err.Error() + " " + stderr
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("error %q does not contain %q", combined, tc.want)
			}
		})
	}
}

// TestCaptureRunCreateOnlyCollision verifies that writing the same run_id twice
// returns an ErrRefExists-based error (does not clobber).
func TestCaptureRunCreateOnlyCollision(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	spec := `
run_id: collision-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "collision.yaml", spec)

	// First write succeeds.
	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("first capture-run error: %v\nstderr: %s", err, stderr)
	}

	// Second write must fail with a clear collision error.
	_, _, err = execute("capture-run", specPath)
	if err == nil {
		t.Fatal("second capture-run returned nil error (should be collision)")
	}
	if !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "ref exists") {
		t.Fatalf("error %q does not mention 'already exists' or 'ref exists'", err.Error())
	}

	// Verify the run still has only ONE commit (not overwritten).
	manifest := readRunManifest(t, repo, "collision-run")
	if len(manifest.Stages) != 1 {
		t.Fatalf("stages count = %d after collision, want 1 (should not be overwritten)", len(manifest.Stages))
	}
}

// TestCaptureRunWorkflowDefaults verifies that omitting workflow/workflow_version
// in the spec applies "manual" / "manual-v1" defaults.
func TestCaptureRunWorkflowDefaults(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	spec := `
run_id: default-wf
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "default-wf.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "default-wf")
	if manifest.Workflow != defaultWorkflow {
		t.Fatalf("Workflow = %q, want %q", manifest.Workflow, defaultWorkflow)
	}
	if manifest.WorkflowVersion != defaultWorkflowVersion {
		t.Fatalf("WorkflowVersion = %q, want %q", manifest.WorkflowVersion, defaultWorkflowVersion)
	}
}

// TestCaptureRunRefsMerged verifies that top-level refs are recorded on the manifest.
func TestCaptureRunRefsMerged(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	spec := `
run_id: refs-run
refs:
  pr: "999"
  branch: main
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "refs-run.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "refs-run")
	if manifest.Refs["pr"] != "999" || manifest.Refs["branch"] != "main" {
		t.Fatalf("refs = %#v", manifest.Refs)
	}
}

// TestCaptureRunMediaTypeInferred verifies that artifact media types are inferred
// from the file extension.
func TestCaptureRunMediaTypeInferred(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "diff.patch", "--- a\n+++ b\n")
	writeFile(t, repo, "out.json", `{"key":"val"}`)
	chdir(t, repo)

	spec := `
run_id: media-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    inputs:
      - role: patch
        path: diff.patch
    output:
      role: result
      path: out.json
`
	specPath := writeSpecFile(t, repo, "media.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "media-run")
	stage := manifest.Stages[0]
	if stage.Inputs[0].MediaType != "text/x-diff; charset=utf-8" {
		t.Fatalf("input media type = %q, want text/x-diff", stage.Inputs[0].MediaType)
	}
	if stage.Output.MediaType != "application/json" {
		t.Fatalf("output media type = %q, want application/json", stage.Output.MediaType)
	}
}

// TestCaptureRunProducedByDefaultAndOverride verifies "original" default + per-stage override.
func TestCaptureRunProducedByDefaultAndOverride(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	writeFile(t, repo, "out2.md", "output2\n")
	chdir(t, repo)

	spec := `
run_id: produced-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
  - stage: implement
    produced_by: manual
    skill:
      id: dev-coder
    output:
      role: diff
      path: out2.md
`
	specPath := writeSpecFile(t, repo, "produced.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "produced-run")
	if manifest.Stages[0].ProducedBy != defaultProducedBy {
		t.Fatalf("stage[0].ProducedBy = %q, want %q", manifest.Stages[0].ProducedBy, defaultProducedBy)
	}
	if manifest.Stages[1].ProducedBy != "manual" {
		t.Fatalf("stage[1].ProducedBy = %q, want manual", manifest.Stages[1].ProducedBy)
	}
}

// TestCaptureRunArtifactsRelativeToSpecDir verifies that artifact paths in the
// spec are resolved relative to the spec file's directory, not the cwd.
func TestCaptureRunArtifactsRelativeToSpecDir(t *testing.T) {
	repo := initCaptureRepo(t)
	// Write the spec and artifact in a subdirectory, run from a different cwd.
	writeFile(t, repo, "subdir/out.md", "subdir output\n")
	chdir(t, repo)

	spec := `
run_id: relpath-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
`
	specPath := writeSpecFile(t, repo, "subdir/run.yaml", spec)

	_, stderr, err := execute("capture-run", specPath)
	if err != nil {
		t.Fatalf("capture-run error: %v\nstderr: %s", err, stderr)
	}

	manifest := readRunManifest(t, repo, "relpath-run")
	content, err := refstore.New(repo).ReadFile(context.Background(), "refs/etude/runs/relpath-run", manifest.Stages[0].Output.Path)
	if err != nil {
		t.Fatalf("artifact not readable: %v", err)
	}
	if string(content) != "subdir output\n" {
		t.Fatalf("artifact content = %q", content)
	}
}

// TestCaptureRunArtifactPathContainment verifies that absolute and escaping
// paths are rejected, and that subdir-relative paths are accepted.
func TestCaptureRunArtifactPathContainment(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	writeFile(t, repo, "sub/out.md", "subdir output\n")
	chdir(t, repo)

	t.Run("absolute output path rejected", func(t *testing.T) {
		spec := `
run_id: abs-path-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: /etc/passwd
`
		specPath := writeSpecFile(t, repo, "abs.yaml", spec)
		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted absolute path; want error")
		}
		if !strings.Contains(err.Error(), "must be relative to the spec directory") {
			t.Fatalf("error %q does not mention 'must be relative to the spec directory'", err.Error())
		}
	})

	t.Run("escaping relative output path rejected", func(t *testing.T) {
		spec := `
run_id: escape-path-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: ../escape.md
`
		specPath := writeSpecFile(t, repo, "escape.yaml", spec)
		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted escaping path; want error")
		}
		if !strings.Contains(err.Error(), "escapes the spec directory") {
			t.Fatalf("error %q does not mention 'escapes the spec directory'", err.Error())
		}
	})

	t.Run("absolute input path rejected", func(t *testing.T) {
		spec := `
run_id: abs-input-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    inputs:
      - role: task
        path: /etc/passwd
    output:
      role: plan
      path: out.md
`
		specPath := writeSpecFile(t, repo, "abs-input.yaml", spec)
		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted absolute input path; want error")
		}
		if !strings.Contains(err.Error(), "must be relative to the spec directory") {
			t.Fatalf("error %q does not mention 'must be relative to the spec directory'", err.Error())
		}
	})

	t.Run("subdir relative path accepted", func(t *testing.T) {
		spec := `
run_id: subdir-path-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: sub/out.md
`
		specPath := writeSpecFile(t, repo, "subdir.yaml", spec)
		_, stderr, err := execute("capture-run", specPath)
		if err != nil {
			t.Fatalf("capture-run rejected subdir relative path: %v\nstderr: %s", err, stderr)
		}
	})
}

// TestCaptureRunTrailingDocParsing verifies trailing-document detection:
// a clean single doc succeeds, a second valid document errors, and a
// malformed trailing document (after ---) errors rather than being swallowed.
func TestCaptureRunTrailingDocParsing(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	t.Run("second valid document rejected", func(t *testing.T) {
		spec := `
run_id: trailing-run
stages:
  - stage: plan
    skill:
      id: dev-planner
    output:
      role: plan
      path: out.md
---
run_id: second-doc
stages: []
`
		specPath := writeSpecFile(t, repo, "two-docs.yaml", spec)
		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted two documents; want error")
		}
		if !strings.Contains(err.Error(), "multiple YAML documents") {
			t.Fatalf("error %q does not mention 'multiple YAML documents'", err.Error())
		}
	})

	t.Run("malformed trailing document errors", func(t *testing.T) {
		// Valid first doc followed by --- and malformed YAML.
		spec := "run_id: malformed-trailing\nstages:\n  - stage: plan\n    skill:\n      id: dev-planner\n    output:\n      role: plan\n      path: out.md\n---\n: bad: yaml: [\n"
		specPath := writeSpecFile(t, repo, "malformed-trailing.yaml", spec)
		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run silently accepted malformed trailing document; want error")
		}
		if !strings.Contains(err.Error(), "parse spec") {
			t.Fatalf("error %q does not mention 'parse spec'", err.Error())
		}
	})
}

// TestCaptureRunArtifactPathSymlinkContainment verifies that symlink-based path
// escapes are caught by the realpath confinement layer, that symlinks pointing
// inside the spec dir are accepted, and that relative-spec invocation works
// correctly (the PLAN-GATE CORRECTION regression test).
func TestCaptureRunArtifactPathSymlinkContainment(t *testing.T) {
	repo := initCaptureRepo(t)

	// Create an "outside" file we'll use as a symlink target for negative tests.
	writeFile(t, repo, "secret.md", "this must not leak\n")

	// specDir lives inside repo so we can write spec + artifacts next to each other.
	writeFile(t, repo, "specdir/placeholder", "")

	chdir(t, repo)

	// Helper that skips if symlink creation is unsupported on this FS.
	mustSymlink := func(t *testing.T, oldname, newname string) {
		t.Helper()
		if err := os.Symlink(oldname, newname); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
	}

	specTemplate := func(runID, outputPath string) string {
		return "run_id: " + runID + "\nstages:\n  - stage: plan\n    skill:\n      id: planner\n    output:\n      role: plan\n      path: " + outputPath + "\n"
	}

	t.Run("symlink_inside_to_absolute_outside_rejected", func(t *testing.T) {
		// Create a real file outside the spec dir to target (self-contained — no host dep).
		writeFile(t, repo, "outside-a.md", "outside content\n")
		outsideAbs := repo + "/outside-a.md"

		// Symlink inside specdir pointing to the absolute outside path.
		mustSymlink(t, outsideAbs, repo+"/specdir/leak-a.md")

		spec := specTemplate("sym-abs-"+t.Name()[len("TestCaptureRunArtifactPathSymlinkContainment/"):], "leak-a.md")
		specPath := writeSpecFile(t, repo, "specdir/sym-abs.yaml", spec)

		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted symlink pointing to absolute outside path; want error")
		}
		if !strings.Contains(err.Error(), "escapes the spec directory") {
			t.Fatalf("error %q does not contain 'escapes the spec directory'", err.Error())
		}
	})

	t.Run("symlink_inside_to_relative_outside_rejected", func(t *testing.T) {
		// secret.md already lives at repo root, specdir is one level down.
		// "../secret.md" from inside specdir resolves to repo/secret.md — outside specdir.
		mustSymlink(t, "../secret.md", repo+"/specdir/leak-b.md")

		spec := specTemplate("sym-rel-outside", "leak-b.md")
		specPath := writeSpecFile(t, repo, "specdir/sym-rel.yaml", spec)

		_, _, err := execute("capture-run", specPath)
		if err == nil {
			t.Fatal("capture-run accepted symlink with relative escape; want error")
		}
		if !strings.Contains(err.Error(), "escapes the spec directory") {
			t.Fatalf("error %q does not contain 'escapes the spec directory'", err.Error())
		}
	})

	t.Run("symlink_inside_to_inside_accepted", func(t *testing.T) {
		// Create a real file deeper inside the spec dir, then symlink to it from the top.
		writeFile(t, repo, "specdir/sub/b.md", "real content inside\n")
		// specdir/a.md -> sub/b.md  (relative, stays inside specdir)
		mustSymlink(t, "sub/b.md", repo+"/specdir/a.md")

		spec := specTemplate("sym-inside", "a.md")
		specPath := writeSpecFile(t, repo, "specdir/sym-inside.yaml", spec)

		_, stderr, err := execute("capture-run", specPath)
		if err != nil {
			t.Fatalf("capture-run rejected inside-pointing symlink: %v\nstderr: %s", err, stderr)
		}
	})

	t.Run("relative_spec_path_with_abs_target_inside_accepted", func(t *testing.T) {
		// PLAN-GATE CORRECTION regression test:
		// Invoke capture-run with a RELATIVE spec path (specDir == ".") and an
		// artifact that is a symlink with an ABSOLUTE target pointing INSIDE the
		// spec dir. Without the filepath.Abs fix this falsely rejects because
		// EvalSymlinks(".") returns "." while the resolved symlink target is
		// absolute, causing filepath.Rel(".", "/abs/...") to succeed but compare
		// incorrectly or error.

		// Make a subdirectory that will be both specdir and cwd.
		writeFile(t, repo, "relspecdir/real.md", "real inside content\n")

		// Absolute path of real.md inside relspecdir.
		absTarget, err := filepath.Abs(repo + "/relspecdir/real.md")
		if err != nil {
			t.Fatalf("Abs: %v", err)
		}
		// Symlink inside relspecdir with absolute target also inside relspecdir.
		mustSymlink(t, absTarget, repo+"/relspecdir/abs-inside.md")

		spec := specTemplate("sym-abs-inside", "abs-inside.md")
		specPath := writeSpecFile(t, repo, "relspecdir/relspec.yaml", spec)

		// chdir into the spec dir and pass a relative spec path (specDir == ".").
		origDir, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if err := os.Chdir(repo + "/relspecdir"); err != nil {
			t.Fatalf("Chdir: %v", err)
		}
		t.Cleanup(func() { os.Chdir(origDir) })

		// Use a relative spec path so specDir == "." inside run().
		_ = specPath // absolute path available but we want relative
		_, stderr, err := execute("capture-run", "relspec.yaml")
		if err != nil {
			t.Fatalf("capture-run rejected abs-target-inside symlink with relative spec path: %v\nstderr: %s", err, stderr)
		}
	})
}

// TestCaptureRunHelp verifies the command shows up with capture-run usage.
func TestCaptureRunHelp(t *testing.T) {
	stdout, _, err := execute("capture-run", "--help")
	if err != nil {
		t.Fatalf("--help error: %v", err)
	}
	if !strings.Contains(stdout, "capture-run") {
		t.Fatalf("help output does not mention capture-run: %q", stdout)
	}
}

// TestCaptureRunInvalidRunID verifies specific invalid run_id formats.
func TestCaptureRunInvalidRunID(t *testing.T) {
	repo := initCaptureRepo(t)
	writeFile(t, repo, "out.md", "output\n")
	chdir(t, repo)

	invalidIDs := []struct {
		name  string
		runID string
	}{
		{"leading dot", ".bad-run"},
		{"trailing dot", "bad-run."},
		{"double dot", "bad..run"},
		{"ends with lock", "bad-run.lock"},
		{"spaces", "bad run"},
		{"slash", "bad/run"},
	}

	for _, tc := range invalidIDs {
		t.Run(tc.name, func(t *testing.T) {
			spec := "run_id: " + tc.runID + "\nstages:\n  - stage: plan\n    skill:\n      id: planner\n    output:\n      role: plan\n      path: out.md\n"
			specPath := writeSpecFile(t, repo, "inv.yaml", spec)
			_, _, err := execute("capture-run", specPath)
			if err == nil {
				t.Fatalf("capture-run accepted invalid run_id %q", tc.runID)
			}
		})
	}
}
