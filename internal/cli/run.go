package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

const runsPrefix = "refs/etude/runs/"

func newRunCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "run",
		Short:         "Inspect etude runs",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	runner := &runShowListRunner{
		store:  refstore.New(""),
		stdout: out,
	}
	cmd.AddCommand(newRunListCommand(runner))
	cmd.AddCommand(newRunShowCommand(runner))
	return cmd
}

type runShowListRunner struct {
	store  refstore.Store
	stdout io.Writer
}

func newRunListCommand(runner *runShowListRunner) *cobra.Command {
	return &cobra.Command{
		Use:           "list",
		Short:         "List all runs",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.list(cmd.Context())
		},
	}
}

func newRunShowCommand(runner *runShowListRunner) *cobra.Command {
	return &cobra.Command{
		Use:           "show <run-id>",
		Short:         "Show details of a run",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.show(cmd.Context(), args[0])
		},
	}
}

func (r *runShowListRunner) list(ctx context.Context) error {
	refs, err := r.store.List(ctx, strings.TrimSuffix(runsPrefix, "/"))
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		fmt.Fprintln(r.stdout, "no runs found")
		return nil
	}

	w := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "RUN ID\tWORKFLOW\tCREATED\tSTAGES")
	for _, ref := range refs {
		id := strings.TrimPrefix(ref, runsPrefix)
		manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
		if err != nil {
			return fmt.Errorf("run %q: %w", id, err)
		}
		manifest, err := runmanifest.ParseJSON(manifestBytes)
		if err != nil {
			return fmt.Errorf("run %q: %w", id, err)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\n",
			id,
			manifest.Workflow,
			manifest.Created.UTC().Format(time.RFC3339),
			len(manifest.Stages),
		)
	}
	return w.Flush()
}

func (r *runShowListRunner) show(ctx context.Context, id string) error {
	if err := validateCLIIdentifier("run id", id); err != nil {
		return err
	}
	if err := validateRunIDExtra(id); err != nil {
		return err
	}

	ref := runsPrefix + id
	_, err := r.store.Resolve(ctx, ref)
	if err != nil {
		if errors.Is(err, refstore.ErrNotFound) {
			return fmt.Errorf("run %q not found", id)
		}
		return err
	}

	manifestBytes, err := r.store.ReadFile(ctx, ref, "manifest.json")
	if err != nil {
		return fmt.Errorf("run %q: %w", id, err)
	}
	manifest, err := runmanifest.ParseJSON(manifestBytes)
	if err != nil {
		return fmt.Errorf("run %q: %w", id, err)
	}

	return printRunDetail(r.stdout, manifest)
}

// validateRunIDExtra enforces the additional rules from runmanifest.validateRunID
// that are not covered by validateCLIIdentifier: leading/trailing dot, ".." anywhere,
// all-dots, and ".lock" suffix. This runs before any git call so it works outside a repo.
func validateRunIDExtra(id string) error {
	if strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") ||
		strings.Contains(id, "..") || strings.Trim(id, ".") == "" ||
		strings.HasSuffix(id, ".lock") {
		return fmt.Errorf("invalid run id %q", id)
	}
	return nil
}

func printRunDetail(out io.Writer, m runmanifest.Manifest) error {
	fmt.Fprintf(out, "run id:           %s\n", m.RunID)
	fmt.Fprintf(out, "workflow:         %s\n", m.Workflow)
	fmt.Fprintf(out, "workflow version: %s\n", m.WorkflowVersion)
	fmt.Fprintf(out, "created:          %s\n", m.Created.UTC().Format(time.RFC3339))

	// Refs sorted by key for deterministic output.
	if len(m.Refs) > 0 {
		fmt.Fprintln(out, "refs:")
		keys := make([]string, 0, len(m.Refs))
		for k := range m.Refs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(out, "  %s: %s\n", k, m.Refs[k])
		}
	}

	for _, stage := range m.Stages {
		fmt.Fprintf(out, "\nstage: %s\n", stage.Name)
		fmt.Fprintf(out, "  produced_by: %s\n", stage.ProducedBy)
		fmt.Fprintf(out, "  git sha:     %s\n", stage.GitSHA)
		fmt.Fprintf(out, "  skill id:    %s\n", stage.Skill.ID)
		fmt.Fprintf(out, "  skill repo:  %s\n", stage.Skill.Repo)
		fmt.Fprintf(out, "  skill ver:   %s\n", stage.Skill.Version)
		for _, input := range stage.Inputs {
			fmt.Fprintf(out, "  input:  role=%s path=%s size=%d storage=%s media-type=%s\n",
				input.Role, input.Path, input.Size, input.Storage, input.MediaType)
		}
		o := stage.Output
		fmt.Fprintf(out, "  output: role=%s path=%s size=%d storage=%s media-type=%s\n",
			o.Role, o.Path, o.Size, o.Storage, o.MediaType)
	}
	return nil
}
