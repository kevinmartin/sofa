package workflow

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCLISelectionUsesOneInstallerBeforeCredentials(t *testing.T) {
	for _, name := range []string{"reconcile", "work", "discovery", "review", "lifecycle"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../../.github/workflows/" + name + ".reusable.yml")
			if err != nil {
				t.Fatal(err)
			}
			w, err := parseContractWorkflow(data)
			if err != nil {
				t.Fatal(err)
			}
			input := w.On.WorkflowCall.Inputs["version"]
			if input.Type != "string" || input.Required || input.Default != "" {
				t.Fatal("version must be one optional selector")
			}
			for _, old := range []string{"toolkit_sha", "release_version"} {
				if _, exists := w.On.WorkflowCall.Inputs[old]; exists {
					t.Fatalf("redundant %s selector remains", old)
				}
			}
			if _, exists := w.Jobs["candidate-build"]; exists {
				t.Fatal("separate candidate build remains")
			}
			installedJobs := 0
			for jobName, job := range w.Jobs {
				validationAt, checkoutAt, sourceAt, releaseAt, credentialAt := -1, -1, -1, -1, len(job.Steps)
				for i, step := range job.Steps {
					if strings.HasPrefix(step.Name, "Validate exact source selector") {
						validationAt = i
						if !strings.Contains(step.Run, "^[a-f0-9]{40}$") || step.Env["SOFA_VERSION"] != "${{ inputs.version }}" {
							t.Fatal("source installer can load a non-exact reference")
						}
					}
					if step.Name == "Check out exact source installer without credentials" {
						checkoutAt = i
						if step.With["repository"] != "kevinmartin/sofa" || step.With["ref"] != "${{ inputs.version }}" || step.With["persist-credentials"] != "false" {
							t.Fatal("source checkout identity or credential boundary changed")
						}
					}
					if step.Name == "Set up source Sofa CLIs" {
						sourceAt = i
						if step.Uses != "./.sofa-cli-source/.github/actions/setup-cli" || step.With["version"] != "${{ inputs.version }}" || step.With["major"] != "v0" {
							t.Fatal("source mode bypasses the shared installer")
						}
					}
					if step.Name == "Set up released Sofa CLIs" {
						releaseAt = i
						if !strings.HasPrefix(step.Uses, "kevinmartin/sofa/.github/actions/setup-cli@") || step.With["version"] != "${{ inputs.version }}" || step.With["major"] != "v0" {
							t.Fatal("release mode bypasses the shared installer or major guard")
						}
					}
					for _, value := range step.Env {
						if strings.Contains(value, "${{ secrets.") && i < credentialAt {
							credentialAt = i
						}
					}
					if strings.HasPrefix(step.Uses, "actions/download-artifact@") && strings.HasPrefix(step.With["name"], "sofa-cli-") {
						t.Fatal("candidate binary artifact handoff remains")
					}
				}
				if sourceAt >= 0 || releaseAt >= 0 {
					installedJobs++
					if validationAt < 0 || checkoutAt <= validationAt || sourceAt <= checkoutAt || releaseAt <= sourceAt || credentialAt <= releaseAt {
						t.Fatalf("%s loads source before validation or grants secrets before setup", jobName)
					}
				}
			}
			if installedJobs == 0 {
				t.Fatal("workflow has no shared CLI installer")
			}
		})
	}
}

