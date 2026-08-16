package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuavial/etude/internal/bench"
	"github.com/joshuavial/etude/internal/eval"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/spf13/cobra"
)

// benchRunner holds injected dependencies. Nil fields are resolved at run time.
// Tests inject runner and judge directly; production callers use newBenchCommand.
type benchRunner struct {
	// runner is the replay.Runner to use. If nil, resolveRunner builds one from spec/git-config.
	runner replay.Runner
	// judge is the eval.Judge to use. If nil, resolveJudge builds one from spec/git-config.
	judge eval.Judge
	// corpus is the gate-bench CorpusSource to use. If nil, run-refs resolves to
	// bench.RunRefsSource over the repository's ref store.
	corpus bench.CorpusSource
	// now returns the current time. Defaults to time.Now; tests inject a fixed clock.
	now func() time.Time
	// timeout overrides the default ExecRunner and ExecJudge timeout when non-zero.
	timeout time.Duration
}

func newBenchCommand(out, errOut io.Writer) *cobra.Command {
	return buildBenchCommand(out, errOut, &benchRunner{now: time.Now, timeout: 10 * time.Minute})
}

// buildBenchCommand constructs the bench cobra.Command backed by r. Tests call
// this directly with injected runner and judge; production callers use newBenchCommand.
func buildBenchCommand(out, errOut io.Writer, r *benchRunner) *cobra.Command {
	var (
		last                                                             int
		runnerSpec, judgeSpec, judgeModel                                string
		gatePhase, cohortSpec, labelsPath                                string
		variants                                                         []string
		seed                                                             int64
		skillVersion, skillID, skillRepo, model, harness, harnessVersion string
		noCache                                                          bool
		timeoutFlag                                                      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "bench <stage>",
		Short: "Benchmark a stage by replaying the cohort and judging replay vs original",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("gate") {
				return cobra.NoArgs(cmd, args)
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			r.timeout = timeoutFlag
			if cmd.Flags().Changed("gate") {
				if err := rejectChangedFlags(cmd, "gate mode", []string{
					"runner", "seed", "skill-id", "skill-repo", "skill-version",
					"model", "harness", "harness-version", "no-cache",
				}); err != nil {
					return err
				}
				return r.runGateBench(cmd.Context(), out, gatePhase, variants, cohortSpec, labelsPath, judgeSpec, judgeModel, last)
			}
			if err := rejectChangedFlags(cmd, "replay mode", []string{"variant", "cohort", "labels"}); err != nil {
				return err
			}
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
			return r.run(cmd.Context(), out, errOut, args[0], runnerSpec, judgeSpec, judgeModel, seed, last, noCache, overrides)
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	cmd.Flags().IntVar(&last, "last", 10, "number of most-recent qualifying runs to benchmark (must be >0)")
	cmd.Flags().StringVar(&runnerSpec, "runner", "", "runner command spec (e.g. ./run.sh)")
	cmd.Flags().StringVar(&judgeSpec, "judge", "", "judge command spec (e.g. ./judge.sh)")
	cmd.Flags().StringVar(&judgeModel, "judge-model", "", "model passed to the judge as ETUDE_MODEL (falls back to git config etude.judgeModel; empty is allowed)")
	cmd.Flags().StringVar(&gatePhase, "gate", "", "benchmark gate-prompt variants for this phase instead of replaying a stage")
	cmd.Flags().StringArrayVar(&variants, "variant", nil, "gate-prompt file to benchmark (repeat for each variant; at least two required)")
	cmd.Flags().StringVar(&cohortSpec, "cohort", "", "gate-bench corpus selector (supported: run-refs)")
	cmd.Flags().StringVar(&labelsPath, "labels", "", "optional strict gate-labels JSON file")
	cmd.Flags().Int64Var(&seed, "seed", 0, "seed for per-pair presentation randomisation")
	cmd.Flags().StringVar(&skillVersion, "skill-version", "", "override skill version in recorded producer (contestant)")
	cmd.Flags().StringVar(&skillID, "skill-id", "", "override skill id in recorded producer (contestant)")
	cmd.Flags().StringVar(&skillRepo, "skill-repo", "", "override skill repo in recorded producer (contestant)")
	cmd.Flags().StringVar(&model, "model", "", "override model in recorded producer (contestant, NOT the judge/referee — use --judge-model for that)")
	cmd.Flags().StringVar(&harness, "harness", "", "override harness name in recorded producer (contestant)")
	cmd.Flags().StringVar(&harnessVersion, "harness-version", "", "override harness version in recorded producer (contestant)")
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "force re-evaluation; skip the eval-result cache")
	cmd.Flags().DurationVar(&timeoutFlag, "timeout", 10*time.Minute, "per-invocation timeout for the runner and judge (0 disables)")

	return cmd
}

func rejectChangedFlags(cmd *cobra.Command, mode string, names []string) error {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return fmt.Errorf("--%s is not valid in %s", name, mode)
		}
	}
	return nil
}

