package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/agent"
)

func buildPeer(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-acp")
	command := exec.Command("go", "build", "-ldflags=-X main.mode="+mode, "-o", path, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake peer: %v: %s", err, output)
	}
	return path
}

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runPeer(t *testing.T, executable, dir string, timeout time.Duration) (agent.Result, error) {
	t.Helper()
	return agent.Run(context.Background(), agent.Config{
		Command:      executable,
		Args:         []string{"--acp", "--stdio"},
		Dir:          dir,
		Env:          []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "GITHUB_TOKEN=sofa-fake-acp-inert-token"},
		AllowedPaths: []string{"fixture/greeting.go"},
		Timeout:      timeout,
	}, "format the approved fixture")
}

func TestFakePeerEditsThroughACPFileCapability(t *testing.T) {
	path := buildPeer(t, "edit")
	dir := fixture(t)
	name := filepath.Join(dir, "fixture", "greeting.go")
	const before = "package fixture\nfunc Greeting() string {\n    return \"hello\"\n}\n"
	if err := os.WriteFile(name, []byte(before), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := runPeer(t, path, dir, 5*time.Second)
	if err != nil || result.PromptRequests != 1 || result.StopReason != "end_turn" || result.ModelCalls != nil || result.ToolExecutes != 0 {
		t.Fatalf("result %+v, error %v", result, err)
	}
	after, err := os.ReadFile(name)
	if err != nil || string(after) == before || !strings.Contains(string(after), "\treturn") {
		t.Fatalf("fake edit missing: %q, %v", after, err)
	}
}

func TestFakePeerFixedFailures(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		want  error
		stop  string
		turns int
	}{
		{"no-change", nil, "end_turn", 1},
		{"refusal", nil, "refusal", 1},
		{"auth", agent.ErrAuthentication, "", 0},
		{"quota", agent.ErrQuota, "", 1},
		{"protocol", agent.ErrProtocol, "", 0},
		{"timeout", context.DeadlineExceeded, "", 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			path := buildPeer(t, tc.mode)
			dir := fixture(t)
			if err := os.WriteFile(filepath.Join(dir, "fixture", "greeting.go"), []byte("package fixture\n"), 0600); err != nil {
				t.Fatal(err)
			}
			timeout := 5 * time.Second
			if tc.mode == "timeout" {
				timeout = time.Second
			}
			result, err := runPeer(t, path, dir, timeout)
			if !errors.Is(err, tc.want) || result.StopReason != tc.stop || result.PromptRequests != tc.turns {
				t.Fatalf("result %+v, error %v", result, err)
			}
		})
	}
}
