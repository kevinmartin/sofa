package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/worker"
)

type executeOptions struct {
	configPath   string
	manifestPath string
	workspace    string
	out          string
}

func newExecuteCommand() *cobra.Command {
	var opts executeOptions
	cmd := newStageCommand("execute", "Create a candidate in the admitted workspace", "execute requires --config, --manifest, --workspace, --out", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.manifestPath == "" || opts.workspace == "" || opts.out == "" {
			return errors.New("execute requires --config, --manifest, --workspace, --out")
		}
		return runExecute(cmd.Context(), opts)
	})
	cmd.Flags().StringVar(&opts.configPath, "config", "", "Path to the consumer configuration")
	cmd.Flags().StringVar(&opts.manifestPath, "manifest", "", "Path to the admitted manifest")
	cmd.Flags().StringVar(&opts.workspace, "workspace", "", "Absolute path to the candidate checkout")
	cmd.Flags().StringVar(&opts.out, "out", "", "Path for the candidate bundle")
	return cmd
}

func runExecute(ctx context.Context, opts executeOptions) (retErr error) {
	c, err := readConfig(opts.configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, false); err != nil {
		return err
	}
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
			_ = writeJSON(filepath.Join(filepath.Dir(opts.out), "execution-failure.json"), failure)
			fmt.Fprintf(os.Stderr, "sofa: execution failure reason=%s used_agent=%t prompt_requests=%d updates=%d permission_requests=%d permission_denials=%d permission_execute_denials=%d tool_reads=%d tool_edits=%d tool_executes=%d tool_others=%d tool_failed_updates=%d\n", failure.Reason, failure.UsedAgent, failure.PromptRequests, failure.Updates, failure.PermissionRequests, failure.PermissionDenials, failure.PermissionExecuteDenials, failure.ToolReads, failure.ToolEdits, failure.ToolExecutes, failure.ToolOthers, failure.ToolFailedUpdates)
		}
	}()
	if m.Recovery != nil || m.RecoveryCheckpoint != nil {
		failure.Reason = "recovery-input"
		return errExecutionValidation
	}
	if err := cleanBase(ctx, opts.workspace, m.Grant.BaseSHA); err != nil {
		failure.Reason = "base-checkout"
		return fmt.Errorf("%w: admitted base checkout", errExecutionValidation)
	}
	result, err := worker.Execute(ctx, worker.Input{
		Config:        c,
		CanonicalSpec: m.CanonicalSpec,
		Directory:     opts.workspace,
		AttemptID:     m.Fence.AttemptID,
		Generation:    uint64(m.Fence.Generation),
		BaseSHA:       m.Grant.BaseSHA,
		ModelToken:    os.Getenv(c.Profile.SecretEnv),
	})
	failure.UsedAgent = result.UsedAgent
	failure.PromptRequests = result.PromptRequests
	failure.Updates = result.Updates
	failure.PermissionRequests = result.PermissionRequests
	failure.PermissionDenials = result.PermissionDenials
	failure.PermissionExecuteDenials = result.PermissionExecuteDenials
	failure.ToolReads = result.ToolReads
	failure.ToolEdits = result.ToolEdits
	failure.ToolExecutes = result.ToolExecutes
	failure.ToolOthers = result.ToolOthers
	failure.ToolFailedUpdates = result.ToolFailedUpdates
	if err != nil {
		failure.Reason = "agent-error"
		if errors.Is(err, worker.ErrValidation) {
			failure.Reason = "worker-validation"
		}
		return err
	}
	if result.NoChange {
		failure.Reason = "candidate-no-change"
		return fmt.Errorf("%w: no candidate changes", errExecutionValidation)
	}
	failure.Reason = "artifact-write"
	if err := writeJSON(opts.out, result.Bundle); err != nil {
		return err
	}
	return writeJSON(filepath.Join(filepath.Dir(opts.out), "execution.json"), struct {
		Version                  int    `json:"version"`
		UsedAgent                bool   `json:"used_agent"`
		PromptRequests           int    `json:"prompt_requests"`
		Updates                  int    `json:"updates"`
		PermissionRequests       int    `json:"permission_requests"`
		PermissionDenials        int    `json:"permission_denials"`
		PermissionExecuteDenials int    `json:"permission_execute_denials"`
		ToolReads                int    `json:"tool_reads"`
		ToolEdits                int    `json:"tool_edits"`
		ToolExecutes             int    `json:"tool_executes"`
		ToolOthers               int    `json:"tool_others"`
		ToolFailedUpdates        int    `json:"tool_failed_updates"`
		ModelCalls               *int   `json:"model_calls"`
		CandidateDigest          string `json:"candidate_digest"`
	}{
		Version:                  1,
		UsedAgent:                result.UsedAgent,
		PromptRequests:           result.PromptRequests,
		Updates:                  result.Updates,
		PermissionRequests:       result.PermissionRequests,
		PermissionDenials:        result.PermissionDenials,
		PermissionExecuteDenials: result.PermissionExecuteDenials,
		ToolReads:                result.ToolReads,
		ToolEdits:                result.ToolEdits,
		ToolExecutes:             result.ToolExecutes,
		ToolOthers:               result.ToolOthers,
		ToolFailedUpdates:        result.ToolFailedUpdates,
		ModelCalls:               result.ModelCalls,
		CandidateDigest:          result.Bundle.CandidateDigest,
	})
}
