package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/version"
)

func TestVersionReportsEmbeddedSourceIdentity(t *testing.T) {
	previousVersion, previousCommit := version.Version, version.SourceCommit
	version.Version = "v0.1.3"
	version.SourceCommit = strings.Repeat("b", 40)
	t.Cleanup(func() {
		version.Version = previousVersion
		version.SourceCommit = previousCommit
	})

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"--version"}, want: "sofa-test v0.1.3 (" + strings.Repeat("b", 40) + ")\n"},
		{args: []string{"version"}},
	} {
		var output bytes.Buffer
		cmd := newRootCommand()
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("version %v: %v", tc.args, err)
		}
		if tc.want != "" {
			if output.String() != tc.want {
				t.Fatalf("version %v = %q, want %q", tc.args, output.String(), tc.want)
			}
			continue
		}
		var got map[string]string
		if err := json.Unmarshal(output.Bytes(), &got); err != nil {
			t.Fatalf("version output is not JSON: %v", err)
		}
		if len(got) != 2 || got["version"] != "v0.1.3" || got["source_commit"] != strings.Repeat("b", 40) {
			t.Fatalf("wrong machine-readable version: %v", got)
		}
	}
}
