package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/liverun"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/spf13/cobra"
)

const runsPrefix = "refs/etude/runs/"

// reservedWorkflowNames are shadowed by run subcommand names.
var reservedWorkflowNames = map[string]bool{"show": true, "list": true}

func newRunCommand(out, errOut io.Writer) *cobra.Command {
	var taskFile, runID, gitSHA, resumeID, runnerSpec string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:           "run [workflow]",
		Short:         "Execute a workflow or inspect runs",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			workflowName := args[0]
			if reservedWorkflowNames[workflowName] {
				return fmt.Errorf("workflow name %q is reserved (conflicts with run subcommands show/list)", workflowName)
			}
			return runWorkflow(cmd.Context(), out, errOut, workflowName, taskFile, runID, gitSHA, resumeID, runnerSpec, timeout)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	flags := cmd.Flags()
	flags.StringVar(&taskFile, "task", "", "path to task input file")
	flags.StringVar(&runID, "run-id", "", "explicit run id (auto-generated if not set)")
	flags.StringVar(&gitSHA, "git-sha", "", "git commit SHA for the run (defaults to HEAD)")
	flags.StringVar(&runnerSpec, "runner", "", "runner command override for all stages")
	flags.DurationVar(&timeout, "timeout", 10*time.Minute, "per-stage runner timeout")
	flags.StringVar(&resumeID, "resume", "", "resume a partial run by id")

	runner := &runShowListRunner{store: refstore.New(""), stdout: out}
	cmd.AddCommand(newRunListCommand(runner))
	cmd.AddCommand(newRunShowCommand(runner))
	return cmd
}

// runWorkflow loads the workflow and registry, then delegates to the Engine.
func runWorkflow(ctx context.Context, out, errOut io.Writer, workflowName, taskFile, runID, gitSHA, resumeID, runnerSpec string, timeout time.Duration) error {
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	wfBytes, err := os.ReadFile(filepath.Join(root, ".etude", "workflow.yaml"))
	if err != nil {
		return fmt.Errorf("load workflow: %w", err)
	}
	wf, err := workflow.ParseYAML(wfBytes)
	if err != nil {
		return fmt.Errorf("parse workflow: %w", err)
	}
	if wf.Name != workflowName {
		return fmt.Errorf("workflow name %q does not match .etude/workflow.yaml name %q", workflowName, wf.Name)
	}

	// Registry is optional (workflows may use inline command runners).
	var reg registry.Registry
	regBytes, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err == nil {
		reg, err = registry.ParseYAML(regBytes)
		if err != nil {
			return fmt.Errorf("parse registry: %w", err)
		}
	}

	// Build the runner factory.
	resolveRunner := buildRunnerFactory(wf, reg, runnerSpec, timeout)

	var taskBytes []byte
	if resumeID == "" && taskFile != "" {
		taskBytes, err = os.ReadFile(taskFile)
		if err != nil {
			return fmt.Errorf("read task file: %w", err)
		}
	}

	engine := &liverun.Engine{
		Store:         refstore.New(root),
		ResolveRunner: resolveRunner,
		Root:          root,
	}

	opts := liverun.RunOptions{
		TaskBytes: taskBytes,
		TaskFile:  taskFile,
		RunID:     runID,
		GitSHA:    gitSHA,
		ResumeID:  resumeID,
	}

	if err := engine.Run(ctx, out, wf, opts); err != nil {
		var stageErr *liverun.StageError
		if errors.As(err, &stageErr) {
			// Print only the resume hint here; the error itself is printed once
			// by the root command from the returned err (avoid a double print).
			fmt.Fprintf(errOut, "resume with: etude run %s --resume %s\n", workflowName, stageErr.RunID)
		}
		return err
	}
	return nil
}

// buildRunnerFactory returns a ResolveRunner factory. If runnerSpec is set,
// all stages use that command; otherwise stages resolve from workflow/registry.
func buildRunnerFactory(wf workflow.Workflow, reg registry.Registry, runnerSpec string, timeout time.Duration) func(workflow.Stage) (replay.Runner, error) {
	if runnerSpec != "" {
		r := &replay.ExecRunner{
			Command:        strings.Fields(runnerSpec),
			Timeout:        timeout,
			MaxOutputBytes: 64 << 20,
		}
		return func(workflow.Stage) (replay.Runner, error) { return r, nil }
	}
	return func(stage workflow.Stage) (replay.Runner, error) {
		return liverun.ResolveStageRunner(wf, reg, stage, timeout)
	}
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
	var asJSON bool
	cmd := &cobra.Command{
		Use:           "show <run-id>",
		Short:         "Show details of a run",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runner.show(cmd.Context(), args[0], asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"Emit the full run manifest (stages and gate attempts) as JSON")
	return cmd
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

func (r *runShowListRunner) show(ctx context.Context, id string, asJSON bool) error {
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

	if asJSON {
		// Emit the canonical on-disk manifest bytes (snake_case wire schema,
		// re-ingestible via runmanifest.ParseJSON). Marshaling the parsed Go
		// struct would NOT work: runmanifest.Manifest has no JSON tags — the
		// wire schema is produced separately via manifestJSON — so a bare
		// marshal yields PascalCase keys + leaked zero-times that no manifest
		// consumer can parse. ParseJSON above already validated the bytes, so a
		// corrupt manifest still errors rather than dumping garbage.
		fmt.Fprintln(r.stdout, strings.TrimRight(string(manifestBytes), "\n"))
		return nil
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
