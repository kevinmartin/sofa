package main

import (
	"testing"
)

func TestCredentialBearingAndACPCommandsFailClosedWithoutTheirRuntime(t *testing.T) {
	t.Setenv("GITHUB_EVENT_PATH", "")
	t.Setenv("SOFA_GATE_READ_TOKEN", "")
	if err := run([]string{"gate-bridge"}); err == nil {
		t.Fatal("gate bridge ran without a trusted workflow event")
	}
	t.Setenv("GITHUB_TOKEN", "")
	if err := run([]string{"--acp", "--stdio"}); err == nil {
		t.Fatal("fake ACP ran without its inert test token")
	}
}