func (r *benchRunner) run(
	ctx context.Context,
	out, errOut io.Writer,
	stage, runnerSpec, judgeSpec, judgeModel string,
	seed int64,
	last int,
	noCache bool,
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

	judgeID := eval.JudgeIdentity(activeJudge)

	pipeline := bench.Pipeline{
		Store:     store,
		Runner:    activeRunner,
		Judge:     activeJudge,
		Recorder:  replay.RunRecorder{Store: store, Now: nowFn},
		Seed:      seed,
		Overrides: overrides,
		Now:       nowFn,
		Cache:     !noCache,
		JudgeID:   judgeID,
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

type gatePromptVariant struct {
	display string
	content []byte
}

type gateBenchCell struct {
	fixtureIndex int
	variant      string
	key          bench.GateLabelKey
	predicted    bench.GateVerdict
	label        *bench.GateLabel
	findings     []eval.Finding
	err          error
	inCommon     bool
}

type gateBenchExclusion struct {
	key      bench.GateLabelKey
	variants []string
}

func (r *benchRunner) runGateBench(
	ctx context.Context,
	out io.Writer,
	phase string,
	variantPaths []string,
	cohortSpec, labelsPath, judgeSpec, judgeModel string,
	last int,
) error {
	if strings.TrimSpace(phase) == "" {
		return fmt.Errorf("--gate requires a non-empty phase")
	}
	if !runmanifest.IsValidIdentifier(phase) {
		return fmt.Errorf("--gate phase %q is not a valid identifier", phase)
	}
	if last <= 0 {
		return fmt.Errorf("--last must be positive")
	}
	if cohortSpec == "" {
		return fmt.Errorf("--cohort is required in gate mode")
	}
	if cohortSpec != "run-refs" {
		return fmt.Errorf("unknown gate-bench cohort %q (supported: run-refs)", cohortSpec)
	}

	variants, err := loadGatePromptVariants(variantPaths)
	if err != nil {
		return err
	}
	activeJudge, err := r.resolveJudge(ctx, judgeSpec, judgeModel)
	if err != nil {
		return err
	}

	store := refstore.New("")
	corpus := r.corpus
	if corpus == nil {
		corpus = bench.RunRefsSource{Store: store}
	}
	fixtures, err := corpus.Fixtures(ctx, bench.CohortSelector{Stage: phase, Last: last})
	if err != nil {
		return fmt.Errorf("load gate-bench cohort: %w", err)
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("no fixtures selected for gate phase %q", phase)
	}

	labels, labelsSupplied, err := loadGateLabels(labelsPath)
	if err != nil {
		return err
	}
	resolver := bench.GateLabelResolver{Explicit: labels, UseProgressionProxy: true}
	fixtureLabels := make([]*bench.GateLabel, len(fixtures))
	explicitMatches := 0
	for i, fixture := range fixtures {
		key := gateLabelKey(fixture)
		if _, ok := labels.Lookup(key); ok {
			explicitMatches++
		}
		label, ok, err := resolver.Resolve(fixture)
		if err != nil {
			return fmt.Errorf("resolve label for run %q stage %q round %d: %w", key.RunID, key.Stage, key.Round, err)
		}
		if ok {
			labelCopy := label
			fixtureLabels[i] = &labelCopy
		}
	}
	if labelsSupplied && explicitMatches == 0 {
		return fmt.Errorf("--labels matched zero selected fixtures by run_id, stage, and round")
	}

	evaluator := eval.GateEvaluator{Judge: activeJudge}
	cells := make([]gateBenchCell, 0, len(fixtures)*len(variants))
	successes := make([]int, len(fixtures))
	for fixtureIndex, fixture := range fixtures {
		key := gateLabelKey(fixture)
		mediaType := fixture.MediaType
		if mediaType == "" {
			mediaType = "text/plain; charset=utf-8"
		}
		artifactHash := sha256.Sum256(fixture.Artifact)
		for _, variant := range variants {
			cell := gateBenchCell{
				fixtureIndex: fixtureIndex,
				variant:      variant.display,
				key:          key,
				label:        fixtureLabels[fixtureIndex],
			}
			result, err := evaluator.Evaluate(ctx, eval.EvalRequest{
				Method: "gate",
				Targets: []eval.EvalInput{{
					Role:      phase,
					MediaType: mediaType,
					Content:   fixture.Artifact,
					Source: eval.ArtifactSource{
						RunID:    fixture.Provenance.RunID,
						Stage:    fixture.Provenance.Stage,
						Commit:   fixture.Provenance.SourceCommit,
						Artifact: fmt.Sprintf("%x", artifactHash),
					},
				}},
				Context: []eval.EvalInput{{
					Role:      eval.GatePromptRole,
					MediaType: "text/plain; charset=utf-8",
					Content:   variant.content,
				}},
			})
			if err != nil {
				cell.err = err
				cells = append(cells, cell)
				continue
			}
			cell.predicted, err = bench.GateVerdictFromPassed(result.Score.Passed)
			if err != nil {
				cell.err = err
				cells = append(cells, cell)
				continue
			}
			cell.findings = result.Findings
			successes[fixtureIndex]++
			cells = append(cells, cell)
		}
	}

	complete := make([]bool, len(fixtures))
	commonCount := 0
	for i, count := range successes {
		complete[i] = count == len(variants)
		if complete[i] {
			commonCount++
		}
	}

	predictions := make([]bench.GatePrediction, 0, commonCount*len(variants))
	for i := range cells {
		cells[i].inCommon = complete[cells[i].fixtureIndex]
		if cells[i].err != nil || !cells[i].inCommon {
			continue
		}
		prediction := bench.GatePrediction{
			Variant:   cells[i].variant,
			Key:       cells[i].key,
			Predicted: cells[i].predicted,
			Label:     cells[i].label,
		}
		predictions = append(predictions, prediction)
	}
	reports, err := bench.ScoreGatePredictions(predictions)
	if err != nil {
		return fmt.Errorf("score gate predictions: %w", err)
	}
	exclusions := gateBenchExclusions(fixtures, variants, cells, complete)
	renderGateBenchReport(out, phase, variants, reports, cells, exclusions, commonCount)
	if commonCount == 0 {
		return fmt.Errorf("no fixture succeeded for all variants")
	}
	return nil
}

func loadGatePromptVariants(paths []string) ([]gatePromptVariant, error) {
	if len(paths) < 2 {
		return nil, fmt.Errorf("gate mode requires at least two --variant prompt files")
	}
	seen := make(map[string]string, len(paths))
	variants := make([]gatePromptVariant, 0, len(paths))
	for _, display := range paths {
		if strings.TrimSpace(display) == "" {
			return nil, fmt.Errorf("--variant path must not be empty")
		}
		absolute, err := filepath.Abs(display)
		if err != nil {
			return nil, fmt.Errorf("resolve --variant %q: %w", display, err)
		}
		canonical, err := filepath.EvalSymlinks(filepath.Clean(absolute))
		if err != nil {
			return nil, fmt.Errorf("resolve --variant %q symlinks: %w", display, err)
		}
		canonical = filepath.Clean(canonical)
		if first, ok := seen[canonical]; ok {
			return nil, fmt.Errorf("--variant %q aliases %q; at least two canonically distinct prompt files are required", display, first)
		}
		content, err := os.ReadFile(canonical)
		if err != nil {
			return nil, fmt.Errorf("read --variant %q: %w", display, err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("--variant %q prompt must be non-empty", display)
		}
		seen[canonical] = display
		variants = append(variants, gatePromptVariant{display: display, content: content})
	}
	return variants, nil
}

func loadGateLabels(path string) (bench.GateLabelSet, bool, error) {
	if path == "" {
		return bench.GateLabelSet{}, false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return bench.GateLabelSet{}, true, fmt.Errorf("read --labels %q: %w", path, err)
	}
	labels, err := bench.ParseGateLabelsJSON(content)
	if err != nil {
		return bench.GateLabelSet{}, true, fmt.Errorf("parse --labels %q: %w", path, err)
	}
	return labels, true, nil
}

func gateLabelKey(fixture bench.Fixture) bench.GateLabelKey {
	return bench.GateLabelKey{
		RunID: fixture.Provenance.RunID,
		Stage: fixture.Provenance.Stage,
		Round: fixture.Provenance.Round,
	}
}

func gateBenchExclusions(fixtures []bench.Fixture, variants []gatePromptVariant, cells []gateBenchCell, complete []bool) []gateBenchExclusion {
	exclusions := make([]gateBenchExclusion, 0)
	for fixtureIndex, fixture := range fixtures {
		if complete[fixtureIndex] {
			continue
		}
		exclusion := gateBenchExclusion{key: gateLabelKey(fixture)}
		for _, variant := range variants {
			for _, cell := range cells {
				if cell.fixtureIndex == fixtureIndex && cell.variant == variant.display && cell.err != nil {
					exclusion.variants = append(exclusion.variants, variant.display)
					break
				}
			}
		}
		exclusions = append(exclusions, exclusion)
	}
	return exclusions
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
	return &replay.ExecRunner{
		Command:        strings.Fields(spec),
		Timeout:        r.timeout,
		MaxOutputBytes: 64 << 20,
	}, nil
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

	return &eval.ExecJudge{
		Command:        strings.Fields(spec),
		Model:          judgeModel,
		Timeout:        r.timeout,
		MaxOutputBytes: 64 << 20,
	}, nil
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
		fmt.Fprintln(w, "SOURCE RUN\tREPLAY RUN\tWINNER\tCONF\tEVAL ID\tFINDING\tCACHED")
		for _, o := range r.Outcomes {
			conf := "-"
			if o.Confidence != nil {
				conf = fmt.Sprintf("%.2f", *o.Confidence)
			}
			finding := "-"
			if len(o.Findings) > 0 {
				finding = o.Findings[0].Message
			}
			cached := ""
			if o.Reused {
				cached = "CACHED"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				o.SourceRunID, o.ReplayRunID, string(o.Winner), conf, o.EvalID, finding, cached)
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

func renderGateBenchReport(
	out io.Writer,
	phase string,
	variants []gatePromptVariant,
	reports []bench.GateVariantReport,
	cells []gateBenchCell,
	exclusions []gateBenchExclusion,
	commonCount int,
) {
	fmt.Fprintf(out, "gate bench %s: %d variants over %d common fixtures; %d excluded\n\n",
		phase, len(variants), commonCount, len(exclusions))

	if len(reports) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VARIANT\tACCURACY\tCATCH\tAVOID-OVERBLOCK\tCOMMON\tSCORED\tEXPLICIT\tPROXY\tUNLABELED\tTP\tFN\tFP\tTN")
		for _, report := range reports {
			explicit, proxy := gateLabelSourceCounts(report)
			fmt.Fprintf(w, "%s\t%.1f%%\t%.1f%%\t%.1f%%\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
				report.Variant,
				report.WinRate*100,
				report.CatchRate*100,
				report.AvoidOverblockRate*100,
				report.Total,
				report.Scored,
				explicit,
				proxy,
				report.Unlabeled,
				report.Matrix.TruePositive,
				report.Matrix.FalseNegative,
				report.Matrix.FalsePositive,
				report.Matrix.TrueNegative,
			)
		}
		w.Flush()
		fmt.Fprintln(out)
	}

	if len(cells) > 0 {
		outcomes := make(map[renderedGateCellKey]bench.GateOutcome)
		for _, report := range reports {
			for _, cell := range report.Cells {
				outcomes[renderedGateCellKey{variant: cell.Variant, key: cell.Key}] = cell.Outcome
			}
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "VARIANT\tRUN\tSTAGE\tROUND\tVERDICT\tEXPECTED\tLABEL-SOURCE\tVERIFIED\tOUTCOME\tSET\tFINDING")
		for _, cell := range cells {
			predicted := "-"
			if cell.predicted != "" {
				predicted = string(cell.predicted)
			}
			expected, source, verified, outcome := "-", "unlabeled", "-", "-"
			if cell.label != nil {
				expected = string(cell.label.Expected)
				source = string(cell.label.Source)
				verified = fmt.Sprintf("%t", cell.label.Verified)
				if cellOutcome, ok := outcomes[renderedGateCellKey{variant: cell.variant, key: cell.key}]; ok {
					outcome = string(cellOutcome)
				}
			}
			set := "common"
			if !cell.inCommon {
				set = "excluded"
			}
			finding := "-"
			if cell.err != nil {
				finding = "ERROR: " + cell.err.Error()
			} else if len(cell.findings) > 0 {
				finding = cell.findings[0].Message
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				cell.variant, cell.key.RunID, cell.key.Stage, cell.key.Round,
				predicted, expected, source, verified, outcome, set, oneLine(finding))
		}
		w.Flush()
		fmt.Fprintln(out)
	}

	if len(exclusions) > 0 {
		fmt.Fprintln(out, "Excluded fixtures (missing a verdict from every variant):")
		for _, exclusion := range exclusions {
			fmt.Fprintf(out, "  %s/%s/r%d: failed variants: %s\n",
				exclusion.key.RunID, exclusion.key.Stage, exclusion.key.Round,
				strings.Join(exclusion.variants, ", "))
		}
		fmt.Fprintln(out)
	}

	seenWarnings := make(map[string]bool)
	for _, report := range reports {
		for _, warning := range report.Warnings {
			if warning == "" || seenWarnings[warning] {
				continue
			}
			seenWarnings[warning] = true
			fmt.Fprintf(out, "WARNING: %s\n", warning)
		}
	}
}

type renderedGateCellKey struct {
	variant string
	key     bench.GateLabelKey
}

func gateLabelSourceCounts(report bench.GateVariantReport) (explicit, proxy int) {
	for _, cell := range report.Cells {
		switch cell.Source {
		case bench.GateLabelSourceExplicit:
			explicit++
		case bench.GateLabelSourceProgressionProxy:
			proxy++
		}
	}
	return explicit, proxy
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
