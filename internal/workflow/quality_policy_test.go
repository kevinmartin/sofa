package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestSofaQualityContract(t *testing.T) {
	caller, err := os.ReadFile("../../.github/workflows/pr-fast.yml")
	if err != nil {
		t.Fatal(err)
	}
	reusable, err := os.ReadFile("../../.github/workflows/quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckSofaQualityContract(caller, reusable); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, before, after string }{
		{"removed tests", "go test -count=1 ./...", "true"},
		{"removed analyzer", "staticcheck ./...", "true"},
		{"removed node checks", "for script in format:check lint typecheck test; do", "for script in lint; do"},
		{"weakened permissions", "contents: read", "contents: write"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := strings.Replace(string(reusable), test.before, test.after, 1)
			if changed == string(reusable) || CheckSofaQualityContract(caller, []byte(changed)) == nil {
				t.Fatal("weakened workflow passed trusted contract")
			}
		})
	}
	changed := strings.Replace(string(caller), "profiles: go", "profiles: auto", 1)
	if CheckSofaQualityContract([]byte(changed), reusable) == nil {
		t.Fatal("sofa Go profile could be silently skipped")
	}
	changed = strings.Replace(string(caller), "  deterministic:\n", "  deterministic:\n    if: false\n", 1)
	if CheckSofaQualityContract([]byte(changed), reusable) == nil {
		t.Fatal("skipped required job passed")
	}
}

func TestQualityProfileSelection(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	var selectScript string
	for _, step := range workflow.Jobs["checks"].Steps {
		if step.Name == "Select and validate quality profiles" {
			selectScript = step.Run
		}
	}
	if selectScript == "" {
		t.Fatal("profile selector missing")
	}
	for _, scenario := range []struct {
		name, profiles string
		files          map[string]string
		wantSuccess    bool
		wantEnv        string
	}{
		{"Go required", "go", map[string]string{"go.mod": "module example.com/a\n"}, true, "SOFA_GO=true"},
		{"missing Go manifest", "go", nil, false, ""},
		{"React detected", "auto", map[string]string{
			"package.json":      `{"dependencies":{"react":"18.0.0"},"scripts":{"format:check":"true","lint":"true","typecheck":"true","test":"true","build":"true"}}`,
			"package-lock.json": "{}", "tsconfig.json": "{}",
		}, true, "SOFA_REACT=true"},
		{"React without build", "react", map[string]string{
			"package.json":      `{"dependencies":{"react":"18.0.0"},"scripts":{"format:check":"true","lint":"true","typecheck":"true","test":"true"}}`,
			"package-lock.json": "{}", "tsconfig.json": "{}",
		}, false, ""},
		{"ambiguous lock", "typescript", map[string]string{
			"package.json":      `{"scripts":{"format:check":"true","lint":"true","typecheck":"true","test":"true"}}`,
			"package-lock.json": "{}", "yarn.lock": "", "tsconfig.json": "{}",
		}, false, ""},
		{"unsupported repo", "auto", nil, false, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range scenario.files {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			envFile := filepath.Join(dir, "github-env")
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", selectScript)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "SOFA_PROFILES="+scenario.profiles, "GITHUB_ENV="+envFile)
			output, err := cmd.CombinedOutput()
			if (err == nil) != scenario.wantSuccess {
				t.Fatalf("selector outcome err=%v output=%s", err, output)
			}
			if scenario.wantSuccess {
				env, err := os.ReadFile(envFile)
				if err != nil || !strings.Contains(string(env), scenario.wantEnv) {
					t.Fatalf("selection env=%s err=%v", env, err)
				}
			}
		})
	}
}
