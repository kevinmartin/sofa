package managedconfig

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSofaOwnConfigurationIsRendered(t *testing.T) {
	spec, err := Load("../../managed-repos", "kevinmartin/sofa")
	if err != nil {
		t.Fatal(err)
	}
	files, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range files {
		got, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("sofa is not using generated %s: %v", path, err)
		}
		if action, err := Difference(got, true, want); err != nil || action != "current" {
			t.Fatalf("current %s: %s %v", path, action, err)
		}
	}
}

func TestRenderedYAMLRejectsDuplicateKeysAndMissingComments(t *testing.T) {
	var doc map[string]any
	if err := decodeRendered([]byte(Marker+"version: 2\nversion: 3\n"), &doc); err == nil {
		t.Fatal("duplicate rendered YAML key accepted")
	}
	if err := decodeRendered([]byte("version: 2\n"), &doc); err == nil {
		t.Fatal("unmarked generated configuration accepted")
	}
}

func TestActionlintRenderedCallers(t *testing.T) {
	if _, err := exec.LookPath("actionlint"); err != nil {
		t.Skip("actionlint executable unavailable; hosted Actions job runs this test")
	}
	spec, err := Load("../../managed-repos", "kevinmartin/sofa")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"self", "consumer"} {
		t.Run(mode, func(t *testing.T) {
			candidate := spec
			if mode == "consumer" {
				candidate.Mode = "consumer"
				candidate.Repository = "kevinmartin/example"
				candidate.Profiles = []string{"typescript", "react"}
				candidate.Dependabot.Ecosystems = []string{"npm", "github-actions"}
			}
			files, err := Render(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateActionlint(t.Context(), files); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestManifestIdentityAndOwnership(t *testing.T) {
	spec, err := Load("../../managed-repos", "kevinmartin/sofa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load("../../managed-repos", "../sofa"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := Load("../../managed-repos", "kevinmartin/unknown"); err == nil {
		t.Fatal("unlisted repository accepted")
	}
	spec.Repository = "someone/sofa"
	if err := spec.Validate(); err == nil {
		t.Fatal("self mode outside sofa accepted")
	}
	if _, err := Difference([]byte("version: 2\n"), true, []byte(Marker+"version: 2\n")); err == nil {
		t.Fatal("unmanaged file was overwritten")
	}
	if _, err := Difference(nil, true, []byte(Marker)); err == nil {
		t.Fatal("existing empty file was overwritten")
	}
	if action, err := Difference(nil, false, []byte(Marker)); err != nil || action != "create" {
		t.Fatalf("new managed file: %s %v", action, err)
	}
}

func TestConsumerRenderingKeepsPolicyInSofa(t *testing.T) {
	spec, err := Load("../../managed-repos", "kevinmartin/sofa")
	if err != nil {
		t.Fatal(err)
	}
	spec.Repository = "kevinmartin/example"
	spec.Mode = "consumer"
	spec.Profiles = []string{"typescript", "react"}
	spec.Dependabot.Ecosystems = []string{"npm", "github-actions"}
	files, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	caller := string(files[".github/workflows/sofa.quality.yml"])
	if !strings.Contains(caller, "quality.reusable.yml@v1") || !strings.Contains(caller, "profiles: typescript,react") || strings.Contains(caller, "secrets:") {
		t.Fatal("consumer caller does not use the shared secretless quality channel")
	}
	dependabot := string(files[".github/dependabot.yml"])
	if !strings.Contains(dependabot, "package-ecosystem: npm") || !strings.Contains(dependabot, "package-ecosystem: github-actions") {
		t.Fatal("consumer Dependabot ecosystems incorrect")
	}
}
