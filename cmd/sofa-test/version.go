package main

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/version"
)

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print machine-readable CLI version and source commit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(version.Current())
		},
	}
}
