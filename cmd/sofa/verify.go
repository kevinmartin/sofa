package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/integrity"
)

type verifyOptions struct {
	configPath   string
	manifestPath string
	workspace    string
	bundlePath   string
	out          string
}

func newVerifyCommand() *cobra.Command {
	var opts verifyOptions
	cmd := newStageCommand("verify", "Verify the candidate with configured checks", "verify requires --config, --manifest, --workspace, --bundle, --out", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.manifestPath == "" || opts.workspace == "" || opts.bundlePath == "" || opts.out == "" {
			return errors.New("verify requires --config, --manifest, --workspace, --bundle, --out")
		}
		return runVerify(cmd.Context(), opts)
	})
	cmd.Flags().StringVar(&opts.configPath, "config", "", "Path to the consumer configuration")
	cmd.Flags().StringVar(&opts.manifestPath, "manifest", "", "Path to the admitted manifest")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "Absolute path to a clean verification checkout")
	cmd.Flags().StringVar(&opts.bundlePath, "bundle", "", "Path to the candidate bundle")
	cmd.Flags().StringVar(&opts.out, "out", "", "Path for verification evidence")
	return cmd
}

func runVerify(ctx context.Context, opts verifyOptions) (retErr error) {
	c, err := readConfig(opts.configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, true); err != nil {
		return err
	}
	checkHome, err := os.MkdirTemp("", "sofa-check-home-")
	if err != nil {
		return errors.New("cannot create isolated check home")
	}
	defer os.RemoveAll(checkHome)
	m, err := readManifest(opts.manifestPath, c)
	if err != nil {
		return err
	}
	failure := ExecutionFailure{
		Version:    2,
		AttemptID:  m.Fence.AttemptID,
		Generation: m.Fence.Generation,
		Reason:     "unexpected",
	}
	defer func() {
		if retErr != nil {
			failure.Kind = executionFailureKind(retErr)
			_ = writeJSON(filepath.Join(filepath.Dir(opts.out), "verification-failure.json"), failure)
		}
	}()
	b, err := readBundle(opts.bundlePath)
	if err != nil {
		return err
	}
	if err := cleanBase(ctx, opts.workspace, m.Grant.BaseSHA); err != nil {
		return err
	}
	if err := integrity.Apply(opts.workspace, b, bundleExpected(m, b), bundlePolicy(c)); err != nil {
		return err
	}
	baseline, err := candidateState(ctx, opts.workspace, b)
	if err != nil {
		return err
	}
	checks := make([]integrity.CheckEvidence, 0, len(c.Checks))
	for _, check := range c.Checks {
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
		cmd := exec.CommandContext(checkCtx, check.Argv[0], check.Argv[1:]...)
		cmd.Dir = opts.workspace
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + checkHome, "GOCACHE=" + filepath.Join(checkHome, "cache"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GOPROXY=off", "GOSUMDB=off", "GOENV=off"}
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		err := cmd.Run()
		cancel()
		if err != nil {
			failure.Reason = "configured-check"
			return fmt.Errorf("%w: configured check %s failed", errExecutionValidation, check.ID)
		}
		current, err := candidateState(ctx, opts.workspace, b)
		if err != nil {
			return err
		}
		if !baseline.matches(current) {
			failure.Reason = "workspace-changed"
			return fmt.Errorf("%w: configured check %s changed candidate workspace", errExecutionValidation, check.ID)
		}
		checks = append(checks, integrity.CheckEvidence{
			Version:         integrity.Version,
			Name:            check.ID,
			CandidateDigest: b.CandidateDigest,
			Passed:          true,
		})
	}
	return writeJSON(opts.out, checks)
}
