// sofa-test contains the test-only fixture, ACP peer, and trusted gate bridge
// commands. Each invocation runs in its workflow's own credential boundary.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sofa-test:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := newRootCommand()
	cmd.SetArgs(args)
	return cmd.Execute()
}

func newRootCommand() *cobra.Command {
	var acpMode, stdio bool
	cmd := &cobra.Command{
		Use:           "sofa-test",
		Short:         "Run sofa's deterministic hosted-test helpers",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			if !acpMode || !stdio {
				return errors.New("a test helper command is required")
			}
			return runFakeACP()
		},
	}
	cmd.CompletionOptions.DisableDefaultCmd = true
	cmd.Flags().BoolVar(&acpMode, "acp", false, "ACP harness mode")
	cmd.Flags().BoolVar(&stdio, "stdio", false, "ACP standard I/O transport")
	_ = cmd.Flags().MarkHidden("acp")
	_ = cmd.Flags().MarkHidden("stdio")
	cmd.AddCommand(newPrepareCommand(), newRecoverCommand(), newReportCommand(), newDenyCommand(), newGateBridgeCommand())
	return cmd
}
