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

func TestCLISelectionKeepsReleaseAndCandidateBoundaries(t *testing.T) {
	for _, name := range []string{"reconcile", "work", "discovery", "review", "lifecycle"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("../../.github/workflows/" + name + ".reusable.yml")
			if err != nil {
				t.Fatal(err)
			}
			workflow, err := parseContractWorkflow(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []string{"toolkit_sha", "release_version"} {
				option, ok := workflow.On.WorkflowCall.Inputs[input]
				if !ok || option.Type != "string" || option.Required || option.Default != "" {
					t.Errorf("%s must be an optional empty string", input)
				}
			}
			if name != "work" {
				build, ok := workflow.Jobs["candidate-build"]
				if !ok || build.If != "inputs.toolkit_sha != ''" || build.Permissions["contents"] != "read" || len(build.Permissions) != 1 || build.With["toolkit_sha"] != "${{ inputs.toolkit_sha }}" || build.With["release_version"] != "${{ inputs.release_version }}" {
					t.Error("candidate source build must be read-only, conditional, and bound to both selector inputs")
				}
				first := workflow.Jobs["admit"]
				if name == "lifecycle" {
					first = workflow.Jobs["reconcile"]
				}
				if first.Needs != "candidate-build" || !strings.Contains(first.If, "always()") || !strings.Contains(first.If, "needs.candidate-build.result == 'success'") || !strings.Contains(first.If, "needs.candidate-build.result == 'skipped' && inputs.toolkit_sha == ''") {
					t.Error("first credentialed job must wait for successful candidate build or valid released-mode skip")
				}
			} else if _, exists := workflow.Jobs["candidate-build"]; exists {
				t.Error("work must consume reconcile's exact candidate artifact within the same invocation")
			}
			cliJobs := 0
			for jobName, job := range workflow.Jobs {
				var released, candidate, selection bool
				for _, step := range job.Steps {
					switch step.Name {
					case "Install released Sofa CLIs":
						released = true
						if step.If != "inputs.toolkit_sha == ''" || step.With["release_version"] != "${{ inputs.release_version }}" || !strings.HasPrefix(step.Uses, "kevinmartin/sofa/.github/actions/setup-cli@") {
							t.Errorf("%s released installer is not conditional or ignores requested release", jobName)
						}
					case "Download exact candidate binaries from this invocation":
						candidate = true
						if step.If != "inputs.toolkit_sha != ''" || !strings.HasPrefix(step.Uses, "actions/download-artifact@") || step.With["name"] != candidateCLIArtifactName {
							t.Errorf("%s candidate artifact is not bound to this source and invocation", jobName)
						}
					case "Verify candidate selection and expose job binaries":
						selection = strings.Contains(step.Run, "sha256sum --check manifest.sha256") && strings.Contains(step.Run, `-z "$SOFA_RELEASE_VERSION"`)
					}
				}
				if released || candidate || selection {
					cliJobs++
					if !released || !candidate || !selection {
						t.Errorf("%s lost one of the released, candidate, or verification paths", jobName)
					}
				}
			}
			if cliJobs == 0 {
				t.Error("workflow no longer prepares CLIs in its jobs")
			}
		})
	}
}

const candidateCLIArtifactName = "sofa-cli-${{ inputs.toolkit_sha }}-${{ github.run_id }}"

func TestCandidateBuildCarriesBothCLIsWithoutSecrets(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/candidate-build.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := parseContractWorkflow(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) != 1 || strings.Contains(string(data), "${{ secrets.") {
		t.Fatal("candidate build must have one secretless job")
	}
	build := workflow.Jobs["build"]
	if len(build.Permissions) != 1 || build.Permissions["contents"] != "read" {
		t.Fatal("candidate build gained write or credential permissions")
	}
	var checkedSource, builtBoth, uploaded bool
	for _, step := range build.Steps {
		switch step.Name {
		case "Verify checkout identity":
			checkedSource = strings.Contains(step.Run, "git -C toolkit rev-parse HEAD") && step.Env["SOFA_TOOLKIT_SHA"] == "${{ inputs.toolkit_sha }}"
		case "Build both candidate CLIs":
			builtBoth = strings.Contains(step.Run, "./cmd/sofa") && strings.Contains(step.Run, "./cmd/sofa-test") && strings.Contains(step.Run, "manifest.sha256")
		case "Carry binaries only within this invocation":
			uploaded = strings.HasPrefix(step.Uses, "actions/upload-artifact@") && step.With["name"] == candidateCLIArtifactName && step.With["if-no-files-found"] == "error" && step.With["overwrite"] == "true"
		}
	}
	if !checkedSource || !builtBoth || !uploaded {
		t.Fatalf("candidate source, both CLI builds, or exact artifact handoff missing: checked=%t both=%t uploaded=%t", checkedSource, builtBoth, uploaded)
	}
}

