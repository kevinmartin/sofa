//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// runCheckProcess bounds the entire check process group. A check may spawn a
// test binary or shell child; neither may outlive the check or hold up Wait.
func runCheckProcess(ctx context.Context, workspace, checkHome string, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workspace
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + checkHome,
		"GOCACHE=" + filepath.Join(checkHome, "cache"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOENV=off",
	}
	// Nil output streams attach /dev/null directly, without copy goroutines
	// whose pipes a surviving descendant could keep open.
	cmd.Stdout, cmd.Stderr = nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		return killCheckGroup(cmd)
	}
	err := cmd.Run()
	_ = killCheckGroup(cmd) // Also stop background descendants after a successful check.
	return err
}

func killCheckGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
