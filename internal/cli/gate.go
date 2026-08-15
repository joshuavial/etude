package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/liverun"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/registry"
	"github.com/joshuavial/etude/internal/replay"
	"github.com/joshuavial/etude/internal/runmanifest"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/spf13/cobra"
)

// ErrGateNotPassed is returned when a gate ran to completion but did not pass.
// The attempt IS recorded; the non-zero exit is what stops a supervisor from
// advancing. Both "rerun" and "escalated" produce this, so a seat outage is
// never distinguishable from success by exit code alone.
type ErrGateNotPassed struct {
	GateID string
	Status runmanifest.GateStatus
}

func (e *ErrGateNotPassed) Error() string {
	return fmt.Sprintf("gate %s did not pass: %s", e.GateID, e.Status)
}

func newGateCommand(out, errOut io.Writer) *cobra.Command {
	var runID, stageName, artifactPath, workflowFile string
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:   "gate",
		Short: "Run one stage's review gate and record the attempt on a run",
		Long: "Resolve a stage's gate tier and abstraction from .etude/workflow.yaml, " +
			"invoke that tier's seats from .etude/registry.yaml against one shared prompt, " +
			"and append the gate attempt to refs/etude/runs/<run>.\n\n" +
			"Exits 0 only when the gate passes. A blocked gate, a failing check, and a " +
			"seat outage all exit non-zero, so a supervisor cannot advance past a gate " +
			"that did not actually pass.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGateCommand(cmd.Context(), out, errOut, gateConfig{
				runID:        runID,
				stageName:    stageName,
				artifactPath: artifactPath,
				workflowFile: workflowFile,
				timeout:      timeout,
			})
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	flags := cmd.Flags()
	flags.StringVar(&runID, "run", "", "run id to gate and record the attempt on (required)")
	flags.StringVar(&stageName, "stage", "", "workflow stage whose gate to run (required)")
	flags.StringVar(&artifactPath, "artifact", "", "path to the artifact under review (required)")
	flags.StringVar(&workflowFile, "workflow", "", "named workflow in .etude/workflows/ (default: .etude/workflow.yaml)")
	flags.DurationVar(&timeout, "timeout", 10*time.Minute, "per-seat and per-check timeout")
	return cmd
}

type gateConfig struct {
	runID        string
	stageName    string
	artifactPath string
	workflowFile string
	timeout      time.Duration
}

