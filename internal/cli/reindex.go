package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joshuavial/etude/internal/index"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/spf13/cobra"
)

func newReindexCommand(out, errOut io.Writer) *cobra.Command {
	runner := &reindexRunner{
		store:  refstore.New(""),
		stdout: out,
	}
	return buildReindexCommand(out, errOut, runner)
}

type reindexRunner struct {
	store  refstore.Store
	dbPath string // empty = resolve from git dir at run time
	stdout io.Writer
}

func buildReindexCommand(out, errOut io.Writer, runner *reindexRunner) *cobra.Command {
	var dbPathOverride string

	cmd := &cobra.Command{
		Use:           "reindex",
		Short:         "Rebuild the SQLite query index from all run and eval refs",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			dbPath := runner.dbPath
			if dbPath == "" {
				dbPath = dbPathOverride
			}
			if dbPath == "" {
				resolved, err := resolveIndexPath(ctx, runner.store.RepoDir)
				if err != nil {
					return err
				}
				dbPath = resolved
			}
			return runner.run(ctx, dbPath)
		},
	}

	cmd.SetOut(out)
	cmd.SetErr(errOut)

	// Hidden flag for test overrides — not shown in --help output.
	cmd.Flags().StringVar(&dbPathOverride, "db-path", "", "override the index database path (for testing)")
	if err := cmd.Flags().MarkHidden("db-path"); err != nil {
		panic(fmt.Sprintf("mark db-path hidden: %v", err))
	}

	return cmd
}

func (r *reindexRunner) run(ctx context.Context, dbPath string) error {
	result, err := index.Reindex(ctx, r.store, dbPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(r.stdout, "reindexed %d runs, %d evals into %s\n",
		result.Runs, result.Evals, dbPath)
	return nil
}

// resolveIndexPath shells out to git to resolve the absolute git dir (handles
// linked worktrees where .git is a file, and bare repos), then appends
// "etude-index.db". repoDir may be empty for cwd.
func resolveIndexPath(ctx context.Context, repoDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir")
	if repoDir != "" {
		cmd.Dir = repoDir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve git dir: %w", err)
	}
	gitDir := strings.TrimSpace(string(out))
	return filepath.Join(gitDir, "etude-index.db"), nil
}
