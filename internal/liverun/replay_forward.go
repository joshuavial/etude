package liverun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/worktree"
)

// ReplayForward re-executes all stages of a run in manifest order using the
// recorded (content-addressed) inputs for each stage. Output bytes from each
// stage are written sequentially to out. The worktree is shared across stages
// and checked out at the run's recorded git SHA.
//
// resolveRunner is called once per stage to obtain the runner. Tests inject a
// StubRunner; production code resolves from the workflow/registry config.
func ReplayForward(
	ctx context.Context,
	store refstore.Store,
	root string,
	out io.Writer,
	runID string,
	resolveRunner func(stageName string) (replay.Runner, error),
) error {
	// Load the run manifest.
	ref := runsPrefix + runID
	commit, err := store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return fmt.Errorf("run %q not found", runID)
		}
		return fmt.Errorf("resolve run %q: %w", runID, err)
	}

	manifestBytes, err := store.ReadCommitFile(ctx, commit, "manifest.json")
	if err != nil {
		return fmt.Errorf("read manifest for run %q: %w", runID, err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("parse manifest for run %q: %w", runID, err)
	}
	if len(manifest.Stages) == 0 {
		return fmt.Errorf("run %q has no stages to replay", runID)
	}

	gitSHA := manifest.Stages[0].GitSHA
	wt, err := worktree.Checkout(ctx, root, gitSHA)
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

	scratch, err := os.MkdirTemp("", "etude-forward-replay-scratch-*")
	if err != nil {
		return fmt.Errorf("create scratch dir: %w", err)
	}
	defer os.RemoveAll(scratch)

	for i, stage := range manifest.Stages {
		runner, err := resolveRunner(stage.Name)
		if err != nil {
			return fmt.Errorf("stage %q: resolve runner: %w", stage.Name, err)
		}

		// Materialize recorded inputs from the manifest commit.
		inputs := make([]replay.RunInput, 0, len(stage.Inputs))
		for _, inp := range stage.Inputs {
			content, err := store.ReadCommitFile(ctx, commit, inp.Path)
			if err != nil {
				return fmt.Errorf("stage %q: read input %q: %w", stage.Name, inp.Role, err)
			}
			inputs = append(inputs, replay.RunInput{
				Role:      inp.Role,
				MediaType: inp.MediaType,
				Content:   content,
			})
		}

		stageScratch := fmt.Sprintf("%s/stage%02d", scratch, i)
		if err := os.MkdirAll(stageScratch, 0o755); err != nil {
			return fmt.Errorf("stage %q: mkdir scratch: %w", stage.Name, err)
		}

		res, err := runner.Run(ctx, replay.RunRequest{
			WorktreeDir:     wt.Dir,
			ScratchDir:      stageScratch,
			Inputs:          inputs,
			OutputRole:      stage.Output.Role,
			OutputMediaType: stage.Output.MediaType,
			Producer:        stage.Producer,
		})
		if err != nil {
			return fmt.Errorf("stage %q: runner: %w", stage.Name, err)
		}

		if _, err := out.Write(res.Output); err != nil {
			return fmt.Errorf("stage %q: write output: %w", stage.Name, err)
		}
	}
	return nil
}
