package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	release "github.com/kevinmartin/sofa/internal/distribution"
)

func TestReleaseArgumentErrorsRemainBounded(t *testing.T) {
	const secret = "inert-release-sensitive-value"
	for _, args := range [][]string{{"release", secret}, {"release", "plan", secret}, {"release", "build", secret}, {"release", "publish", secret}, {"release", "canary", secret}, {"release", "promote", secret}, {"release", "rollback-plan", secret}, {"release", "plan", "--" + secret}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			err := executeCommand(context.Background(), cmd, args)
			if err == nil || strings.Contains(err.Error(), secret) || output.Len() != 0 {
				t.Fatalf("release parser leaked invalid input: %v output=%q", err, output.String())
			}
		})
	}
}

func TestReleaseJSONRejectsUnknownFieldsTrailingDataAndOversizedFiles(t *testing.T) {
	for name, data := range map[string]string{"unknown": `{"series":"0.1","secret":"inert"}`, "trailing": `{"series":"0.1"} {"series":"0.2"}`, "large": strings.Repeat("x", (64<<10)+1)} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			var config release.Config
			if err := readReleaseJSON(path, &config); err == nil {
				t.Fatal("malformed release configuration accepted")
			}
		})
	}
}

func TestReleaseV1AllocationRequiresSeparateOwnerActivation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"series":"1.0"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFA_V1_ENABLED", "")
	t.Setenv("SOFA_RELEASE_TOKEN", "")
	err := run(context.Background(), []string{"release", "plan", "--config", path, "--source", strings.Repeat("a", 40)})
	if err == nil || err.Error() != "stable v1 release activation requires Kevin's approval" {
		t.Fatalf("stable v1 did not stop before credentialed API: %v", err)
	}
}
