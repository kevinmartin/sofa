package managedconfig

import (
	"bytes"
	"os"
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
		if action, err := Difference(got, want); err != nil || action != "current" {
			t.Fatalf("current %s: %s %v", path, action, err)
		}
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
	if _, err := Difference([]byte("version: 2\n"), []byte(Marker+"version: 2\n")); err == nil {
		t.Fatal("unmanaged file was overwritten")
	}
	if action, err := Difference(nil, []byte(Marker)); err != nil || action != "create" {
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
	caller := string(files[".github/workflows/sofa-quality.yml"])
	if !strings.Contains(caller, "quality.reusable.yml@v1") || !strings.Contains(caller, "profiles: typescript,react") || strings.Contains(caller, "secrets:") {
		t.Fatal("consumer caller does not use the shared secretless quality channel")
	}
	dependabot := string(files[".github/dependabot.yml"])
	if !strings.Contains(dependabot, "package-ecosystem: npm") || !strings.Contains(dependabot, "package-ecosystem: github-actions") {
		t.Fatal("consumer Dependabot ecosystems incorrect")
	}
}