func TestHostedDistributionE2ERequiresEverySecretlessHandoff(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/distribution-e2e.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	w, err := parseContractWorkflow(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Jobs) != 4 || w.On.WorkflowCall.Inputs["toolkit_sha"].Type != "string" || !w.On.WorkflowCall.Inputs["toolkit_sha"].Required || strings.Contains(string(data), "${{ secrets.") {
		t.Fatal("hosted distribution test must require exact source and carry no secret")
	}
	build, first, next, result := w.Jobs["candidate-build"], w.Jobs["first"], w.Jobs["next"], w.Jobs["result"]
	if build.Uses != "./.github/workflows/candidate-build.reusable.yml" || build.With["toolkit_sha"] != "${{ inputs.toolkit_sha }}" || len(build.Permissions) != 1 || build.Permissions["contents"] != "read" {
		t.Fatal("hosted distribution test bypasses the reusable candidate build")
	}
	if first.Needs != "candidate-build" || !strings.Contains(first.If, "always()") || !strings.Contains(first.If, "needs.candidate-build.result == 'success'") || next.Needs != "first" || !strings.Contains(next.If, "always()") || !strings.Contains(next.If, "needs.first.result == 'success'") {
		t.Fatal("failed or skipped candidate build or first-job handoff may reach later work")
	}
	if !reflect.DeepEqual(result.Needs, []any{"candidate-build", "first", "next"}) || result.If != "always()" {
		t.Fatal("distribution result does not await every required stage")
	}
	var aggregate bool
	for _, step := range result.Steps {
		if step.Env["SOFA_BUILD_RESULT"] == "${{ needs.candidate-build.result }}" && step.Env["SOFA_FIRST_RESULT"] == "${{ needs.first.result }}" && step.Env["SOFA_NEXT_RESULT"] == "${{ needs.next.result }}" && strings.Contains(step.Run, `test "$SOFA_BUILD_RESULT" = success`) && strings.Contains(step.Run, `test "$SOFA_FIRST_RESULT" = success`) && strings.Contains(step.Run, `test "$SOFA_NEXT_RESULT" = success`) {
			aggregate = true
		}
	}
	if !aggregate {
		t.Fatal("aggregate result does not fail for a missing, failed, or skipped stage")
	}
	if len(first.Permissions) != 2 || first.Permissions["contents"] != "read" || first.Permissions["actions"] != "read" || len(next.Permissions) != 2 || next.Permissions["contents"] != "read" || next.Permissions["actions"] != "read" || len(result.Permissions) != 0 {
		t.Fatal("distribution test gained model or write permissions")
	}
	for _, job := range []contractJob{first, next} {
		var binaryArtifact, versionCheck, stateCheck, pathExport, laterPathCheck bool
		pathExportIndex, laterPathCheckIndex := -1, -1
		for index, step := range job.Steps {
			binaryArtifact = binaryArtifact || strings.HasPrefix(step.Uses, "actions/download-artifact@") && step.With["name"] == candidateCLIArtifactName
			versionCheck = versionCheck || strings.Contains(step.Run, ".source_commit == $sha") && strings.Contains(step.Run, "sha256sum --check manifest.sha256")
			stateCheck = stateCheck || strings.Contains(step.Run, "./sofa-test distribution compatibility-fixture")
			if strings.Contains(step.Run, `echo "$RUNNER_TEMP/sofa-cli" >> "$GITHUB_PATH"`) {
				pathExport = true
				pathExportIndex = index
			}
			if step.Name == "Prove both CLIs remain on PATH in a later step" {
				laterPathCheck = strings.Contains(step.Run, `"$binary" --version`) && strings.Contains(step.Run, `"$binary" version`) && strings.Contains(step.Run, `for binary in sofa sofa-test`) && strings.Contains(step.Run, `.source_commit == $sha`) && strings.Contains(step.Run, `export PATH="$RUNNER_TEMP/sofa-cli"`) && strings.Contains(step.Run, "command -v go") && strings.Contains(step.Run, "sofa-test distribution compatibility-fixture")
				laterPathCheckIndex = index
			}
			if strings.Contains(step.Run, "go build ") {
				t.Fatal("hosted follow-on job rebuilt a CLI instead of consuming the exact artifact")
			}
		}
		if !binaryArtifact || !versionCheck || !stateCheck || !pathExport || !laterPathCheck || laterPathCheckIndex <= pathExportIndex {
			t.Fatal("fresh job lost exact binary artifact, source identity, state handoff, or subsequent-step PATH usability")
		}
	}
	produced := artifactName(first, "actions/upload-artifact", "sofa-distribution-fixture-")
	consumed := artifactName(next, "actions/download-artifact", "sofa-distribution-fixture-")
	if produced == "" || produced != consumed {
		t.Fatal("compatibility fixture is not carried to the next job")
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
