package main

import (
	"errors"

	"github.com/kevinmartin/sofa/internal/agent"
	"github.com/kevinmartin/sofa/internal/worker"
)

type ExecutionFailure struct {
	Version                  int    `json:"version" validate:"oneof=1 2"`
	AttemptID                string `json:"attempt_id" validate:"required"`
	Generation               int64  `json:"generation" validate:"gt=0"`
	Kind                     string `json:"kind" validate:"oneof=authentication quota infrastructure validation"`
	Reason                   string `json:"reason,omitempty" validate:"omitempty,oneof=recovery-input base-checkout candidate-no-change worker-validation configured-check workspace-changed agent-error artifact-write unexpected"`
	UsedAgent                bool   `json:"used_agent,omitempty"`
	PromptRequests           int    `json:"prompt_requests,omitempty" validate:"gte=0,lte=1"`
	Updates                  int    `json:"updates,omitempty" validate:"gte=0"`
	PermissionRequests       int    `json:"permission_requests,omitempty" validate:"gte=0"`
	PermissionDenials        int    `json:"permission_denials,omitempty" validate:"gte=0,ltefield=PermissionRequests"`
	PermissionExecuteDenials int    `json:"permission_execute_denials,omitempty" validate:"gte=0,ltefield=PermissionDenials"`
	ToolReads                int    `json:"tool_reads,omitempty" validate:"gte=0,ltefield=Updates"`
	ToolEdits                int    `json:"tool_edits,omitempty" validate:"gte=0,ltefield=Updates"`
	ToolExecutes             int    `json:"tool_executes,omitempty" validate:"gte=0,ltefield=Updates"`
	ToolOthers               int    `json:"tool_others,omitempty" validate:"gte=0,ltefield=Updates"`
	ToolFailedUpdates        int    `json:"tool_failed_updates,omitempty" validate:"gte=0,ltefield=Updates"`
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
	if err := artifactValidator.Struct(f); err != nil {
		return errors.New("invalid execution failure fields")
	}
	// Version one is the workflow's bounded infrastructure fallback when the
	// execution job could not upload an artifact. It cannot assert telemetry.
	if f.Version == 1 && f.Kind == "infrastructure" && f.Reason == "" && !f.hasACPActivity() {
		return nil
	}
	if f.Version != 2 || (!f.UsedAgent && f.PromptRequests != 0) {
		return errors.New("invalid execution failure telemetry")
	}
	if f.Updates > maxACPObservationCount || f.PermissionRequests > maxACPObservationCount || int64(f.ToolReads)+int64(f.ToolEdits)+int64(f.ToolExecutes)+int64(f.ToolOthers) > int64(f.Updates) || (!f.UsedAgent && f.hasACPActivity()) {
		return errors.New("invalid ACP observation telemetry")
	}
	if f.Reason == "" {
		return errors.New("unsupported execution failure reason")
	}
	if (f.Reason == "recovery-input" || f.Reason == "base-checkout") && f.hasACPActivity() {
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

func (f ExecutionFailure) hasACPActivity() bool {
	if f.UsedAgent {
		return true
	}
	for _, count := range []int{
		f.PromptRequests,
		f.Updates,
		f.PermissionRequests,
		f.PermissionDenials,
		f.PermissionExecuteDenials,
		f.ToolReads,
		f.ToolEdits,
		f.ToolExecutes,
		f.ToolOthers,
		f.ToolFailedUpdates,
	} {
		if count != 0 {
			return true
		}
	}
	return false
}