func TestHostedDistributionE2ERequiresEverySecretlessHandoff(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/distribution-e2e.yml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := parseContractWorkflow(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Jobs) != 3 || !w.On.WorkflowCall.Inputs["toolkit_sha"].Required || strings.Contains(string(data), "${{ secrets.") {
		t.Fatal("source proof must remain exact and secretless")
	}
	first, next, result := w.Jobs["first"], w.Jobs["next"], w.Jobs["result"]
	if first.Needs != nil || next.Needs != "first" || !strings.Contains(next.If, "needs.first.result == 'success'") {
		t.Fatal("failed first-job state may reach the second job")
	}
	if !reflect.DeepEqual(result.Needs, []any{"first", "next"}) || result.If != "always()" {
		t.Fatal("result must await every stage")
	}
	aggregate := false
	for _, step := range result.Steps {
		if step.Env["SOFA_FIRST_RESULT"] == "${{ needs.first.result }}" && step.Env["SOFA_NEXT_RESULT"] == "${{ needs.next.result }}" && strings.Contains(step.Run, "test \"$SOFA_FIRST_RESULT\" = success") && strings.Contains(step.Run, "test \"$SOFA_NEXT_RESULT\" = success") {
			aggregate = true
		}
	}
	if !aggregate {
		t.Fatal("aggregate accepts a missing, failed or skipped stage")
	}
	for _, job := range []contractJob{first, next} {
		if len(job.Permissions) != 2 || job.Permissions["contents"] != "read" || job.Permissions["actions"] != "read" {
			t.Fatal("source proof gained model or write permission")
		}
		installed, identity, state, goFree := false, false, false, false
		for _, step := range job.Steps {
			installed = installed || step.Uses == "./.sofa-cli-source/.github/actions/setup-cli" && step.With["version"] == "${{ inputs.toolkit_sha }}"
			identity = identity || strings.Contains(step.Run, ".source_commit == $sha")
			state = state || strings.Contains(step.Run, "./sofa-test distribution compatibility-fixture")
			if step.Name == "Prove both CLIs remain on PATH in a later step" {
				exportAt := strings.Index(step.Run, "export PATH=\"$RUNNER_TEMP/sofa-cli\"")
				goAt := strings.Index(step.Run, "command -v go")
				loopAt := strings.Index(step.Run, "for binary in sofa sofa-test")
				goFree = exportAt >= 0 && goAt > exportAt && loopAt > goAt
			}
		}
		if !installed || !identity || !state || !goFree {
			t.Fatal("fresh job lost installer, exact identity, state or ordered Go-free proof")
		}
	}
	produced := artifactName(first, "actions/upload-artifact", "sofa-distribution-fixture-")
	consumed := artifactName(next, "actions/download-artifact", "sofa-distribution-fixture-")
	if produced == "" || produced != consumed {
		t.Fatal("state handoff changed ownership")
	}
}

// actionlint 1.7.12 predates GitHub's queue key. Keep this temporary gap
// narrow: validate its exact release-workflow semantics and reject unknown
// concurrency keys, while actionlint still checks every other workflow field.
func checkReleaseConcurrency(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("release workflow is not a YAML mapping")
	}
	root := document.Content[0]
	var concurrency *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "concurrency" {
			if concurrency != nil {
				return errors.New("duplicate release concurrency declaration")
			}
			concurrency = root.Content[i+1]
		}
	}
	if concurrency == nil || concurrency.Kind != yaml.MappingNode {
		return errors.New("release workflow lacks concurrency mapping")
	}
	values := make(map[string]*yaml.Node)
	for i := 0; i+1 < len(concurrency.Content); i += 2 {
		key := concurrency.Content[i].Value
		if key != "group" && key != "cancel-in-progress" && key != "queue" {
			return fmt.Errorf("unsupported release concurrency key %q", key)
		}
		if _, exists := values[key]; exists {
			return fmt.Errorf("duplicate release concurrency key %q", key)
		}
		values[key] = concurrency.Content[i+1]
	}
	if len(values) != 3 || values["group"].Tag != "!!str" || values["group"].Value != "sofa-toolkit-release" || values["cancel-in-progress"].Tag != "!!bool" || values["cancel-in-progress"].Value != "false" || values["queue"].Tag != "!!str" || values["queue"].Value != "max" {
		return errors.New("release workflow must serialize all runs with queue:max and no cancellation")
	}
	return nil
}

func TestReleaseConcurrencyQueueContract(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkReleaseConcurrency(data); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		old     string
		changed string
	}{
		{
			name:    "cancel prior run",
			old:     "cancel-in-progress: false",
			changed: "cancel-in-progress: true",
		},
		{
			name:    "single pending run",
			old:     "queue: max",
			changed: "queue: single",
		},
		{
			name:    "unknown key",
			old:     "queue: max",
			changed: "queue: max\n  replacement: true",
		},
		{
			name:    "missing queue",
			old:     "  queue: max\n",
			changed: "",
		},
		{
			name:    "duplicate key",
			old:     "  queue: max\n",
			changed: "  queue: max\n  queue: max\n",
		},
		{
			name:    "per-ref group loses release serialization",
			old:     "group: sofa-toolkit-release",
			changed: "group: ${{ github.ref }}",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := strings.Replace(string(data), tc.old, tc.changed, 1)
			if mutated == string(data) {
				t.Fatal("test mutation did not alter release workflow")
			}
			if err := checkReleaseConcurrency([]byte(mutated)); err == nil {
				t.Fatal("unsafe release concurrency policy was accepted")
			}
		})
	}
}
