package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshuavial/etude/internal/nudge"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/joshuavial/etude/internal/workflow"
	"github.com/spf13/cobra"
)

var version = "dev"

const rootLong = "etude captures stage artifacts as git-native run records. " +
	"The replay command re-executes a recorded stage end-to-end. " +
	"The bench command benchmarks a cohort of runs by replaying and judging replay vs original."

// NewRootCommand constructs the root command so tests can execute it without
// touching process-global stdout, stderr, or argv.
func NewRootCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "etude",
		Short:         "Root CLI scaffold for etude",
		Long:          rootLong,
		Version:       version,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	cmd.AddCommand(newBenchCommand(out, errOut))
	cmd.AddCommand(newCaptureCommand(out, errOut))
	cmd.AddCommand(newCaptureGateCommand(out, errOut))
	cmd.AddCommand(newCaptureRunCommand(out, errOut))
	cmd.AddCommand(newGCCommand(out, errOut))
	cmd.AddCommand(newImportCommand(out, errOut))
	cmd.AddCommand(newInitCommand(out, errOut))
	cmd.AddCommand(newLogCommand(out, errOut))
	cmd.AddCommand(newPrimeCommand(out, errOut))
	cmd.AddCommand(newReindexCommand(out, errOut))
	cmd.AddCommand(newReplayCommand(out, errOut))
	cmd.AddCommand(newRetroCommand(out, errOut))
	cmd.AddCommand(newRunCommand(out, errOut))
	cmd.AddCommand(newSyncCommand(out, errOut))

	return cmd
}

// Execute runs the root CLI using standard output.
func Execute() error {
	return ExecuteWithWriters(os.Stdout, os.Stderr, os.Args[1:])
}

// ExecuteWithWriters runs the root CLI with caller-supplied streams and args.
func ExecuteWithWriters(out, errOut io.Writer, args []string) error {
	cmd := NewRootCommand(out, errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(errOut, err)
	}
	// Run the best-effort retro-nudge AFTER the subcommand finishes — including
	// on subcommand failure (acceptance criterion 4). The emitter itself is
	// silent-no-op on any internal error so it never alters err or the parent
	// command's output beyond a single stderr reminder line.
	emitRetroNudge(context.Background(), cmd, args, errOut)
	return err
}

// emitRetroNudge writes the retro-nudge stderr line when, per
// .etude/workflow.yaml, the number of runs since the most recent retro has
// reached the configured threshold AND no fresh snooze suppresses it.
//
// The function is silently a no-op for help, version, completion, the entire
// `etude retro nudge` subtree, and any error path (missing workflow.yaml,
// disabled nudge, missing git repo, refstore failure, snooze parse error,
// ...). It must NEVER crash or alter the parent command's exit status — a
// deferred recover() exists as a belt-and-braces guarantee against future
// refactors introducing a panic vector in the called code.
func emitRetroNudge(ctx context.Context, rootCmd *cobra.Command, args []string, errOut io.Writer) {
	defer func() {
		_ = recover()
	}()
	// Skip when no meaningful subcommand was invoked.
	if shouldSkipNudgeArgs(args) {
		return
	}
	// Resolve the actual cobra command to skip help/completion/version and the
	// retro-nudge subtree without string-matching arbitrary args.
	resolved, _, _ := rootCmd.Find(args)
	if shouldSkipNudgeCommand(resolved) {
		return
	}

	// Cheap-checks-first: short-circuit on disabled nudge BEFORE touching the
	// refstore so opted-out repos pay zero ref I/O. Bound git rev-parse with a
	// short timeout so a wedged git (network filesystem, hung index lock)
	// cannot wedge etude itself.
	rootCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	root, err := nudgeRepoRoot(rootCtx)
	if err != nil {
		return
	}
	wf, ok := loadWorkflowForNudge(root)
	if !ok || !wf.NudgeEnabled() {
		return
	}

	store := refstore.New("")
	// Reuse rootCtx so the best-effort path is uniformly bounded — both the
	// git rev-parse and the refstore walk share the 2s budget.
	count, lastRetroID, err := nudge.CountRunsSinceLastRetro(rootCtx, store)
	if err != nil {
		return
	}
	snooze, snoozePresent, _ := nudge.ReadSnooze(root) // parse errors → treat as not present, silent

	st := nudge.Decide(true, wf.NudgeThreshold(), count, lastRetroID, snooze, snoozePresent)
	if st.WouldEmit {
		fmt.Fprint(errOut, nudge.NudgeLine(st.RunsSinceLastRetro, st.Threshold))
	}
}

// shouldSkipNudgeArgs handles the trivial cases that don't need a parse.
func shouldSkipNudgeArgs(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, a := range args {
		switch a {
		case "--help", "-h", "--version":
			return true
		}
	}
	return false
}

// shouldSkipNudgeCommand inspects the cobra-resolved command and skips the
// nudge for help, shell-completion (auto-registered by cobra under the names
// `completion`, `__complete`, and `__completeNoDesc`), and the entire
// `retro nudge` subtree (so a status JSON on stdout is never accompanied by
// a stderr nudge line).
func shouldSkipNudgeCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return true
	}
	// Walk up the parent chain and check each command name. This covers
	// `etude completion`, `etude completion bash`, `etude __complete`, and the
	// retro-nudge subtree (`retro nudge`, `retro nudge dismiss`, `retro nudge
	// status`).
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "help", "completion", "__complete", "__completeNoDesc":
			return true
		case "nudge":
			if c.Parent() != nil && c.Parent().Name() == "retro" {
				return true
			}
		}
	}
	// Root command (no real subcommand chosen — help screen / no args path).
	if !cmd.HasParent() {
		return true
	}
	return false
}

// loadWorkflowForNudge reads .etude/workflow.yaml under root. Returns
// (workflow, true) when readable and valid; (zero, false) otherwise.
func loadWorkflowForNudge(root string) (workflow.Workflow, bool) {
	path := filepath.Join(root, ".etude", "workflow.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return workflow.Workflow{}, false
	}
	wf, err := workflow.ParseYAML(raw)
	if err != nil {
		return workflow.Workflow{}, false
	}
	return wf, true
}

// nudgeRepoRoot returns the git repo root for the current cwd. It deliberately
// duplicates the equivalent helper in init.go rather than introducing a
// new internal coupling for one-line use; the function is short and self-
// contained, and the init.go variant takes a ctx the nudge path needs to be
// able to pass through.
func nudgeRepoRoot(ctx context.Context) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
