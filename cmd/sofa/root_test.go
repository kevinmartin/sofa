package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/admission"
)

func TestCommandHelpDescribesStagesAndFlags(t *testing.T) {
	for _, item := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "root",
			args: []string{"--help"},
			want: []string{"admit", "execute", "verify", "publish", "fail", "spec-digest"},
		},
		{
			name: "admit",
			args: []string{"admit", "--help"},
			want: []string{"--config", "--issue", "--out-dir", "Approved issue number"},
		},
		{
			name: "help subcommand",
			args: []string{"help", "admit"},
			want: []string{"--config", "--issue", "--out-dir", "Approved issue number"},
		},
		{
			name: "execute",
			args: []string{"execute", "--help"},
			want: []string{"--config", "--manifest", "--workspace", "--out"},
		},
		{
			name: "verify",
			args: []string{"verify", "--help"},
			want: []string{"--config", "--manifest", "--workspace", "--bundle", "--out"},
		},
		{
			name: "publish",
			args: []string{"publish", "--help"},
			want: []string{"--config", "--manifest", "--bundle", "--evidence", "--base-branch"},
		},
		{
			name: "fail",
			args: []string{"fail", "--help"},
			want: []string{"--config", "--manifest", "--failure"},
		},
		{
			name: "spec-digest",
			args: []string{"spec-digest", "--help"},
			want: []string{"--title", "--body-file"},
		},
	} {
		t.Run(item.name, func(t *testing.T) {
			var output bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			if err := executeCommand(context.Background(), cmd, item.args); err != nil {
				t.Fatal(err)
			}
			for _, want := range item.want {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("help does not describe %q: %s", want, output.String())
				}
			}
		})
	}
}

func TestCommandArgumentErrorsDoNotEchoInput(t *testing.T) {
	const secret = "inert-sensitive-argument"
	for _, args := range [][]string{
		{},
		{secret},
		{"__complete", "admit", "--issue", secret, ""},
		{"__completeNoDesc", "admit", "--issue", secret, ""},
		{"--help=false", "__complete", "admit", "--issue", secret, ""},
		{"help", secret},
		{"help", "admit", secret},
		{"--" + secret},
		{"admit"},
		{"admit", "--issue", secret},
		{"execute", "--config", secret},
		{"verify", "--" + secret},
		{"publish", secret},
		{"fail", "--failure", secret},
		{"spec-digest", "--title", secret},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			cmd := newRootCommand()
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			err := executeCommand(context.Background(), cmd, args)
			if err == nil {
				t.Fatal("accepted invalid command arguments")
			}
			if strings.Contains(err.Error(), secret) || output.Len() != 0 {
				t.Fatalf("argument error exposed input or printed usage: error=%v, output=%q", err, output.String())
			}
		})
	}
}

func TestStageFlagsReachConfigurationValidation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yml")
	for _, args := range [][]string{
		{"admit", "--config", missing, "--issue", "1", "--out-dir", "out"},
		{"execute", "--config", missing, "--manifest", "manifest", "--workspace", "workspace", "--out", "out"},
		{"verify", "--config", missing, "--manifest", "manifest", "--workspace", "workspace", "--bundle", "bundle", "--out", "out"},
		{"publish", "--config", missing, "--manifest", "manifest", "--bundle", "bundle", "--evidence", "evidence", "--base-branch", "main"},
		{"fail", "--config", missing, "--manifest", "manifest", "--failure", "failure"},
	} {
		t.Run(args[0], func(t *testing.T) {
			if err := run(context.Background(), args); err == nil || err.Error() != "cannot read configuration" {
				t.Fatalf("stage did not parse its existing flags: %v", err)
			}
		})
	}
}

func TestSpecDigestPreservesFlagSpellingsAndValues(t *testing.T) {
	body := "Change the fixture"
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// A flag-shaped title must stay a value even while converting legacy flags.
	title := "-body-file"
	_, digest, err := admission.CanonicalSpec(title, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"spec-digest", "--title", title, "--body-file", bodyFile},
		{"spec-digest", "--title=" + title, "--body-file=" + bodyFile},
		{"spec-digest", "-title", title, "-body-file", bodyFile},
		{"spec-digest", "-title=" + title, "-body-file=" + bodyFile},
	} {
		var output bytes.Buffer
		cmd := newRootCommand()
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		if err := executeCommand(context.Background(), cmd, args); err != nil {
			t.Fatal(err)
		}
		if output.String() != digest+"\n" {
			t.Fatalf("unexpected digest output: %q", output.String())
		}
	}
}