func runGateCommand(ctx context.Context, out, errOut io.Writer, cfg gateConfig) error {
	if strings.TrimSpace(cfg.runID) == "" {
		return fmt.Errorf("--run is required")
	}
	if strings.TrimSpace(cfg.stageName) == "" {
		return fmt.Errorf("--stage is required")
	}
	if strings.TrimSpace(cfg.artifactPath) == "" {
		return fmt.Errorf("--artifact is required")
	}
	if err := validateCLIIdentifier("run id", cfg.runID); err != nil {
		return err
	}

	root, err := repoRoot(ctx)
	if err != nil {
		return err
	}

	wf, err := loadWorkflowByName(root, cfg.workflowFile)
	if err != nil {
		return err
	}

	stage, ok := findStage(wf, cfg.stageName)
	if !ok {
		return fmt.Errorf("unknown stage %q in %s; stages are: %s",
			cfg.stageName, workflowRelPath(cfg.workflowFile), strings.Join(stageNames(wf), ", "))
	}
	if stage.Gate == nil {
		return fmt.Errorf("stage %q has no gate configured in %s",
			cfg.stageName, workflowRelPath(cfg.workflowFile))
	}

	artifact, err := os.ReadFile(cfg.artifactPath)
	if err != nil {
		return fmt.Errorf("read artifact: %w", err)
	}
	if len(artifact) == 0 {
		return fmt.Errorf("artifact %s is empty; there is nothing to review", cfg.artifactPath)
	}

	// Registry is required here: a gate with a tier cannot resolve its seats
	// without it, and silently gating with zero seats would be the exact
	// failure this command exists to prevent.
	regBytes, err := os.ReadFile(filepath.Join(root, ".etude", "registry.yaml"))
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	reg, err := registry.ParseYAML(regBytes)
	if err != nil {
		return fmt.Errorf("parse registry: %w", err)
	}

	// A seat's `invoke` names a repo-relative adapter script. exec resolves a
	// relative program path against the CALLER's cwd, not the child's Dir, so a
	// gate run from a subdirectory would fail every seat with a confusing
	// "file not found" that reads as a seat outage. Anchor them to the repo root.
	anchorSeatCommands(&reg, root)

	envAllowlist := wf.EnvAllowlist

	// Scratch must NOT live under the worktree (ExecRunner rejects that), and the
	// worktree here is the repo root because a supervised gate reviews
	// UNCOMMITTED work — `make test` has to see the real tree.
	scratch, err := os.MkdirTemp("", "etude-gate-*")
	if err != nil {
		return fmt.Errorf("create gate scratch: %w", err)
	}
	defer os.RemoveAll(scratch)

	engine := &liverun.Engine{
		Store: refstore.New(root),
		ResolveCheck: func(r workflow.Runner) (liverun.CheckRunner, error) {
			return liverun.ResolveCheckRunner(reg, r, cfg.timeout, envAllowlist)
		},
		ResolveSeat: func(seatName string) (replay.Runner, liverun.SeatMeta, error) {
			return liverun.ResolveGateSeat(reg, seatName, cfg.timeout, envAllowlist)
		},
		// Walk the seat's full invocation ladder, so a failed primary falls through
		// to a configured fallback instead of being recorded as a flat outage.
		// Both commands share this: two paths resolving seats differently is the
		// second-seat-path problem this machinery exists to avoid.
		ResolveSeatCandidates: func(seatName string) ([]liverun.SeatCandidate, error) {
			return liverun.ResolveGateSeatCandidates(reg, seatName, cfg.timeout, envAllowlist)
		},
		Tiers:        liverun.ResolveTiers(reg),
		Root:         root,
		EnvAllowlist: envAllowlist,
	}

	outcome, err := engine.GateStage(ctx, out, liverun.GateRequest{
		RunID:       cfg.runID,
		Stage:       stage,
		Artifact:    artifact,
		WorktreeDir: root,
		ScratchDir:  scratch,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "recorded %s on refs/etude/runs/%s\n", outcome.GateID, cfg.runID)
	if !outcome.Passed() {
		fmt.Fprintf(errOut, "gate did not pass; the phase must not advance\n")
		return &ErrGateNotPassed{GateID: outcome.GateID, Status: outcome.Status}
	}
	return nil
}

// loadWorkflowByName reads .etude/workflow.yaml, or .etude/workflows/<name>.yaml
// when a named workflow is requested. There is deliberately no fallback between
// the two, so a missing named workflow never silently reports the default.
func loadWorkflowByName(root, workflowFile string) (workflow.Workflow, error) {
	if strings.ContainsAny(workflowFile, `/\`) {
		return workflow.Workflow{}, fmt.Errorf("--workflow takes a name, not a path: %q", workflowFile)
	}
	bytes, err := os.ReadFile(filepath.Join(root, workflowRelPath(workflowFile)))
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("load workflow: %w", err)
	}
	wf, err := workflow.ParseYAML(bytes)
	if err != nil {
		return workflow.Workflow{}, fmt.Errorf("parse workflow: %w", err)
	}
	return wf, nil
}

func workflowRelPath(workflowFile string) string {
	if workflowFile != "" {
		return filepath.Join(".etude", "workflows", workflowFile+".yaml")
	}
	return filepath.Join(".etude", "workflow.yaml")
}

func findStage(wf workflow.Workflow, name string) (workflow.Stage, bool) {
	for _, s := range wf.Stages {
		if s.Name == name {
			return s, true
		}
	}
	return workflow.Stage{}, false
}

func stageNames(wf workflow.Workflow) []string {
	names := make([]string, 0, len(wf.Stages))
	for _, s := range wf.Stages {
		names = append(names, s.Name)
	}
	return names
}

// anchorSeatCommands rewrites each seat's invoke so a repo-relative program
// path (e.g. "scripts/seat-adapter.sh") resolves against the repo root.
//
// Only the FIRST field is rewritten, and only when it looks like a relative path
// (contains a separator) and exists under root. A bare command name is left
// alone so it still resolves via PATH, and an absolute path is untouched.
func anchorSeatCommands(reg *registry.Registry, root string) {
	for name, seat := range reg.Seats {
		// Anchor the primary AND every fallback independently. A seat whose
		// primary is a bare PATH command needs no anchoring itself, but its
		// fallbacks may still name a repo-relative script — so this must not
		// hang off whether the primary happened to be rewritten.
		seat.Invoke = anchorCommand(seat.Invoke, root)
		for i, fb := range seat.InvocationFallbacks {
			seat.InvocationFallbacks[i].Invoke = anchorCommand(fb.Invoke, root)
		}
		reg.Seats[name] = seat
	}
}

// anchorCommand rewrites a repo-relative program path to an absolute one under
// root. It leaves absolute paths, bare command names (resolved via PATH), and
// non-command markers such as "in-harness:..." untouched.
func anchorCommand(invoke, root string) string {
	if strings.HasPrefix(invoke, "in-harness:") {
		return invoke
	}
	fields := strings.Fields(invoke)
	if len(fields) == 0 {
		return invoke
	}
	prog := fields[0]
	if filepath.IsAbs(prog) || !strings.ContainsRune(prog, filepath.Separator) {
		return invoke
	}
	candidate := filepath.Join(root, prog)
	if _, err := os.Stat(candidate); err != nil {
		return invoke
	}
	fields[0] = candidate
	return strings.Join(fields, " ")
}
