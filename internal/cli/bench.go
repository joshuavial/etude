package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/bench"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/spf13/cobra"
)

// benchRunner holds injected dependencies. Nil fields are resolved at run time.
// Tests inject runner and judge directly; production callers use newBenchCommand.
type benchRunner struct {
	// runner is the replay.Runner to use. If nil, resolveRunner builds one from spec/git-config.
	runner replay.Runner
	// judge is the eval.Judge to use. If nil, resolveJudge builds one from spec/git-config.
	judge eval.Judge
	// now returns the current time. Defaults to time.Now; tests inject a fixed clock.
	now func() time.Time
}

func newBenchCommand(out, errOut io.Writer) *cobra.Command {
	return buildBenchCommand(out, errOut, &benchRunner{now: time.Now})
}

// buildBenchCommand constructs the bench cobra.Command backed by r. Tests call
// this directly with injected runner and judge; production callers use newBenchCommand.
func buildBenchCommand(out, errOut io.Writer, r *benchRunner) *cobra.Command {
	var (
		last                                                             int
		runnerSpec, judgeSpec, judgeModel                                string
		seed                                                             int64
		skillVersion, skillID, skillRepo, model, harness, harnessVersion string
	)

	cmd := &cobra.Command{
		Use:           "bench <stage>",
		Short:         "Benchmark a stage by replaying the cohort and judging replay vs original",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			overrides := bench.ProducerOverrides{
				SkillIDChanged:        cmd.Flags().Changed("skill-id"),
				SkillRepoChanged:      cmd.Flags().Changed("skill-repo"),
				SkillVersionChanged:   cmd.Flags().Changed("skill-version"),
				ModelChanged:          cmd.Flags().Changed("model"),
				HarnessChanged:        cmd.Flags().Changed("harness"),
				HarnessVersionChanged: cmd.Flags().Changed("harness-version"),
				SkillID:               skillID,
				SkillRepo:             skillRepo,
				SkillVersion:          skillVersion,
				Model:                 model,
				Harness:               harness,
				HarnessVersion:        harnessVersion,
			}
			return r.run(cmd.Context(), out, errOut, args[0], runnerSpec, judgeSpec, judgeModel, seed, last, overrides)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	cmd.Flags().IntVar(&last, "last", 10, "number of most-recent qualifying runs to benchmark (must be >0)")
	cmd.Flags().StringVar(&runnerSpec, "runner", "", "runner command spec (e.g. ./run.sh)")
	cmd.Flags().StringVar(&judgeSpec, "judge", "", "judge command spec (e.g. ./judge.sh)")
	cmd.Flags().StringVar(&judgeModel, "judge-model", "", "model passed to the judge as ETUDE_MODEL (falls back to git config etude.judgeModel; empty is allowed)")
	cmd.Flags().Int64Var(&seed, "seed", 0, "seed for per-pair presentation randomisation")
	cmd.Flags().StringVar(&skillVersion, "skill-version", "", "override skill version in recorded producer (contestant)")
	cmd.Flags().StringVar(&skillID, "skill-id", "", "override skill id in recorded producer (contestant)")
	cmd.Flags().StringVar(&skillRepo, "skill-repo", "", "override skill repo in recorded producer (contestant)")
	cmd.Flags().StringVar(&model, "model", "", "override model in recorded producer (contestant, NOT the judge/referee — use --judge-model for that)")
	cmd.Flags().StringVar(&harness, "harness", "", "override harness name in recorded producer (contestant)")
	cmd.Flags().StringVar(&harnessVersion, "harness-version", "", "override harness version in recorded producer (contestant)")

	return cmd
}

func (r *benchRunner) run(
	ctx context.Context,
	out, errOut io.Writer,
	stage, runnerSpec, judgeSpec, judgeModel string,
	seed int64,
	last int,
	overrides bench.ProducerOverrides,
) error {
	// Validate --last before any store access.
	if last <= 0 {
		return fmt.Errorf("--last must be positive")
	}

	// Resolve runner and judge before any store/repo work.
	activeRunner, err := r.resolveRunner(ctx, runnerSpec)
	if err != nil {
		return err
	}

	activeJudge, err := r.resolveJudge(ctx, judgeSpec, judgeModel)
	if err != nil {
		return err
	}

	store := refstore.New("")

	// Select the cohort.
	cohort, err := bench.SelectCohort(ctx, store, stage, last)
	if err != nil {
		return err
	}
	if len(cohort.Selected) == 0 {
		return fmt.Errorf("no runs contain stage %q with a replayable output", stage)
	}

	// Resolve repo root for worktree checkout.
	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	nowFn := r.now
	if nowFn == nil {
		nowFn = time.Now
	}

	pipeline := bench.Pipeline{
		Store:     store,
		Runner:    activeRunner,
		Judge:     activeJudge,
		Recorder:  replay.RunRecorder{Store: store, Now: nowFn},
		Seed:      seed,
		Overrides: overrides,
		Now:       nowFn,
	}

	// Run the pipeline: skip-and-report on per-run errors.
	var outcomes []bench.BenchOutcome
	var failures []bench.BenchFailure
	for _, cr := range cohort.Selected {
		outcome, err := pipeline.BenchRun(ctx, root, cr)
		if err != nil {
			failures = append(failures, bench.BenchFailure{SourceRunID: cr.RunID, Err: err})
			continue
		}
		outcomes = append(outcomes, outcome)
	}

	report := bench.Aggregate(outcomes)
	report.Stage = stage
	report.Failures = failures
	report.Skipped = cohort.Skipped

	renderReport(out, report)

	// Exit non-zero only when no evals succeeded.
	if report.Total == 0 {
		return fmt.Errorf("no successful evaluations")
	}
	return nil
}

// resolveRunner returns r.runner if injected (test seam), otherwise builds an
// ExecRunner from the provided spec (flag), or falls back to git config
// etude.runner. Returns an error if no runner can be determined.
func (r *benchRunner) resolveRunner(ctx context.Context, spec string) (replay.Runner, error) {
	if r.runner != nil {
		return r.runner, nil
	}
	if spec == "" {
		spec = gitConfigGet(ctx, "etude.runner")
	}
	if spec == "" {
		return nil, fmt.Errorf("no runner configured (set --runner or git config etude.runner)")
	}
	return &replay.ExecRunner{Command: strings.Fields(spec)}, nil
}

// resolveJudge returns r.judge if injected (test seam), otherwise resolves the
// judge command and model independently:
//   - command: --judge spec / git config etude.judge; empty => error.
//   - model: --judge-model / git config etude.judgeModel; empty is allowed (the
//     judge command may encode its own model selection).
//
// --model (producer/contestant override) is kept entirely separate and NEVER
// reaches ExecJudge.Model.
func (r *benchRunner) resolveJudge(ctx context.Context, spec, judgeModel string) (eval.Judge, error) {
	if r.judge != nil {
		return r.judge, nil
	}

	// Resolve judge command.
	if spec == "" {
		spec = gitConfigGet(ctx, "etude.judge")
	}
	if spec == "" {
		return nil, fmt.Errorf("no judge configured (set --judge or git config etude.judge)")
	}

	// Resolve judge model independently. Empty is allowed.
	if judgeModel == "" {
		judgeModel = gitConfigGet(ctx, "etude.judgeModel")
	}

	return &eval.ExecJudge{Command: strings.Fields(spec), Model: judgeModel}, nil
}

// renderReport writes the bench report to out using tabwriter for alignment.
func renderReport(out io.Writer, r bench.Report) {
	// Headline.
	fmt.Fprintf(out, "bench %s: replay (new skill) wins %.1f%% vs original\n",
		r.Stage, r.WinRateB*100)
	fmt.Fprintf(out, "(B=%d A=%d tie=%d) over %d evals; %d skipped, %d failed\n\n",
		r.CountB, r.CountA, r.CountTie, r.Total,
		len(r.Skipped), len(r.Failures))

	if len(r.Outcomes) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SOURCE RUN\tREPLAY RUN\tWINNER\tCONF\tEVAL ID\tFINDING")
		for _, o := range r.Outcomes {
			conf := "-"
			if o.Confidence != nil {
				conf = fmt.Sprintf("%.2f", *o.Confidence)
			}
			finding := "-"
			if len(o.Findings) > 0 {
				finding = o.Findings[0].Message
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				o.SourceRunID, o.ReplayRunID, string(o.Winner), conf, o.EvalID, finding)
		}
		w.Flush()
		fmt.Fprintln(out)
	}

	if len(r.Skipped) > 0 {
		fmt.Fprintln(out, "Skipped runs:")
		for _, s := range r.Skipped {
			detail := s.Detail
			if detail == "" {
				detail = string(s.Reason)
			}
			fmt.Fprintf(out, "  %s: %s\n", s.RunID, detail)
		}
		fmt.Fprintln(out)
	}

	if len(r.Failures) > 0 {
		fmt.Fprintln(out, "Failed runs:")
		for _, f := range r.Failures {
			fmt.Fprintf(out, "  %s: %v\n", f.SourceRunID, f.Err)
		}
		fmt.Fprintln(out)
	}
}
