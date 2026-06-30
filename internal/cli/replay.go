package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/liverun"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/joshuavial/etude/internal/worktree"
	"github.com/spf13/cobra"
)

type replayRunner struct {
	// runner is the Runner to use for single-stage replay. If nil, an ExecRunner
	// is built from the resolved --runner spec at run time. Tests inject a StubRunner.
	runner replay.Runner
	// forwardRunner, when non-nil, is used for all stages in forward replay (tests only).
	forwardRunner replay.Runner
	// now returns the current time. Defaults to time.Now; tests inject a fixed clock.
	now func() time.Time
	// timeout overrides the default ExecRunner timeout when non-zero.
	timeout time.Duration
	// envAllowlist holds the NAMES (never values) to pass through to runners
	// when --allow-env is set. Nil/empty means hermetic (default).
	envAllowlist []string
}

func newReplayCommand(out, errOut io.Writer) *cobra.Command {
	return buildReplayCommand(out, errOut, &replayRunner{now: time.Now, timeout: 10 * time.Minute})
}

// buildReplayCommand constructs the replay cobra.Command backed by r. Tests
// call this directly to inject a pre-configured replayRunner (e.g. one with a
// StubRunner already set); production callers use newReplayCommand.
func buildReplayCommand(out, errOut io.Writer, r *replayRunner) *cobra.Command {
	var runnerSpec string
	var outputPath string
	var record bool
	var allowEnv bool
	var skillVersion, skillID, skillRepo, model, harness, harnessVersion string
	var timeoutFlag time.Duration

	// singleStageOnlyFlags are rejected in forward-replay (1-arg) mode.
	const singleStageOnly = "only valid for single-stage replay (requires a stage argument)"

	cmd := &cobra.Command{
		Use:           "replay <run> [<stage>]",
		Short:         "Replay a recorded stage or entire run",
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.timeout = timeoutFlag

			// --record + --allow-env is rejected: replay --record passes values
			// to the runner but does not record the allowlist names in the audit
			// manifest. Use etude run for live runs with audited env passthrough.
			if record && allowEnv {
				return fmt.Errorf("--record and --allow-env cannot be combined: use etude run for live runs with audited env passthrough")
			}

			// When --allow-env is set, load the workflow to get the allowlist
			// names and set them on the runner. Values are never stored here.
			if allowEnv {
				root, rootErr := repoRoot(cmd.Context())
				if rootErr != nil {
					return rootErr
				}
				wfBytes, wfErr := os.ReadFile(filepath.Join(root, ".etude", "workflow.yaml"))
				if wfErr != nil {
					return fmt.Errorf("load workflow for --allow-env: %w", wfErr)
				}
				wf, wfErr := workflow.ParseYAML(wfBytes)
				if wfErr != nil {
					return fmt.Errorf("parse workflow: %w", wfErr)
				}
				r.envAllowlist = wf.EnvAllowlist
			}

			if len(args) == 1 {
				// Forward replay: error on single-stage-only flags.
				for _, flag := range []string{"record", "output", "skill-id", "skill-repo", "skill-version", "model", "harness", "harness-version"} {
					if cmd.Flags().Changed(flag) {
						return fmt.Errorf("--%s is %s", flag, singleStageOnly)
					}
				}
				return r.runForward(cmd.Context(), out, args[0], runnerSpec, timeoutFlag)
			}

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
	cmd.Flags().BoolVar(&allowEnv, "allow-env", false, "pass the workflow env_allowlist vars to the runner (default: hermetic; cannot combine with --record)")
	cmd.Flags().StringVar(&skillVersion, "skill-version", "", "override skill version in recorded producer")
	cmd.Flags().StringVar(&skillID, "skill-id", "", "override skill id in recorded producer")
	cmd.Flags().StringVar(&skillRepo, "skill-repo", "", "override skill repo in recorded producer")
	cmd.Flags().StringVar(&model, "model", "", "override model in recorded producer")
	cmd.Flags().StringVar(&harness, "harness", "", "override harness name in recorded producer")
	cmd.Flags().StringVar(&harnessVersion, "harness-version", "", "override harness version in recorded producer")
	cmd.Flags().DurationVar(&timeoutFlag, "timeout", 10*time.Minute, "per-invocation timeout for the runner (0 disables)")
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
		// Friendly pre-check: if the path already exists and is not a regular file
		// (e.g. a symlink or directory), reject with a clear message before the
		// atomic open below would return a less descriptive ELOOP or EISDIR.
		if fi, statErr := os.Lstat(outputPath); statErr == nil && !fi.Mode().IsRegular() {
			return fmt.Errorf("write output file: refusing to write --output %s: not a regular file", outputPath)
		}
		// Atomic guard: O_NOFOLLOW ensures a final-component symlink fails with
		// ELOOP rather than being followed, defeating create-time TOCTOU races.
		f, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|nofollowFlag, 0o644)
		if err != nil {
			return fmt.Errorf("write output file: %w", err)
		}
		if _, err := f.Write(res.Output); err != nil {
			f.Close()
			return fmt.Errorf("write output file: %w", err)
		}
		if err := f.Close(); err != nil {
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

// recordRun is a thin wrapper around replay.RunRecorder.Record that handles the
// CLI-specific concerns: printing the "recorded replay run" confirmation line.
func (r *replayRunner) recordRun(
	ctx context.Context,
	store refstore.Store,
	out io.Writer,
	sourceRunID, sourceStageName string,
	resolved replay.ResolvedStage,
	res replay.RunResult,
	_ []replay.RunInput, // materialized inputs unused here; raw bytes come from source commit
) error {
	rec := replay.RunRecorder{Store: store, Now: r.now}
	recorded, err := rec.Record(ctx, sourceRunID, sourceStageName, resolved, res)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "recorded replay run %s (commit %s)\n", recorded.RunID, recorded.Commit)
	return err
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

	return &replay.ExecRunner{
		Command:        strings.Fields(spec),
		Timeout:        r.timeout,
		MaxOutputBytes: 64 << 20,
		EnvAllowlist:   r.envAllowlist,
	}, nil
}

// runForward performs a forward replay of all stages in a run (1-arg mode).
func (r *replayRunner) runForward(ctx context.Context, out io.Writer, runID, runnerSpec string, timeout time.Duration) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	store := refstore.New(root)

	// Build the stage-name→runner factory for forward replay.
	var resolveRunner func(stageName string) (replay.Runner, error)

	switch {
	case r.forwardRunner != nil:
		// Test injection: one stub for all stages.
		fwd := r.forwardRunner
		resolveRunner = func(string) (replay.Runner, error) { return fwd, nil }

	case runnerSpec != "":
		// CLI --runner override: same ExecRunner for all stages.
		er := &replay.ExecRunner{
			Command:        strings.Fields(runnerSpec),
			Timeout:        timeout,
			MaxOutputBytes: 64 << 20,
			EnvAllowlist:   r.envAllowlist,
		}
		resolveRunner = func(string) (replay.Runner, error) { return er, nil }

	default:
		// Resolve from workflow/registry per stage.
		wfBytes, err := os.ReadFile(filepath.Join(root, ".etude", "workflow.yaml"))
		if err != nil {
			return fmt.Errorf("load workflow for forward replay: %w", err)
		}
		wf, err := workflow.ParseYAML(wfBytes)
		if err != nil {
			return fmt.Errorf("parse workflow: %w", err)
		}
		var reg registry.Registry
		regBytes, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
		if err == nil {
			reg, err = registry.ParseYAML(regBytes)
			if err != nil {
				return fmt.Errorf("parse registry: %w", err)
			}
		}
		resolveRunner = func(stageName string) (replay.Runner, error) {
			for _, s := range wf.Stages {
				if s.Name == stageName {
					return liverun.ResolveStageRunner(wf, reg, s, timeout, r.envAllowlist)
				}
			}
			return nil, fmt.Errorf("stage %q not found in workflow %q", stageName, wf.Name)
		}
	}

	return liverun.ReplayForward(ctx, store, root, out, runID, resolveRunner)
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
