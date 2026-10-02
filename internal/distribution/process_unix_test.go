//go:build linux || darwin

package distribution

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

func TestBuildCancellationStopsToolDescendants(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child-pid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := buildCommand(ctx, "sh", "-c", `sleep 1000 & echo "$!" > "$SOFA_CHILD_PID"; wait`)
	command.Env = append(os.Environ(), "SOFA_CHILD_PID="+pidPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer command.Cancel()
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var child int
	for child == 0 {
		data, err := os.ReadFile(pidPath)
		if err == nil {
			child, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || child < 1 {
				t.Fatalf("invalid child identity: %v", err)
			}
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("build tool descendant did not start")
		case err := <-wait:
			t.Fatalf("tool exited before readiness: %v", err)
		}
	}
	cancel()
	select {
	case err := <-wait:
		if err == nil {
			t.Fatal("cancelled build tool succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled build did not finish promptly")
	}
	deadline.Reset(3 * time.Second)
	for {
		if errors.Is(syscall.Kill(child, 0), syscall.ESRCH) {
			return
		}
		// Linux containers can leave stopped orphan children as unreaped zombies.
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		if err == nil && strings.Contains(string(data), ") Z ") {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			_ = syscall.Kill(child, syscall.SIGKILL)
			t.Fatal("descendant survived build cancellation")
		}
	}
}
