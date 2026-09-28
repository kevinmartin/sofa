package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/managedconfig"
	"github.com/spf13/cobra"
)

func newConfigCommand() *cobra.Command {
	var repository string
	var manifestDir string
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Render and reconcile managed repository files",
		Args:  cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&repository, "repo", "", "Enrolled owner/repository")
	cmd.PersistentFlags().StringVar(&manifestDir, "manifest-dir", "managed-repos", "Trusted manifest directory")
	load := func() (managedconfig.Spec, error) {
		if repository == "" {
			return managedconfig.Spec{}, errors.New("--repo is required")
		}
		return managedconfig.Load(manifestDir, repository)
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "render",
		Short: "Print the desired managed files as JSON",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			spec, err := load()
			if err != nil {
				return err
			}
			files, err := managedconfig.Render(spec)
			if err != nil {
				return err
			}
			if err := managedconfig.ValidateActionlint(command.Context(), files); err != nil {
				return err
			}
			readable := make(map[string]string, len(files))
			for path, content := range files {
				readable[path] = string(content)
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(readable)
		},
	})
	var root string
	check := &cobra.Command{
		Use:   "check",
		Short: "Verify a local checkout matches its managed files",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			spec, err := load()
			if err != nil {
				return err
			}
			files, err := managedconfig.Render(spec)
			if err != nil {
				return err
			}
			if err := managedconfig.ValidateActionlint(command.Context(), files); err != nil {
				return err
			}
			paths := make([]string, 0, len(files))
			for path := range files {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				actual, err := os.ReadFile(filepath.Join(root, path))
				if err != nil || !bytes.Equal(actual, files[path]) {
					return fmt.Errorf("managed file %s differs from its manifest", path)
				}
			}
			fmt.Fprintln(command.OutOrStdout(), "managed configuration is current")
			return nil
		},
	}
	check.Flags().StringVar(&root, "root", ".", "Repository checkout to check")
	cmd.AddCommand(check)
	var apply bool
	reconcile := &cobra.Command{
		Use:   "reconcile",
		Short: "Inspect remote drift or open/update its config PR",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			spec, err := load()
			if err != nil {
				return err
			}
			api := github.NewAnonymous(nil)
			if token := os.Getenv("SOFA_CONFIG_TOKEN"); token != "" {
				api, err = github.New(token, nil)
				if err != nil {
					return err
				}
			}
			client := managedconfig.Reconciler{GitHub: api}
			result, err := client.Reconcile(command.Context(), spec, apply)
			if err != nil {
				return err
			}
			return json.NewEncoder(command.OutOrStdout()).Encode(result)
		},
	}
	reconcile.Flags().BoolVar(&apply, "apply", false, "Open or update a reviewable config PR")
	cmd.AddCommand(reconcile)
	return cmd
}
