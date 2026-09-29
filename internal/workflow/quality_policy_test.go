package workflow

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestSofaQualityContract(t *testing.T) {
	caller, err := os.ReadFile("testdata/approved-pr-fast.yml")
	if err != nil {
		t.Fatal(err)
	}
	reusable, err := os.ReadFile("testdata/approved-quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckSofaQualityContract(caller, reusable); err != nil {
		t.Fatal(err)
	}
	// The exact next generated caller is preapproved before the branch rule is
	// migrated. Its only job must be the shared quality workflow.
	oneJob := string(caller)
	if strings.Contains(oneJob, "  deterministic:\n") {
		var markerFound bool
		oneJob, markerFound = strings.CutSuffix(oneJob, "  # Temporary compatibility check for the existing branch-protection rule.\n"+
			"  deterministic:\n    needs: quality\n    if: always()\n    runs-on: ubuntu-24.04\n    permissions:\n      contents: read\n    steps:\n      - name: Require shared quality gate\n        env:\n          SOFA_QUALITY_RESULT: ${{ needs.quality.result }}\n        run: test \"$SOFA_QUALITY_RESULT\" = success\n")
		if !markerFound {
			t.Fatal("legacy caller does not match the expected transition")
		}
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(oneJob))) != "e1cc51e38b5bb08fd5c1423c1df9af18b823123ef63cc305e103e16a2ed35f83" {
		t.Fatal("preapproved one-job caller no longer matches the generated transition")
	}
	if err := CheckSofaQualityContract([]byte(oneJob), reusable); err != nil {
		t.Fatalf("one-job caller rejected: %v", err)
	}
	if err := CheckSofaQualityContract([]byte(oneJob+"  bypass: {}\n"), reusable); err == nil {
		t.Fatal("unapproved extra job passed")
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
	if strings.Contains(string(caller), "  deterministic:\n") {
		changed = strings.Replace(string(caller), "  deterministic:\n", "  deterministic:\n    if: false\n", 1)
		if CheckSofaQualityContract([]byte(changed), reusable) == nil {
			t.Fatal("skipped required job passed")
		}
	}
	for _, test := range []struct {
		name, before, after, want string
		onCaller                  bool
	}{
		{"job continues after failure", "  quality:\n", "  quality:\n    continue-on-error: true\n", "quality job cannot continue", true},
		{"step continues after failure", "      - name: Test Go packages\n", "      - name: Test Go packages\n        continue-on-error: true\n", "quality step cannot continue", false},
		{"tests bypassed before command", "        run: go test -count=1 ./...", "        run: |\n          exit 0\n          go test -count=1 ./...", "go validators must be separate", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateCaller, candidateReusable := caller, reusable
			if test.onCaller {
				candidateCaller = []byte(strings.Replace(string(caller), test.before, test.after, 1))
			} else {
				candidateReusable = []byte(strings.Replace(string(reusable), test.before, test.after, 1))
			}
			err := CheckSofaQualityContract(candidateCaller, candidateReusable)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
	if CheckSofaQualityContract(caller, append(append([]byte(nil), reusable...), []byte("\n# unapproved gate edit\n")...)) == nil {
		t.Fatal("unapproved quality workflow revision passed trusted digest")
	}
}

func TestCurrentSofaQualityWorkflows(t *testing.T) {
	caller, err := os.ReadFile("../../.github/workflows/pr-fast.yml")
	if err != nil {
		t.Fatal(err)
	}
	reusable, err := os.ReadFile("../../.github/workflows/quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := os.ReadFile("testdata/approved-quality.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	// The trusted bridge verifies release provenance at runtime. This test
	// ensures the live workflow differs from the approved fixture only by pins.
	if err := CheckSofaQualityContractWithActionPins(caller, reusable, approved, func(ActionPin) error { return nil }); err != nil {
		t.Fatal(err)
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
