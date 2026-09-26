package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRootCommandHelpDoesNotRequireCredential(t *testing.T) {
	t.Setenv("SOFA_MODEL_TOKEN", "")
	cmd := newRootCommand(func(config) result {
		t.Fatal("help must not start a preflight probe")
		return result{}
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "acp-preflight [flags]") || stderr.Len() != 0 {
		t.Fatalf("unexpected help output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandRequiresCredential(t *testing.T) {
	t.Setenv("SOFA_MODEL_TOKEN", "")
	cmd := newRootCommand(func(config) result {
		t.Fatal("missing credentials must not start a preflight probe")
		return result{}
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); !errors.Is(err, errPreflightFailed) {
		t.Fatalf("got error %v, want failed preflight", err)
	}
	if stdout.String() != "sofa ACP preflight: missing model credential\n" || stderr.Len() != 0 {
		t.Fatalf("unexpected credential output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandUsesEnvironment(t *testing.T) {
	for _, workspace := range []string{"", "/custom-workspace"} {
		t.Run("workspace="+workspace, func(t *testing.T) {
			t.Setenv("SOFA_MODEL_TOKEN", "test-secret-cli")
			t.Setenv("SOFA_WORKSPACE", workspace)
			t.Setenv("SOFA_COPILOT_ENTRY", "")
			t.Setenv("SOFA_COPILOT_PATH", "/custom/copilot")
			var received config
			calls := 0
			r := result{
				phase:  "session/new",
				detail: "accepted without a model prompt",
				ok:     true,
			}
			cmd := newRootCommand(func(c config) result {
				received = c
				calls++
				return r
			})
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs([]string{})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			wantWorkspace := workspace
			if wantWorkspace == "" {
				wantWorkspace = "/workspace"
			}
			if calls != 1 || received.workspace != wantWorkspace || received.token != "test-secret-cli" || received.timeout != 30*time.Second {
				t.Fatalf("incorrect probe configuration: calls=%d config=%+v", calls, received)
			}
			if received.command != "/custom/copilot" || strings.Join(received.args, " ") != "--acp --stdio" {
				t.Fatalf("incorrect Copilot command: %q %q", received.command, received.args)
			}
			if stdout.String() != r.String()+"\n" || stderr.Len() != 0 {
				t.Fatalf("unexpected probe output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRootCommandReportsProbeFailure(t *testing.T) {
	t.Setenv("SOFA_MODEL_TOKEN", "test-secret-cli")
	r := result{
		phase:   "initialize",
		detail:  "RPC code -32000",
		stderr:  42,
		classes: []string{"module unavailable"},
	}
	cmd := newRootCommand(func(config) result {
		return r
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); !errors.Is(err, errPreflightFailed) {
		t.Fatalf("got error %v, want failed preflight", err)
	}
	if stdout.String() != r.String()+"\n" || stderr.Len() != 0 {
		t.Fatalf("unexpected failure output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRootCommandRejectsUnexpectedArguments(t *testing.T) {
	for _, argument := range []string{"unexpected", "--unexpected"} {
		t.Run(argument, func(t *testing.T) {
			cmd := newRootCommand(func(config) result {
				t.Fatal("invalid arguments must not start a preflight probe")
				return result{}
			})
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{argument})
			if err := cmd.Execute(); err == nil || errors.Is(err, errPreflightFailed) {
				t.Fatalf("got error %v, want an argument error", err)
			}
			if output.Len() != 0 {
				t.Fatalf("argument failure unexpectedly printed diagnostics: %q", output.String())
			}
		})
	}
}

func TestPreflightCommandDoesNotEchoArguments(t *testing.T) {
	const secret = "inert-sensitive-argument"
	for _, args := range [][]string{
		{secret},
		{"--unknown=" + secret},
		{"__complete", "--help=" + secret, ""},
		{"__completeNoDesc", "--help=" + secret, ""},
		{"--help=false", "__complete", "--help=" + secret, ""},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newRootCommand(func(config) result {
				t.Fatal("invalid arguments must not start a preflight probe")
				return result{}
			})
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			err := executePreflightCommand(cmd, args)
			if err == nil || strings.Contains(err.Error(), secret) || output.Len() != 0 {
				t.Fatalf("argument diagnostic exposed input: err=%v output=%q", err, output.String())
			}
		})
	}
}
