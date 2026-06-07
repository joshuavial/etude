package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/joshuavial/etude/internal/nudge"
	"github.com/joshuavial/etude/internal/refstore"
	"github.com/spf13/cobra"
)

// newRetroNudgeCommand registers the `retro nudge` subcommand group under
// `etude retro`. The group has two leaf subcommands:
//
//   - `retro nudge dismiss [--for N]` — record a snooze, default N=1.
//   - `retro nudge status` — print a JSON Status object to stdout.
//
// The group itself prints its own help when called with no subcommand.
func newRetroNudgeCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nudge",
		Short: "Manage the retro overdue reminder (nudge)",
		Long: "The retro nudge is an opt-in reminder emitted on stderr by any etude " +
			"command when, per .etude/workflow.yaml, the number of captured runs since " +
			"the most recent retro has reached a configured threshold. The dismiss " +
			"subcommand snoozes the nudge for one or more upcoming runs; the status " +
			"subcommand prints the current decision as JSON.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.AddCommand(newRetroNudgeDismissCommand(out, errOut))
	cmd.AddCommand(newRetroNudgeStatusCommand(out, errOut))
	return cmd
}

func newRetroNudgeDismissCommand(out, errOut io.Writer) *cobra.Command {
	var forN int
	cmd := &cobra.Command{
		Use:   "dismiss",
		Short: "Snooze the retro nudge for the next N bead(s) (default 1)",
		Long: "Records a snooze in .git/etude/retro-nudge-snooze.json. The snooze " +
			"silences the nudge while runs-since-last-retro grows by fewer than --for " +
			"more beads. A new retro automatically invalidates the snooze.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if forN < 1 {
				return fmt.Errorf("--for must be >= 1; got %d", forN)
			}
			ctx := cmd.Context()
			root, err := nudgeRepoRoot(ctx)
			if err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}
			store := refstore.New("")
			count, lastRetroID, err := nudge.CountRunsSinceLastRetro(ctx, store)
			if err != nil {
				return err
			}
			now := time.Now().UTC()
			sn := nudge.Snooze{
				RunsAtSnooze:        count,
				SnoozeFor:           forN,
				SnoozedAt:           now,
				LastRetroIDAtSnooze: lastRetroID,
			}
			if err := nudge.WriteSnooze(root, sn); err != nil {
				return err
			}
			fmt.Fprintf(out,
				"snoozed retro nudge for %d more bead(s); next reminder when runs-since-last-retro reaches %d\n",
				forN, count+forN,
			)
			return nil
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.Flags().IntVar(&forN, "for", 1, "snooze the nudge for the next N completed bead(s) before reminding again")
	return cmd
}

func newRetroNudgeStatusCommand(out, errOut io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "Print the current retro-nudge decision as JSON",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			root, err := nudgeRepoRoot(ctx)
			if err != nil {
				return fmt.Errorf("not a git repository: %w", err)
			}
			wf, wfOK := loadWorkflowForNudge(root)
			// status always succeeds even when the nudge is disabled / absent
			// (per acceptance criterion 6); falling back to a zero-config Status
			// matches that contract.
			enabled := false
			threshold := 3
			if wfOK {
				enabled = wf.NudgeEnabled()
				threshold = wf.NudgeThreshold()
			}

			store := refstore.New("")
			count, lastRetroID, err := nudge.CountRunsSinceLastRetro(ctx, store)
			if err != nil {
				return err
			}
			snooze, present, err := nudge.ReadSnooze(root)
			if err != nil {
				// A corrupted snooze is a user-visible problem in status mode.
				return err
			}
			st := nudge.Decide(enabled, threshold, count, lastRetroID, snooze, present)
			raw, err := json.MarshalIndent(st, "", "  ")
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(out, string(raw))
			return err
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	return cmd
}
