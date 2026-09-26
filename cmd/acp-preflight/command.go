package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/agent"
)

var errPreflightFailed = errors.New("ACP preflight failed")

func newRootCommand(probe func(config) result) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acp-preflight",
		Short: "Check Copilot ACP startup without sending a model prompt",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("unexpected argument")
			}
			return nil
		},
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("SOFA_MODEL_TOKEN")
			if token == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "sofa ACP preflight: missing model credential")
				return errPreflightFailed
			}
			workspace := os.Getenv("SOFA_WORKSPACE")
			if workspace == "" {
				workspace = "/workspace"
			}
			c := config{
				workspace: workspace,
				token:     token,
				timeout:   30 * time.Second,
			}
			c.command, c.args = agent.CopilotCommand()
			r := probe(c)
			fmt.Fprintln(cmd.OutOrStdout(), r.String())
			if !r.ok {
				return errPreflightFailed
			}
			return nil
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error {
		return errors.New("invalid command flags")
	})
	return cmd
}

func executePreflightCommand(cmd *cobra.Command, args []string) error {
	// Hidden shell completion may print invalid flag values to stderr.
	for _, arg := range args {
		if arg == cobra.ShellCompRequestCmd || arg == cobra.ShellCompNoDescRequestCmd {
			return errors.New("unknown command")
		}
	}
	cmd.SetArgs(args)
	return cmd.Execute()
}
