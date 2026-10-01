package main

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"
)

// newRootCommand assembles the CLI with bounded parser errors and explicit help routing.
func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sofa",
		Short:         "Run approved issue work through the SOFA trust stages",
		SilenceErrors: true,
		SilenceUsage:  true,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New("unknown command")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("command required: admit, execute, verify, publish, fail, discovery, lifecycle, review-repair, spec-digest, config")
		},
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error {
		return errors.New("invalid command flags")
	})
	cmd.AddCommand(
		newAdmitCommand(),
		newExecuteCommand(),
		newVerifyCommand(),
		newPublishCommand(),
		newFailCommand(),
		newDiscoveryCommand(),
		newLifecycleCommand(),
		newReviewRepairCommand(),
		newSpecDigestCommand(),
		newConfigCommand(),
	)
	cmd.SetHelpCommand(newHelpCommand(cmd))
	return cmd
}

func newHelpCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Show help for a command",
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) > 1 {
				return errors.New("unknown command")
			}
			target := root
			if len(args) == 1 {
				found, _, err := root.Find(args)
				if err != nil || found == root {
					return errors.New("unknown command")
				}
				target = found
			}
			target.InitDefaultHelpFlag()
			return target.Help()
		},
	}
}

// newStageCommand keeps parser failures bounded: flag values and unexpected
// arguments may contain credentials, so they must never appear in diagnostics.
func newStageCommand(name, short, requirements string, run func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   name,
		Short: short,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 0 {
				return errors.New(requirements)
			}
			return nil
		},
		RunE: run,
	}
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, _ error) error {
		return errors.New(requirements)
	})
	return cmd
}

func run(ctx context.Context, args []string) error {
	return executeCommand(ctx, newRootCommand(), args)
}

func executeCommand(ctx context.Context, root *cobra.Command, args []string) error {
	// Cobra adds hidden completion commands during Execute even when its visible
	// completion command is disabled. Their diagnostics can echo flag values.
	for _, arg := range args {
		if arg == cobra.ShellCompRequestCmd || arg == cobra.ShellCompNoDescRequestCmd {
			return errors.New("unknown command")
		}
	}
	if len(args) != 0 && !strings.HasPrefix(args[0], "-") {
		cmd, _, err := root.Find(args[:1])
		if err != nil {
			return errors.New("unknown command")
		}
		args = append([]string{args[0]}, normalizeLegacyFlags(cmd, args[1:])...)
	}
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// The original flag package accepted both -config and --config. Keep that
// spelling compatible while Cobra owns flag parsing and command help.
func normalizeLegacyFlags(cmd *cobra.Command, args []string) []string {
	normalized := append([]string(nil), args...)
	for i := 0; i < len(normalized); i++ {
		arg := normalized[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			normalized[i] = "-" + arg
		}
		if !hasValue && flag.NoOptDefVal == "" {
			i++ // A value beginning with '-' is still a value, not another flag.
		}
	}
	return normalized
}
