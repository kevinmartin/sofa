package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	release "github.com/kevinmartin/sofa/internal/distribution"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/spf13/cobra"
)

func readReleaseJSON(path string, output any) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("release input unavailable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return errors.New("release input exceeds bound")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(output) != nil {
		return errors.New("release input invalid")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("release input trailing content")
	}
	return nil
}

func boundedReleaseArgs(_ *cobra.Command, args []string) error {
	if len(args) > 0 {
		return errors.New("invalid release command arguments")
	}
	return nil
}

func releaseAuthority(major string) error {
	if major != "v0" && major != "v1" {
		return errors.New("release automation supports only v0 and v1")
	}
	if major == "v1" && os.Getenv("SOFA_V1_ENABLED") != "true" {
		return errors.New("stable v1 release activation requires Kevin's approval")
	}
	return nil
}

func newReleaseCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "release",
		Short: "Publish and promote approved Sofa binary releases",
		Args:  boundedReleaseArgs,
		RunE:  func(*cobra.Command, []string) error { return errors.New("release subcommand required") },
	}
	var planPath string
	command.PersistentFlags().StringVar(&planPath, "plan", "release-plan.json", "Trusted release plan")
	load := func() (release.Plan, error) {
		var p release.Plan
		if err := readReleaseJSON(planPath, &p); err != nil {
			return p, err
		}
		return p, p.Validate()
	}
	client := func(key string) (*github.Client, error) { return github.New(os.Getenv(key), nil) }
	var configPath, source string
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Allocate or resume an exact source release",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var config release.Config
			if err := readReleaseJSON(configPath, &config); err != nil {
				return err
			}
			if err := config.Validate(); err != nil {
				return err
			}
			if err := releaseAuthority("v" + strings.Split(config.Series, ".")[0]); err != nil {
				return err
			}
			api, err := client("SOFA_RELEASE_TOKEN")
			if err != nil {
				return err
			}
			p, err := release.Allocate(cmd.Context(), api, config, source)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
		},
	}
	plan.Flags().StringVar(&configPath, "config", ".github/sofa-release.json", "Reviewed release series configuration")
	plan.Flags().StringVar(&source, "source", "", "Validated main commit SHA")
	command.AddCommand(plan)
	var rollbackVersion, expectedChannel string
	rollback := &cobra.Command{
		Use:   "rollback-plan",
		Short: "Plan an owner-authorized rollback after target state compatibility testing",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			major, err := release.VersionMajor(rollbackVersion)
			if err != nil {
				return err
			}
			if err := releaseAuthority(major); err != nil {
				return err
			}
			api, err := client("SOFA_RELEASE_TOKEN")
			if err != nil {
				return err
			}
			p, err := release.RollbackPlan(cmd.Context(), api, rollbackVersion, expectedChannel)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(p)
		},
	}
	rollback.Flags().StringVar(&rollbackVersion, "release-version", "", "Verified previous exact immutable release")
	rollback.Flags().StringVar(&expectedChannel, "expected-channel-sha", "", "Observed current major channel commit")
	command.AddCommand(rollback)
	var root, output string
	build := &cobra.Command{
		Use:   "build",
		Short: "Build the approved Linux amd64 bundle without release credentials",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			return release.Build(cmd.Context(), p, root, output)
		},
	}
	build.Flags().StringVar(&root, "root", ".", "Exact approved source checkout")
	build.Flags().StringVar(&output, "output", "release-bundle", "Bundle output directory")
	command.AddCommand(build)
	var bundlePath string
	publish := &cobra.Command{
		Use:   "publish",
		Short: "Resume immutable publication without changing channel tags",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := releaseAuthority(p.Channel); err != nil {
				return err
			}
			file, err := os.Open(bundlePath)
			if err != nil {
				return errors.New("release bundle unavailable")
			}
			defer file.Close()
			info, err := file.Stat()
			if err != nil || !info.Mode().IsRegular() || info.Size() > 128<<20 {
				return errors.New("release bundle exceeds bound")
			}
			content, err := io.ReadAll(io.LimitReader(file, 128<<20+1))
			if err != nil || len(content) > 128<<20 {
				return errors.New("release bundle unavailable")
			}
			if err := release.ValidateBundle(p, content); err != nil {
				return err
			}
			api, err := client("SOFA_RELEASE_TOKEN")
			if err != nil {
				return err
			}
			record, err := release.Publish(cmd.Context(), api, p, content)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(record)
		},
	}
	publish.Flags().StringVar(&bundlePath, "bundle", filepath.Join("release-bundle", release.BundleName), "Trusted same-workflow bundle")
	command.AddCommand(publish)
	canary := &cobra.Command{
		Use:   "canary",
		Short: "Dispatch and observe an exact verified-release disposable canary",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := releaseAuthority(p.Channel); err != nil {
				return err
			}
			api, err := client("SOFA_RELEASE_CANARY_TOKEN")
			if err != nil {
				return err
			}
			evidence, err := release.RunCanary(cmd.Context(), api, p, os.Getenv("GITHUB_RUN_ID"), os.Getenv("GITHUB_RUN_ATTEMPT"), 5*time.Second)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(evidence)
		},
	}
	command.AddCommand(canary)
	var canaryPath string
	promote := &cobra.Command{
		Use:   "promote",
		Short: "Advance a major channel only after its exact successful canary",
		Args:  boundedReleaseArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := releaseAuthority(p.Channel); err != nil {
				return err
			}
			var evidence release.Canary
			if err := readReleaseJSON(canaryPath, &evidence); err != nil {
				return err
			}
			canaryAPI, err := client("SOFA_RELEASE_CANARY_TOKEN")
			if err != nil {
				return err
			}
			run, err := canaryAPI.ReleaseCanaryRun(cmd.Context(), release.DisposableRepository, evidence.RunID)
			if err != nil {
				return err
			}
			api, err := client("SOFA_RELEASE_TOKEN")
			if err != nil {
				return err
			}
			return release.Promote(cmd.Context(), api, p, run, evidence)
		},
	}
	promote.Flags().StringVar(&canaryPath, "canary", "release-canary.json", "Matching completed canary record")
	command.AddCommand(promote)
	return command
}
