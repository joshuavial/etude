package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/worktree"
	"github.com/spf13/cobra"
)

type replayRunner struct {
	// runner is the Runner to use. If nil, an ExecRunner is built from the
	// resolved --runner spec at run time. Tests inject a *replay.StubRunner here.
	runner replay.Runner
}

func newReplayCommand(out, errOut io.Writer) *cobra.Command {
	return buildReplayCommand(out, errOut, &replayRunner{})
}

// buildReplayCommand constructs the replay cobra.Command backed by r. Tests
// call this directly to inject a pre-configured replayRunner (e.g. one with a
// StubRunner already set); production callers use newReplayCommand.
func buildReplayCommand(out, errOut io.Writer, r *replayRunner) *cobra.Command {
	var runnerSpec string
	var outputPath string

	cmd := &cobra.Command{
		Use:           "replay <run> <stage>",
		Short:         "Replay a recorded stage end-to-end",
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return r.run(cmd.Context(), out, args[0], args[1], runnerSpec, outputPath)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().StringVar(&runnerSpec, "runner", "", "runner command spec (e.g. ./run.sh)")
	cmd.Flags().StringVar(&outputPath, "output", "", "write output to this file instead of stdout")
	return cmd
}

func (r *replayRunner) run(ctx context.Context, out io.Writer, runID, stageName, runnerSpec, outputPath string) error {
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

	// Step 7: materialize inputs.
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

	// Step 8: build and execute the run request.
	req := replay.RunRequest{
		WorktreeDir:     wt.Dir,
		ScratchDir:      scratch,
		Inputs:          inputs,
		OutputRole:      resolved.Output.Role,
		OutputMediaType: resolved.Output.MediaType,
		Producer:        resolved.Producer,
	}
	res, err := activeRunner.Run(ctx, req)
	if err != nil {
		return err
	}

	// Step 9: emit the output.
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
