//go:build linux || darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckTimeoutStopsDescendant(t *testing.T) {
	workspace := t.TempDir()
	checkHome := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := runCheckProcess(ctx, workspace, checkHome, []string{
		"sh", "-c", "sh -c 'sleep 1; echo leaked > \"$HOME/marker\"' & echo ready > \"$HOME/ready\"; wait",
	})
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("timed-out check did not finish promptly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkHome, "ready")); err != nil {
		t.Fatalf("check never started a descendant: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(checkHome, "marker")); !os.IsNotExist(err) {
		t.Fatalf("descendant outlived timed-out check: %v", err)
	}
}

func TestSuccessfulCheckStopsBackgroundDescendant(t *testing.T) {
	workspace := t.TempDir()
	checkHome := t.TempDir()
	started := time.Now()
	err := runCheckProcess(context.Background(), workspace, checkHome, []string{
		"sh", "-c", "sh -c 'sleep 1; echo leaked > \"$HOME/marker\"' & echo ready > \"$HOME/ready\"; exit 0",
	})
	if err != nil || time.Since(started) > 3*time.Second {
		t.Fatalf("successful check waited for background child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkHome, "ready")); err != nil {
		t.Fatalf("check never started a descendant: %v", err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(checkHome, "marker")); !os.IsNotExist(err) {
		t.Fatalf("descendant outlived successful check: %v", err)
	}
}
