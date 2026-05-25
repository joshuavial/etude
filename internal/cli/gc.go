package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/joshuavial/etude/internal/gc"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/spf13/cobra"
)

func newGCCommand(out, errOut io.Writer) *cobra.Command {
	runner := &gcRunner{
		store:  refstore.New(""),
		stdout: out,
		stderr: errOut,
	}
	return buildGCCommand(out, errOut, runner)
}

type gcRunner struct {
	store  refstore.Store
	stdout io.Writer
	stderr io.Writer
}

func buildGCCommand(out, errOut io.Writer, runner *gcRunner) *cobra.Command {
	var prune bool
	var maxSize int64

	cmd := &cobra.Command{
		Use:           "gc [--max-size N] [--prune] [run-id...]",
		Short:         "Report artifact storage or prune named run refs",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if prune {
				if len(args) == 0 {
					return fmt.Errorf("--prune requires one or more run ids")
				}
				return runner.prune(ctx, args)
			}
			return runner.report(ctx, gc.CollectOptions{MaxSize: maxSize})
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().BoolVar(&prune, "prune", false, "delete named run refs (requires run-id args)")
	cmd.Flags().Int64Var(&maxSize, "max-size", 0, "report runs whose total content-artifact bytes exceed N")
	return cmd
}

// report runs the default (no --prune) mode: collect and render a storage report.
func (r *gcRunner) report(ctx context.Context, opts gc.CollectOptions) error {
	rpt, err := gc.Collect(ctx, r.store, opts)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "TOTAL\t%d logical artifact bytes (pre-dedup)\t%d runs\t%d evals\n",
		rpt.TotalBytes, rpt.RunCount, rpt.EvalCount)
	w.Flush()

	if opts.MaxSize > 0 && len(rpt.Oversized) > 0 {
		fmt.Fprintln(r.stdout)
		fmt.Fprintln(r.stdout, "OVERSIZED (exceeds --max-size)")
		ow := tabwriter.NewWriter(r.stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(ow, "RUN ID\tBYTES")
		for _, s := range rpt.Oversized {
			fmt.Fprintf(ow, "%s\t%d\n", s.RunID, s.ContentSize)
		}
		ow.Flush()
	}

	if len(rpt.External) > 0 {
		fmt.Fprintln(r.stdout)
		fmt.Fprintln(r.stdout, "EXTERNAL (pointer artifacts)")
		for _, s := range rpt.External {
			for _, p := range s.Pointers {
				uri := p.URI
				if uri == "" {
					uri = "(unknown)"
				}
				fmt.Fprintf(r.stdout, "  %s  stage=%s  role=%s  uri=%s\n",
					s.RunID, p.Stage, p.Role, uri)
			}
		}
	}

	return nil
}

// prune runs the --prune mode: delete named run refs that pass the safety check.
// Exits non-zero if any named run was unknown or refused.
func (r *gcRunner) prune(ctx context.Context, runIDs []string) error {
	pruned, refused, err := gc.Prune(ctx, r.store, runIDs)
	if err != nil {
		return err
	}

	for _, id := range pruned {
		fmt.Fprintf(r.stdout, "pruned %s\n", id)
	}
	for _, ref := range refused {
		fmt.Fprintf(r.stderr, "refused %s: %s\n", ref.RunID, ref.Reason)
	}

	if len(refused) > 0 {
		// Summarise which ids were refused so the caller can see the full picture
		// on stdout as well. (stderr already has the per-line reasons.)
		ids := make([]string, 0, len(refused))
		for _, ref := range refused {
			ids = append(ids, ref.RunID)
		}
		return fmt.Errorf("refused: %s", strings.Join(ids, ", "))
	}
	return nil
}
