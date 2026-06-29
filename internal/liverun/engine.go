package liverun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/artifactstore"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/joshuavial/etude/internal/worktree"
)

const runsPrefix = "refs/etude/runs/"

// StageError records a stage execution failure with the run id so callers can
// print a --resume hint.
type StageError struct {
	StageName string
	RunID     string
	Err       error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("stage %q failed: %v", e.StageName, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }

// roleArtifact pairs a content-addressed ArtifactRef with its raw bytes.
type roleArtifact struct {
	ref     runmanifest.ArtifactRef
	content []byte
}

// RunOptions configures a call to Engine.Run.
type RunOptions struct {
	// TaskBytes is the task input content. Required unless ResumeID is set.
	TaskBytes []byte
	// TaskFile is the filename used to infer the task media type.
	TaskFile string
	// RunID is an explicit run id; auto-generated if empty.
	RunID string
	// GitSHA is the git commit SHA; defaults to HEAD if empty.
	GitSHA string
	// ResumeID, when non-empty, resumes an existing partial run.
	// TaskBytes, TaskFile, RunID, and GitSHA are ignored in resume mode.
	ResumeID string
}

// Engine executes a live workflow run.
type Engine struct {
	// Store is the refstore for CAS commits.
	Store refstore.Store
	// ResolveRunner returns a runner for the given stage.
	// Tests inject a StubRunner; production code resolves from workflow/registry config.
	ResolveRunner func(stage workflow.Stage) (replay.Runner, error)
	// Root is the repository root directory used for worktree checkout and HEAD resolution.
	Root string
	// Now returns the current time. Defaults to time.Now when nil.
	Now func() time.Time
}

func (e *Engine) clock() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Run executes the workflow, capturing each stage incrementally via CAS.
// If opts.ResumeID is non-empty, resumes an existing partial run from its frontier.
func (e *Engine) Run(ctx context.Context, out io.Writer, wf workflow.Workflow, opts RunOptions) error {
	if opts.ResumeID != "" {
		return e.resume(ctx, out, wf, opts.ResumeID)
	}
	return e.startFresh(ctx, out, wf, opts)
}

func (e *Engine) startFresh(ctx context.Context, out io.Writer, wf workflow.Workflow, opts RunOptions) error {
	runID := opts.RunID
	if runID == "" {
		var err error
		runID, err = GenerateRunID(wf.Name)
		if err != nil {
			return err
		}
	} else if !runmanifest.IsValidRunID(runID) {
		// An explicit --run-id override must pass the same validation as a
		// generated id before it reaches any git ref path (rejects path
		// traversal, .lock, leading/trailing dots, bad charset).
		return fmt.Errorf("invalid run id %q", runID)
	}

	gitSHA := opts.GitSHA
	if gitSHA == "" {
		var err error
		gitSHA, err = resolveHEAD(ctx, e.Root)
		if err != nil {
			return err
		}
	}

	wt, err := worktree.Checkout(ctx, e.Root, gitSHA)
	if err != nil {
		switch {
		case errors.Is(err, worktree.ErrInvalidSHA):
			return fmt.Errorf("invalid git sha %q: %w", gitSHA, err)
		case errors.Is(err, worktree.ErrSHANotFound):
			return fmt.Errorf("git sha %q not found in repository", gitSHA)
		default:
			return fmt.Errorf("checkout %q: %w", gitSHA, err)
		}
	}
	defer wt.Close()

	scratch, err := os.MkdirTemp("", "etude-live-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	as := artifactstore.New()
	chain := make(map[string]roleArtifact)

	// Seed task into store and chain (if provided).
	if len(opts.TaskBytes) > 0 {
		mediaType := inferTaskMediaType(opts.TaskFile)
		taskArtifact, err := as.AddContent("task", mediaType, opts.TaskBytes)
		if err != nil {
			return fmt.Errorf("store task: %w", err)
		}
		taskRef := runmanifest.ArtifactFromManifestArtifact(taskArtifact)
		chain["task"] = roleArtifact{ref: taskRef, content: opts.TaskBytes}
	}

	return e.executeStages(ctx, out, wf, runID, gitSHA, e.clock(), as, chain, "", 0, nil, wt.Dir, scratch)
}

func (e *Engine) resume(ctx context.Context, out io.Writer, wf workflow.Workflow, resumeID string) error {
	ref := runsPrefix + resumeID
	commit, err := e.Store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return fmt.Errorf("run %q not found", resumeID)
		}
		return fmt.Errorf("resolve run %q: %w", resumeID, err)
	}

	manifestBytes, err := e.Store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return fmt.Errorf("read manifest for run %q: %w", resumeID, err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("parse manifest for run %q: %w", resumeID, err)
	}

	frontier := DeriveFrontier(wf, manifest)
	if frontier >= len(wf.Stages) {
		return fmt.Errorf("run %q is already complete (%d stages done)", resumeID, len(wf.Stages))
	}
	if len(manifest.Stages) == 0 {
		return fmt.Errorf("run %q has no completed stages to resume from", resumeID)
	}
	gitSHA := manifest.Stages[0].GitSHA

	wt, err := worktree.Checkout(ctx, e.Root, gitSHA)
	if err != nil {
		return fmt.Errorf("checkout %q for resume: %w", gitSHA, err)
	}
	defer wt.Close()

	scratch, err := os.MkdirTemp("", "etude-live-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	// Re-seed store and chain from all referenced artifact paths in the manifest.
	as := artifactstore.New()
	chain := make(map[string]roleArtifact)

	refByPath := make(map[string]runmanifest.ArtifactRef)
	for _, ms := range manifest.Stages {
		for _, inp := range ms.Inputs {
			refByPath[inp.Path] = inp
		}
		refByPath[ms.Output.Path] = ms.Output
	}

	rawBytes := make(map[string][]byte)
	for _, path := range runmanifest.ArtifactPaths(manifest) {
		data, err := e.Store.ReadCommitFile(ctx, commit, path)
		if err != nil {
			return fmt.Errorf("reseed artifact %q: %w", path, err)
		}
		rawBytes[path] = data
		ref := refByPath[path]
		if _, err := as.AddContent(ref.Role, ref.MediaType, data); err != nil {
			return fmt.Errorf("reseed store path %q: %w", path, err)
		}
	}

	// Build chain: stage outputs first, then any remaining input roles (e.g. "task").
	for _, ms := range manifest.Stages {
		chain[ms.Output.Role] = roleArtifact{ref: ms.Output, content: rawBytes[ms.Output.Path]}
	}
	for _, ms := range manifest.Stages {
		for _, inp := range ms.Inputs {
			if _, ok := chain[inp.Role]; !ok {
				chain[inp.Role] = roleArtifact{ref: inp, content: rawBytes[inp.Path]}
			}
		}
	}

	return e.executeStages(ctx, out, wf, manifest.RunID, gitSHA, manifest.Created, as, chain, commit, frontier, manifest.Stages, wt.Dir, scratch)
}

// executeStages runs wf.Stages[frontier:], accumulating CAS commits.
// preCompleted holds the already-committed stages from a resume (nil for fresh runs).
func (e *Engine) executeStages(
	ctx context.Context,
	out io.Writer,
	wf workflow.Workflow,
	runID, gitSHA string,
	created time.Time,
	as *artifactstore.Store,
	chain map[string]roleArtifact,
	prevCommit string,
	frontier int,
	preCompleted []runmanifest.Stage,
	worktreeDir, scratch string,
) error {
	completedStages := make([]runmanifest.Stage, len(preCompleted), len(wf.Stages))
	copy(completedStages, preCompleted)

	for i, stage := range wf.Stages[frontier:] {
		stageIdx := frontier + i

		// Gate block is a NO-OP in this bead (etude-04i executes gates).
		if stage.Gate != nil {
			fmt.Fprintf(out, "gate %q deferred to etude-04i\n", stage.Name)
		}

		// Build inputs from chain.
		var inputRefs []runmanifest.ArtifactRef
		var runInputs []replay.RunInput
		for _, role := range stage.Inputs {
			if role == "repo-state" {
				continue // implicit worktree; not materialized or recorded as ArtifactRef
			}
			ra, ok := chain[role]
			if !ok {
				return &StageError{
					StageName: stage.Name,
					RunID:     runID,
					Err:       fmt.Errorf("input role %q not available in chain", role),
				}
			}
			inputRefs = append(inputRefs, ra.ref)
			runInputs = append(runInputs, replay.RunInput{
				Role:      role,
				MediaType: ra.ref.MediaType,
				Content:   ra.content,
			})
		}

		runner, err := e.ResolveRunner(stage)
		if err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: err}
		}

		stageSkill := runmanifest.Skill{
			ID:      stage.Skill,
			Repo:    "manual",
			Version: "manual",
		}
		producer := runmanifest.Producer{Skill: stageSkill}

		// Per-stage scratch subdir avoids output file collision between stages.
		stageScratch := fmt.Sprintf("%s/stage%02d", scratch, stageIdx)
		if err := os.MkdirAll(stageScratch, 0o755); err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: fmt.Errorf("mkdir stage scratch: %w", err)}
		}

		res, err := runner.Run(ctx, replay.RunRequest{
			WorktreeDir:     worktreeDir,
			ScratchDir:      stageScratch,
			Inputs:          runInputs,
			OutputRole:      stage.Produces,
			OutputMediaType: "application/octet-stream",
			Producer:        producer,
		})
		if err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: err}
		}

		outputMediaType := res.MediaType
		if outputMediaType == "" {
			outputMediaType = "application/octet-stream"
		}
		outputArtifact, err := as.AddContent(stage.Produces, outputMediaType, res.Output)
		if err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: fmt.Errorf("store output: %w", err)}
		}
		outputRef := runmanifest.ArtifactFromManifestArtifact(outputArtifact)
		chain[stage.Produces] = roleArtifact{ref: outputRef, content: res.Output}

		completedStages = append(completedStages, runmanifest.Stage{
			Name:       stage.Name,
			ProducedBy: "original",
			GitSHA:     gitSHA,
			Skill:      stageSkill,
			Producer:   producer,
			Inputs:     inputRefs,
			Output:     outputRef,
			Timestamp:  e.clock(),
		})

		manifest := runmanifest.Manifest{
			RunID:           runID,
			Workflow:        wf.Name,
			WorkflowVersion: wf.Name + "-v1",
			Created:         created,
			Refs:            map[string]string{},
			Stages:          completedStages,
		}

		newCommit, err := runmanifest.WriteManifestTree(
			ctx, e.Store, runsPrefix, manifest,
			filesForManifest(manifest, as),
			refstore.WriteOptions{
				ExpectedOld: prevCommit,
				Message:     fmt.Sprintf("live run %s: stage %s", runID, stage.Name),
			},
		)
		if err != nil {
			return &StageError{StageName: stage.Name, RunID: runID, Err: fmt.Errorf("write manifest: %w", err)}
		}
		prevCommit = newCommit
		fmt.Fprintf(out, "captured %s\n", newCommit)
	}

	fmt.Fprintf(out, "ref %s%s\n", runsPrefix, runID)
	return nil
}

// filesForManifest returns only the artifact files referenced by the manifest.
// WriteManifestTree rejects any unreferenced files, so we must not pass extras.
func filesForManifest(manifest runmanifest.Manifest, as *artifactstore.Store) map[string][]byte {
	paths := runmanifest.ArtifactPaths(manifest)
	allFiles := as.Files()
	files := make(map[string][]byte, len(paths))
	for _, p := range paths {
		if content, ok := allFiles[p]; ok {
			files[p] = content
		}
	}
	return files
}

// inferTaskMediaType returns a media type for a task file based on its extension.
func inferTaskMediaType(filePath string) string {
	lower := strings.ToLower(filePath)
	switch {
	case strings.HasSuffix(lower, ".txt"):
		return "text/plain; charset=utf-8"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "text/markdown; charset=utf-8"
	case strings.HasSuffix(lower, ".json"):
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// resolveHEAD returns the HEAD commit SHA of the git repository at root.
func resolveHEAD(ctx context.Context, root string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: ensure the repo has at least one commit or pass --git-sha")
	}
	return strings.TrimSpace(string(out)), nil
}
