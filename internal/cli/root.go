package cli

import (
	"fmt"
	"io"
	"os"

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
	cmd.AddCommand(newGCCommand(out, errOut))
	cmd.AddCommand(newInitCommand(out, errOut))
	cmd.AddCommand(newReindexCommand(out, errOut))
	cmd.AddCommand(newReplayCommand(out, errOut))
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
	return err
}
