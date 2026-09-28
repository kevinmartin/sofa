//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCheckTimeoutStopsDescendant(t *testing.T) {
	workspace := t.TempDir()
	checkHome := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := runCheckProcess(ctx, workspace, checkHome, []string{
		"sh", "-c", "sh -c 'sleep 1; echo leaked > \"$HOME/marker\"' & echo \"$!\" > \"$HOME/child-pid\"; wait",
	})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("timed-out check did not finish promptly: %v", err)
	}
	assertDescendantStopped(t, checkHome)
}

func TestSuccessfulCheckStopsBackgroundDescendant(t *testing.T) {
	workspace := t.TempDir()
	checkHome := t.TempDir()
	started := time.Now()
	err := runCheckProcess(t.Context(), workspace, checkHome, []string{
		"sh", "-c", "sh -c 'sleep 1; echo leaked > \"$HOME/marker\"' & echo \"$!\" > \"$HOME/child-pid\"; exit 0",
	})
	if err != nil || time.Since(started) > 3*time.Second {
		t.Fatalf("successful check waited for background child: %v", err)
	}
	assertDescendantStopped(t, checkHome)
}

func assertDescendantStopped(t *testing.T, checkHome string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(checkHome, "child-pid"))
	if err != nil {
		t.Fatalf("check never started a descendant: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid descendant PID %q: %v", data, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !checkProcessStopped(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !checkProcessStopped(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("check descendant %d survived", pid)
	}
	if _, err := os.Stat(filepath.Join(checkHome, "marker")); !os.IsNotExist(err) {
		t.Fatalf("descendant wrote marker before stopping: %v", err)
	}
}

func checkProcessStopped(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	// Linux containers may leave orphaned descendants as unreaped zombies.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && strings.Contains(string(data), ") Z ")
}
