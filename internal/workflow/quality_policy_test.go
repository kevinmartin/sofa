package workflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	check := func(nextCaller, nextReusable []byte) error {
		return CheckSofaQualityContract(nextCaller, nextReusable, caller, reusable)
	}
	if err := check(caller, reusable); err != nil {
		t.Fatal(err)
	}

	pin := regexp.MustCompile(`actions/checkout@[0-9a-f]{40} # v[0-9]+\.[0-9]+\.[0-9]+`)
	original := pin.Find(reusable)
	if original == nil {
		t.Fatal("checkout pin missing")
	}
	changedPin := []byte(strings.Replace(string(reusable), string(original), "actions/checkout@"+strings.Repeat("f", 40)+" # v999.0.0", 1))
	if err := check(caller, changedPin); err != nil {
		t.Fatalf("an otherwise unchanged pinned action was rejected: %v", err)
	}

	for _, scenario := range []struct {
		name     string
		caller   []byte
		reusable []byte
	}{
		{"caller profile changed", []byte(strings.Replace(string(caller), "profiles: go", "profiles: auto", 1)), reusable},
		{"caller extra job", append(append([]byte(nil), caller...), []byte("  bypass: {}\n")...), reusable},
		{"tests removed", caller, []byte(strings.Replace(string(reusable), "go test -count=1 ./...", "true", 1))},
		{"extra command", caller, []byte(strings.Replace(string(reusable), "go test -count=1 ./...", "echo bypass; go test -count=1 ./...", 1))},
		{"permissions broadened", caller, []byte(strings.Replace(string(reusable), "contents: read", "contents: write", 1))},
		{"different action", caller, []byte(strings.Replace(string(reusable), "actions/checkout", "attacker/checkout", 1))},
		{"unpinned action", caller, []byte(strings.Replace(string(reusable), string(original), "actions/checkout@v999", 1))},
		{"placeholder ref", caller, []byte(strings.Replace(string(reusable), string(original), "actions/checkout@<sha> # <version>", 1))},
		{"extra workflow line", caller, append(append([]byte(nil), reusable...), []byte("\n# extra command\n")...)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			if err := check(scenario.caller, scenario.reusable); err == nil {
				t.Fatal("workflow change beyond action pin accepted")
			}
		})
	}
	if err := CheckSofaQualityContract(caller, reusable, nil, reusable); err == nil {
		t.Fatal("missing trusted caller baseline accepted")
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
	var selectScript, detectScript string
	for _, step := range workflow.Jobs["select"].Steps {
		if step.Name == "Select and validate quality profiles" {
			selectScript = step.Run
		}
	}
	for _, step := range workflow.Jobs["autodetect"].Steps {
		if step.Name == "Detect quality profiles" {
			detectScript = step.Run
		}
	}
	if selectScript == "" || detectScript == "" {
		t.Fatal("profile detection or selector missing")
	}
	for _, scenario := range []struct {
		name, profiles string
		files          map[string]string
		wantSuccess    bool
		wantEnv        string
	}{
		{"Go required", "go", map[string]string{"go.mod": "module example.com/a\n"}, true, "go=true"},
		{"missing Go manifest", "go", nil, false, ""},
		{"React detected", "auto", map[string]string{
			"package.json":      `{"dependencies":{"react":"18.0.0"},"scripts":{"format:check":"true","lint":"true","typecheck":"true","test":"true","build":"true"}}`,
			"package-lock.json": "{}", "tsconfig.json": "{}",
		}, true, "react=true"},
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
			outputFile := filepath.Join(dir, "github-output")
			detected, detectionResult := "", "skipped"
			if scenario.profiles == "auto" {
				cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", detectScript)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GITHUB_OUTPUT="+outputFile)
				output, err := cmd.CombinedOutput()
				if err != nil {
					if scenario.wantSuccess {
						t.Fatalf("detection failed: %s: %v", output, err)
					}
					return
				}
				detectionResult = "success"
				data, err := os.ReadFile(outputFile)
				if err != nil {
					t.Fatal(err)
				}
				detected = strings.TrimPrefix(strings.TrimSpace(string(data)), "profiles=")
			}
			if err := os.WriteFile(outputFile, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-e", "-o", "pipefail", "-c", selectScript)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "SOFA_PROFILES="+scenario.profiles, "DETECTED_PROFILES="+detected,
				"AUTODETECT_RESULT="+detectionResult, "GITHUB_OUTPUT="+outputFile)
			output, err := cmd.CombinedOutput()
			if (err == nil) != scenario.wantSuccess {
				t.Fatalf("selector outcome err=%v output=%s", err, output)
			}
			if scenario.wantSuccess {
				selected, err := os.ReadFile(outputFile)
				if err != nil || !strings.Contains(string(selected), scenario.wantEnv) {
					t.Fatalf("selection output=%s err=%v", selected, err)
				}
			}
		})
	}
}
