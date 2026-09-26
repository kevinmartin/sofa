package main

import (
	"errors"

	"github.com/kevinmartin/sofa/internal/agent"
	"github.com/kevinmartin/sofa/internal/worker"
)

type ExecutionFailure struct {
	Version                  int    `json:"version"`
	AttemptID                string `json:"attempt_id"`
	Generation               int64  `json:"generation"`
	Kind                     string `json:"kind"`
	Reason                   string `json:"reason,omitempty"`
	UsedAgent                bool   `json:"used_agent,omitempty"`
	PromptRequests           int    `json:"prompt_requests,omitempty"`
	Updates                  int    `json:"updates,omitempty"`
	PermissionRequests       int    `json:"permission_requests,omitempty"`
	PermissionDenials        int    `json:"permission_denials,omitempty"`
	PermissionExecuteDenials int    `json:"permission_execute_denials,omitempty"`
	ToolReads                int    `json:"tool_reads,omitempty"`
	ToolEdits                int    `json:"tool_edits,omitempty"`
	ToolExecutes             int    `json:"tool_executes,omitempty"`
	ToolOthers               int    `json:"tool_others,omitempty"`
	ToolFailedUpdates        int    `json:"tool_failed_updates,omitempty"`
}

const maxACPObservationCount = agent.MaxObservationCount

var errExecutionValidation = errors.New("execution input or candidate validation failed")

func executionFailureKind(err error) string {
	switch {
	case errors.Is(err, agent.ErrAuthentication):
		return "authentication"
	case errors.Is(err, agent.ErrQuota):
		return "quota"
	case errors.Is(err, agent.ErrPermission):
		return "validation"
	case errors.Is(err, worker.ErrValidation), errors.Is(err, errExecutionValidation):
		return "validation"
	default:
		return "infrastructure"
	}
}

func validateExecutionFailure(f ExecutionFailure, m Manifest) error {
	if f.AttemptID != m.Fence.AttemptID || f.Generation != m.Fence.Generation {
		return errors.New("failure artifact identity mismatch")
	}
	switch f.Kind {
	case "authentication", "quota", "infrastructure", "validation":
	default:
		return errors.New("unsupported failure class")
	}
	// Version one is the workflow's bounded infrastructure fallback when the
	// execution job could not upload an artifact. It cannot assert telemetry.
	if f.Version == 1 && f.Kind == "infrastructure" && f.Reason == "" && !f.UsedAgent && f.PromptRequests == 0 && f.Updates == 0 && f.PermissionRequests == 0 && f.PermissionDenials == 0 && f.PermissionExecuteDenials == 0 && f.ToolReads == 0 && f.ToolEdits == 0 && f.ToolExecutes == 0 && f.ToolOthers == 0 && f.ToolFailedUpdates == 0 {
		return nil
	}
	if f.Version != 2 || f.PromptRequests < 0 || f.PromptRequests > 1 || (!f.UsedAgent && f.PromptRequests != 0) {
		return errors.New("invalid execution failure telemetry")
	}
	if f.Updates < 0 || f.Updates > maxACPObservationCount || f.PermissionRequests < 0 || f.PermissionRequests > maxACPObservationCount || f.PermissionDenials < 0 || f.PermissionDenials > f.PermissionRequests || f.PermissionExecuteDenials < 0 || f.PermissionExecuteDenials > f.PermissionDenials || f.ToolReads < 0 || f.ToolEdits < 0 || f.ToolExecutes < 0 || f.ToolOthers < 0 || f.ToolFailedUpdates < 0 || f.ToolReads > f.Updates || f.ToolEdits > f.Updates || f.ToolExecutes > f.Updates || f.ToolOthers > f.Updates || f.ToolFailedUpdates > f.Updates || int64(f.ToolReads)+int64(f.ToolEdits)+int64(f.ToolExecutes)+int64(f.ToolOthers) > int64(f.Updates) || (!f.UsedAgent && (f.Updates != 0 || f.PermissionRequests != 0 || f.PermissionDenials != 0 || f.PermissionExecuteDenials != 0 || f.ToolReads != 0 || f.ToolEdits != 0 || f.ToolExecutes != 0 || f.ToolOthers != 0 || f.ToolFailedUpdates != 0)) {
		return errors.New("invalid ACP observation telemetry")
	}
	switch f.Reason {
	case "recovery-input", "base-checkout", "candidate-no-change", "worker-validation", "configured-check", "workspace-changed", "agent-error", "artifact-write", "unexpected":
	default:
		return errors.New("unsupported execution failure reason")
	}
	if (f.Reason == "recovery-input" || f.Reason == "base-checkout") && (f.UsedAgent || f.PromptRequests != 0 || f.Updates != 0 || f.PermissionRequests != 0 || f.PermissionDenials != 0 || f.PermissionExecuteDenials != 0 || f.ToolReads != 0 || f.ToolEdits != 0 || f.ToolExecutes != 0 || f.ToolOthers != 0 || f.ToolFailedUpdates != 0) {
		return errors.New("invalid pre-execution failure telemetry")
	}
	switch f.Reason {
	case "recovery-input", "base-checkout", "candidate-no-change", "worker-validation", "configured-check", "workspace-changed":
		if f.Kind != "validation" {
			return errors.New("execution failure reason and class mismatch")
		}
	case "artifact-write", "unexpected":
		if f.Kind != "infrastructure" {
			return errors.New("execution failure reason and class mismatch")
		}
	}
	return nil
}
