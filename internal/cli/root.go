package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

var version = "dev"

const rootLong = "etude captures stage artifacts as git-native run records. " +
	"Replay and bench commands are planned but not implemented yet."

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
	cmd.AddCommand(newCaptureCommand(out, errOut))
	cmd.AddCommand(newInitCommand(out, errOut))

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
