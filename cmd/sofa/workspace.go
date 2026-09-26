package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/kevinmartin/sofa/internal/integrity"
)

func cleanBase(ctx context.Context, workspace, expected string) error {
	if !filepath.IsAbs(workspace) {
		return errors.New("workspace must be absolute")
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "rev-parse", "HEAD")
	cmd.Dir = workspace
	cmd.Env = gitEnv()
	b, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(b)) != expected {
		return errors.New("workspace is not at admitted base")
	}
	cmd = exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = workspace
	cmd.Env = gitEnv()
	b, err = cmd.Output()
	if err != nil || len(b) != 0 {
		return errors.New("workspace must be clean before candidate work")
	}
	return nil
}

type candidateWorkspaceState struct {
	status []byte
	files  map[string]integrity.BaseFile
}

func candidateState(ctx context.Context, workspace string, b integrity.Bundle) (candidateWorkspaceState, error) {
	paths := make([]string, len(b.Files))
	for i, change := range b.Files {
		paths[i] = change.Path
	}
	files, err := integrity.Snapshot(workspace, paths)
	if err != nil {
		return candidateWorkspaceState{}, err
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = workspace
	cmd.Env = gitEnv()
	status, err := cmd.Output()
	if err != nil {
		return candidateWorkspaceState{}, errors.New("cannot inspect candidate workspace")
	}
	return candidateWorkspaceState{
		status: status,
		files:  files,
	}, nil
}

func (s candidateWorkspaceState) matches(other candidateWorkspaceState) bool {
	return bytes.Equal(s.status, other.status) && reflect.DeepEqual(s.files, other.files)
}

func gitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
}
