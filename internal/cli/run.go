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
		id, manifest, err := loadManifestForRef(ctx, r.store, ref, runsPrefix, "run")
		if err != nil {
			return err
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
	if err := validateExtraID("run", runmanifest.IsValidRunID(id), id); err != nil {
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

// validateExtraID returns an error when valid is false, using kind and id to
// form the exact message "invalid <kind> id <id>". Both validateRunIDExtra and
// validateRetroIDExtra delegate here so the format string has one source of
// truth across both callers.
func validateExtraID(kind string, valid bool, id string) error {
	if !valid {
		return fmt.Errorf("invalid %s id %q", kind, id)
	}
	return nil
}

// formatHarness returns the harness inner display value: "name version" when
// Version is set, else "name". Callers keep their own prefix/indent and their
// own Name != "" guard.
func formatHarness(h runmanifest.Harness) string {
	if h.Version != "" {
		return h.Name + " " + h.Version
	}
	return h.Name
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
		if stage.ReplayOf != nil {
			fmt.Fprintf(out, "  replay of:   %s/%s\n", stage.ReplayOf.RunID, stage.ReplayOf.Stage)
		}
		fmt.Fprintf(out, "  git sha:     %s\n", stage.GitSHA)
		if stage.Producer.Harness.Name != "" {
			fmt.Fprintf(out, "  harness:     %s\n", formatHarness(stage.Producer.Harness))
		}
		if stage.Producer.Model != "" {
			fmt.Fprintf(out, "  model:       %s\n", stage.Producer.Model)
		}
		fmt.Fprintf(out, "  skill:       %s@%s (%s)\n", stage.Producer.Skill.ID, stage.Producer.Skill.Version, stage.Producer.Skill.Repo)
		for _, input := range stage.Inputs {
			fmt.Fprintf(out, "  input:  role=%s path=%s size=%d storage=%s media-type=%s\n",
				input.Role, input.Path, input.Size, input.Storage, input.MediaType)
		}
		o := stage.Output
		fmt.Fprintf(out, "  output: role=%s path=%s size=%d storage=%s media-type=%s\n",
			o.Role, o.Path, o.Size, o.Storage, o.MediaType)
	}

	for _, gate := range m.Gates {
		printGate(out, gate)
	}
	return nil
}

// printGate renders one review-gate attempt in the same flat, indented style as
// the stage block. Optional fields (reviewed roles/artifacts, skill, required/
// optional feedback, failure note, raw output, decision reasons) print only when
// present, so they never leave blank lines.
func printGate(out io.Writer, g runmanifest.GateAttempt) {
	fmt.Fprintf(out, "\ngate: %s\n", g.GateID)
	fmt.Fprintf(out, "  phase:    %s\n", g.Phase)
	fmt.Fprintf(out, "  round:    %d\n", g.Round)
	fmt.Fprintf(out, "  tier:     %d\n", g.Tier)
	fmt.Fprintf(out, "  status:   %s\n", g.Status)
	for _, r := range g.ReviewedStages {
		line := r.Stage
		if r.Role != "" {
			line += fmt.Sprintf(" (role=%s)", r.Role)
		}
		if r.Artifact != "" {
			line += fmt.Sprintf(" (artifact=%s)", r.Artifact)
		}
		fmt.Fprintf(out, "  reviewed: %s\n", line)
	}
	if g.Decision.EscalationReason != "" {
		fmt.Fprintf(out, "  escalation: %s\n", g.Decision.EscalationReason)
	}
	if g.Decision.DegradedReason != "" {
		fmt.Fprintf(out, "  degraded: %s\n", g.Decision.DegradedReason)
	}
	if len(g.Decision.DeferredBeads) > 0 {
		fmt.Fprintf(out, "  deferred: %s\n", strings.Join(g.Decision.DeferredBeads, ", "))
	}
	for _, s := range g.Seats {
		fmt.Fprintf(out, "  seat: %s\n", s.Seat)
		fmt.Fprintf(out, "    provider: %s / %s\n", s.Provider.Name, s.Provider.Model)
		if s.Harness.Name != "" {
			fmt.Fprintf(out, "    harness:  %s\n", formatHarness(s.Harness))
		}
		if s.Skill.ID != "" {
			fmt.Fprintf(out, "    skill:    %s@%s (%s)\n", s.Skill.ID, s.Skill.Version, s.Skill.Repo)
		}
		fmt.Fprintf(out, "    verdict:  %s\n", s.Verdict)
		if len(s.Required) > 0 {
			fmt.Fprintln(out, "    required:")
			for _, r := range s.Required {
				fmt.Fprintf(out, "      - %s\n", r)
			}
		}
		if len(s.Optional) > 0 {
			fmt.Fprintln(out, "    optional:")
			for _, o := range s.Optional {
				fmt.Fprintf(out, "      - %s\n", o)
			}
		}
		if s.FailureNote != "" {
			fmt.Fprintf(out, "    note:     %s\n", s.FailureNote)
		}
		if s.RawOutput != nil {
			fmt.Fprintf(out, "    raw_output: %s\n", s.RawOutput.Path)
		}
	}
}
