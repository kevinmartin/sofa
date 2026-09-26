package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

type failOptions struct {
	configPath   string
	manifestPath string
	failurePath  string
}

func newFailCommand() *cobra.Command {
	var opts failOptions
	cmd := newStageCommand("fail", "Finalize a failed execution attempt", "fail requires --config, --manifest, --failure", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.manifestPath == "" || opts.failurePath == "" {
			return errors.New("fail requires --config, --manifest, --failure")
		}
		return runFail(cmd.Context(), opts)
	})
	cmd.Flags().StringVar(&opts.configPath, "config", "", "Path to the consumer configuration")
	cmd.Flags().StringVar(&opts.manifestPath, "manifest", "", "Path to the admitted manifest")
	cmd.Flags().StringVar(&opts.failurePath, "failure", "", "Path to the bounded failure artifact")
	return cmd
}

// runFail is a privileged finalizer. The worker supplies only a fixed failure
// class; it cannot mint ownership.
func runFail(ctx context.Context, opts failOptions) error {
	c, err := readConfig(opts.configPath)
	if err != nil {
		return err
	}
	if os.Getenv("SOFA_PROJECTS_TOKEN") != "" || os.Getenv("SOFA_PUBLISH_TOKEN") != "" || os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("finalizer has an unnecessary privileged credential")
	}
	m, err := readManifest(opts.manifestPath, c)
	if err != nil {
		return err
	}
	currentOwner, err := ownerFromEnv()
	if err != nil || currentOwner != m.Fence.Owner {
		return errors.New("failure manifest does not belong to this Actions attempt")
	}
	var failure ExecutionFailure
	if err := readJSON(opts.failurePath, 4096, &failure); err != nil {
		return err
	}
	if err := validateExecutionFailure(failure, m); err != nil {
		return err
	}
	client, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	engine := state.Engine{
		Store: github.StateStore{
			Client:     client,
			Repository: c.Repository,
		},
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		return err
	}
	attempt, ok := snapshot.State.Attempts[m.Fence.AttemptID]
	if !ok || attempt.Admission != ledgerAdmission(m.Grant) || attempt.Generation != m.Fence.Generation || attempt.Owner == nil || *attempt.Owner != m.Fence.Owner {
		return errors.New("failure ledger identity mismatch")
	}
	if !((attempt.Phase == state.Blocked || attempt.Phase == state.Deferred) && attempt.Failure == failure.Kind) {
		if err := engine.AssertOwner(ctx, m.Fence); err != nil {
			return err
		}
		if err := engine.Fail(ctx, m.Fence, failure.Kind); err != nil {
			return err
		}
	}
	return observeOnce(ctx, engine, m.Fence.AttemptID, "failure-finalizer", failure.Kind, m.Grant.BaseSHA, "", fmt.Sprintf("g%d", m.Fence.Generation))
}
