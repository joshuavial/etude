package cli

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
	"github.com/joshuavial/etude/internal/worktree"
	"github.com/spf13/cobra"
)

type replayRunner struct {
	// runner is the Runner to use. If nil, an ExecRunner is built from the
	// resolved --runner spec at run time. Tests inject a *replay.StubRunner here.
	runner replay.Runner
	// now returns the current time. Defaults to time.Now; tests inject a fixed clock
	// to make replay run ids deterministic.
	now func() time.Time
}

func newReplayCommand(out, errOut io.Writer) *cobra.Command {
	return buildReplayCommand(out, errOut, &replayRunner{now: time.Now})
}

// buildReplayCommand constructs the replay cobra.Command backed by r. Tests
// call this directly to inject a pre-configured replayRunner (e.g. one with a
// StubRunner already set); production callers use newReplayCommand.
func buildReplayCommand(out, errOut io.Writer, r *replayRunner) *cobra.Command {
	var runnerSpec string
	var outputPath string
	var record bool
	var skillVersion, skillID, skillRepo, model, harness, harnessVersion string

	cmd := &cobra.Command{
		Use:           "replay <run> <stage>",
		Short:         "Replay a recorded stage end-to-end",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			producerFlags := replayProducerFlags{
				skillIDChanged:        cmd.Flags().Changed("skill-id"),
				skillRepoChanged:      cmd.Flags().Changed("skill-repo"),
				skillVersionChanged:   cmd.Flags().Changed("skill-version"),
				modelChanged:          cmd.Flags().Changed("model"),
				harnessChanged:        cmd.Flags().Changed("harness"),
				harnessVersionChanged: cmd.Flags().Changed("harness-version"),
				skillID:               skillID,
				skillRepo:             skillRepo,
				skillVersion:          skillVersion,
				model:                 model,
				harness:               harness,
				harnessVersion:        harnessVersion,
			}
			return r.run(cmd.Context(), out, args[0], args[1], runnerSpec, outputPath, record, producerFlags)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().StringVar(&runnerSpec, "runner", "", "runner command spec (e.g. ./run.sh)")
	cmd.Flags().StringVar(&outputPath, "output", "", "write output to this file instead of stdout")
	cmd.Flags().BoolVar(&record, "record", false, "persist the replay output as a new linked run")
	cmd.Flags().StringVar(&skillVersion, "skill-version", "", "override skill version in recorded producer")
	cmd.Flags().StringVar(&skillID, "skill-id", "", "override skill id in recorded producer")
	cmd.Flags().StringVar(&skillRepo, "skill-repo", "", "override skill repo in recorded producer")
	cmd.Flags().StringVar(&model, "model", "", "override model in recorded producer")
	cmd.Flags().StringVar(&harness, "harness", "", "override harness name in recorded producer")
	cmd.Flags().StringVar(&harnessVersion, "harness-version", "", "override harness version in recorded producer")
	return cmd
}

// replayProducerFlags carries the producer-override flag values and change
// indicators so the run method can merge source producer with overrides.
type replayProducerFlags struct {
	skillIDChanged        bool
	skillRepoChanged      bool
	skillVersionChanged   bool
	modelChanged          bool
	harnessChanged        bool
	harnessVersionChanged bool

	skillID        string
	skillRepo      string
	skillVersion   string
	model          string
	harness        string
	harnessVersion string
}

func (r *replayRunner) run(ctx context.Context, out io.Writer, runID, stageName, runnerSpec, outputPath string, record bool, pf replayProducerFlags) error {
	store := refstore.New("")

	// Step 1: resolve inputs (also validates runID and locates the stage).
	resolved, err := replay.ResolveInputs(ctx, store, runID, stageName)
	if err != nil {
		switch {
		case errors.Is(err, replay.ErrInvalidRunID):
			return fmt.Errorf("invalid run id: %s", runID)
		case errors.Is(err, replay.ErrRunNotFound):
			return fmt.Errorf("run not found: %s", runID)
		case errors.Is(err, replay.ErrStageNotFound):
			return err // already includes available stage names
		case errors.Is(err, replay.ErrAmbiguousStage):
			return err // already includes per-duplicate detail
		default:
			return err
		}
	}

	// Step 2: require a recorded git SHA before any worktree or scratch work.
	gitSHA := resolved.GitSHA
	if gitSHA == "" {
		return fmt.Errorf("stage %q has no recorded git sha", stageName)
	}

	// Step 3: resolve the runner before creating any temp resources.
	activeRunner, err := r.resolveRunner(ctx, runnerSpec)
	if err != nil {
		return err
	}

	// Step 4: resolve repo root before creating temp resources.
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	// Step 5: checkout the recorded SHA into a throwaway worktree.
	wt, err := worktree.Checkout(ctx, root, gitSHA)
	if err != nil {
		switch {
		case errors.Is(err, worktree.ErrInvalidSHA):
			return fmt.Errorf("invalid git sha %q: %w", gitSHA, err)
		case errors.Is(err, worktree.ErrSHANotFound):
			return fmt.Errorf("git sha %q not found in repository", gitSHA)
		default:
			return err
		}
	}
	defer wt.Close()

	// Step 6: create a scratch directory outside the worktree.
	scratch, err := os.MkdirTemp("", "etude-replay-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	// Step 7: materialize inputs (content only; pointer inputs are not yet supported).
	inputs := make([]replay.RunInput, 0, len(resolved.ResolvedInputs))
	for _, inp := range resolved.ResolvedInputs {
		content, err := inp.ReadContent(ctx)
		if err != nil {
			if errors.Is(err, replay.ErrPointerNotMaterialized) {
				return fmt.Errorf("input %q is a pointer artifact and cannot be replayed yet", inp.Role)
			}
			return fmt.Errorf("read input %q: %w", inp.Role, err)
		}
		inputs = append(inputs, replay.RunInput{
			Role:      inp.ArtifactRef.Role,
			MediaType: inp.ArtifactRef.MediaType,
			Content:   content,
		})
	}

	// Step 8: build the producer, merging source with any flag overrides.
	src := resolved.Producer
	producer := runmanifest.Producer{
		Harness: runmanifest.Harness{
			Name:    mergeString(pf.harnessChanged, pf.harness, src.Harness.Name),
			Version: mergeString(pf.harnessVersionChanged, pf.harnessVersion, src.Harness.Version),
		},
		Model: mergeString(pf.modelChanged, pf.model, src.Model),
		Skill: runmanifest.Skill{
			ID:      mergeString(pf.skillIDChanged, pf.skillID, src.Skill.ID),
			Repo:    mergeString(pf.skillRepoChanged, pf.skillRepo, src.Skill.Repo),
			Version: mergeString(pf.skillVersionChanged, pf.skillVersion, src.Skill.Version),
		},
	}

	// Step 9: build and execute the run request.
	req := replay.RunRequest{
		WorktreeDir:     wt.Dir,
		ScratchDir:      scratch,
		Inputs:          inputs,
		OutputRole:      resolved.Output.Role,
		OutputMediaType: resolved.Output.MediaType,
		Producer:        producer,
	}
	res, err := activeRunner.Run(ctx, req)
	if err != nil {
		return err
	}

	// Step 10: optionally record the replay as a new linked run.
	if record {
		if len(res.Output) == 0 {
			return fmt.Errorf("replay produced no output; cannot record empty run")
		}
		if err := r.recordRun(ctx, store, out, runID, stageName, resolved, res, inputs); err != nil {
			return err
		}
	}

	// Step 11: emit the output (--output and --record may coexist).
	if outputPath != "" {
		if err := os.WriteFile(outputPath, res.Output, 0o644); err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		_, err = fmt.Fprintf(out, "output written to %s\n", outputPath)
		return err
	}
	_, err = out.Write(res.Output)
	return err
}

// mergeString returns override if changed is true, otherwise fallback.
func mergeString(changed bool, override, fallback string) string {
	if changed {
		return override
	}
	return fallback
}

// recordRun persists a new replay run ref with a single stage linking back to
// the source run/stage. It mirrors the create path used by capture.go.
func (r *replayRunner) recordRun(
	ctx context.Context,
	store refstore.Store,
	out io.Writer,
	sourceRunID, sourceStageName string,
	resolved replay.ResolvedStage,
	res replay.RunResult,
	_ []replay.RunInput, // materialized inputs unused here; we read raw bytes from source commit
) error {
	clockFn := r.now
	if clockFn == nil {
		clockFn = time.Now
	}
	now := clockFn().UTC()

	// Build the unique replay run id from source id + timestamp.
	baseID := fmt.Sprintf("%s-replay-%s", sourceRunID, now.Format("20060102T150405Z"))
	replayRunID, err := allocateReplayRunID(ctx, store, baseID)
	if err != nil {
		return err
	}

	// Build the artifact store for the new run: output goes in via AddContent,
	// then all source inputs are copied raw from the source commit.
	artifactStore := artifactstore.New()

	outputArtifact, err := artifactStore.AddContent(resolved.Output.Role, res.MediaType, res.Output)
	if err != nil {
		return fmt.Errorf("record: store output artifact: %w", err)
	}

	// Seed the files map from the artifact store (output bytes).
	files := artifactStore.Files()

	// Copy each source input's raw bytes from the source commit into files.
	// Using ReadCommitFile gives us the raw stored bytes (correct for both
	// content blobs and pointer-record JSON), bypassing ReadContent which
	// would fail on pointer artifacts.
	for _, inp := range resolved.ResolvedInputs {
		rawBytes, err := store.ReadCommitFile(ctx, resolved.Commit, inp.ArtifactRef.Path)
		if err != nil {
			return fmt.Errorf("record: read source input %q: %w", inp.ArtifactRef.Role, err)
		}
		files[inp.ArtifactRef.Path] = rawBytes
	}

	// Build the single replay stage. Both Stage.Skill and Stage.Producer must
	// be set (mirrors capture.go's pattern; validateStage requires Skill fields).
	skill := res.Producer.Skill
	stage := runmanifest.Stage{
		Name:       sourceStageName,
		ProducedBy: "replay",
		GitSHA:     resolved.GitSHA,
		Skill:      skill,
		Producer:   res.Producer,
		Inputs:     sourceInputRefs(resolved),
		Output:     runmanifest.ArtifactFromManifestArtifact(outputArtifact),
		Timestamp:  now,
		ReplayOf: &runmanifest.ReplayLink{
			RunID:  sourceRunID,
			Stage:  sourceStageName,
			Commit: resolved.Commit,
		},
	}

	manifest := runmanifest.Manifest{
		RunID:           replayRunID,
		Workflow:        resolved.Workflow,
		WorkflowVersion: resolved.WorkflowVersion,
		Created:         now,
		Refs:            resolved.Refs,
		Stages:          []runmanifest.Stage{stage},
	}

	written, err := (runmanifest.Writer{Store: store}).Write(ctx, manifest, files, runmanifest.WriteOptions{
		Message: fmt.Sprintf("replay: record %s stage %s from %s", replayRunID, sourceStageName, sourceRunID),
	})
	if err != nil {
		return fmt.Errorf("record: write replay run: %w", err)
	}

	_, err = fmt.Fprintf(out, "recorded replay run %s (commit %s)\n", replayRunID, written)
	return err
}

// sourceInputRefs extracts the ArtifactRefs from the resolved stage's inputs,
// preserving them verbatim so the replay run carries identical content-addressed refs.
func sourceInputRefs(resolved replay.ResolvedStage) []runmanifest.ArtifactRef {
	refs := make([]runmanifest.ArtifactRef, len(resolved.ResolvedInputs))
	for i, inp := range resolved.ResolvedInputs {
		refs[i] = inp.ArtifactRef
	}
	return refs
}

// allocateReplayRunID probes for a free replay run id, trying base then
// base-2 through base-10. Returns an error if none are free.
func allocateReplayRunID(ctx context.Context, store refstore.Store, base string) (string, error) {
	if !runmanifest.IsValidRunID(base) {
		return "", fmt.Errorf("derived replay run id %q is not a valid run id", base)
	}

	candidates := make([]string, 0, 11)
	candidates = append(candidates, base)
	for n := 2; n <= 10; n++ {
		candidates = append(candidates, fmt.Sprintf("%s-%d", base, n))
	}

	for _, id := range candidates {
		ref := "refs/etude/runs/" + id
		_, err := store.Resolve(ctx, ref)
		if errors.Is(err, refstore.ErrNotFound) {
			// Free slot found.
			return id, nil
		}
		if err != nil {
			return "", fmt.Errorf("probe replay run id %q: %w", id, err)
		}
		// Already exists — try next suffix.
	}
	return "", fmt.Errorf("could not allocate unique replay run id after 10 attempts (base: %s)", base)
}

// resolveRunner returns r.runner if injected (test seam), otherwise builds an
// ExecRunner from the provided spec (flag), or falls back to git config
// etude.runner. Returns an error if no runner can be determined.
func (r *replayRunner) resolveRunner(ctx context.Context, spec string) (replay.Runner, error) {
	if r.runner != nil {
		return r.runner, nil
	}

	if spec == "" {
		// Try git config etude.runner (any scope, not just --local).
		spec = gitConfigGet(ctx, "etude.runner")
	}

	if spec == "" {
		return nil, fmt.Errorf("no runner configured (set --runner or git config etude.runner)")
	}

	return &replay.ExecRunner{Command: strings.Fields(spec)}, nil
}

// gitConfigGet reads a single git config value for key using any scope.
// Returns an empty string if the key is absent or git is unavailable.
func gitConfigGet(ctx context.Context, key string) string {
	cmd := exec.CommandContext(ctx, "git", "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
