package workflow

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestControllerBuildArtifactBoundary(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/controller-build.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Name string `yaml:"name"`
		On   struct {
			WorkflowCall struct {
				Inputs map[string]struct {
					Type string `yaml:"type"`
				} `yaml:"inputs"`
				Outputs map[string]struct {
					Value string `yaml:"value"`
				} `yaml:"outputs"`
				Secrets map[string]any `yaml:"secrets"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Outputs     map[string]string `yaml:"outputs"`
			Steps       []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.Name == "" || len(contract.On.WorkflowCall.Inputs) != 1 ||
		contract.On.WorkflowCall.Inputs["toolkit_sha"].Type != "string" ||
		len(contract.On.WorkflowCall.Secrets) != 0 || strings.Contains(string(data), "${{ secrets.") {
		t.Fatal("controller build must accept only the immutable toolkit revision and no secret")
	}
	if len(contract.Jobs) != 1 {
		t.Fatalf("controller build must have one secretless job, got %d", len(contract.Jobs))
	}
	job, ok := contract.Jobs["build"]
	if !ok || len(job.Permissions) != 1 || job.Permissions["contents"] != "read" ||
		!strings.Contains(job.If, "github.event.repository.default_branch") ||
		!strings.Contains(job.If, "github.event_name == 'workflow_dispatch'") {
		t.Fatal("controller build must stay on the caller's trusted default branch with read-only permissions")
	}
	if contract.On.WorkflowCall.Outputs["artifact_id"].Value != "${{ jobs.build.outputs.artifact_id }}" ||
		job.Outputs["artifact_id"] != "${{ steps.upload.outputs.artifact-id }}" {
		t.Fatal("controller build must expose the uploaded artifact ID for auditing")
	}
	var source, build, artifact bool
	for _, step := range job.Steps {
		switch step.Name {
		case "Check out immutable sofa toolkit":
			source = strings.HasPrefix(step.Uses, "actions/checkout@") &&
				step.With["repository"] == "kevinmartin/sofa" &&
				step.With["ref"] == "${{ inputs.toolkit_sha }}" &&
				step.With["persist-credentials"] == "false"
		case "Build and verify controller provenance":
			build = strings.Contains(step.Run, "go build -trimpath -buildvcs=true") &&
				strings.Contains(step.Run, "vcs.revision=") &&
				strings.Contains(step.Run, "vcs.modified=") &&
				strings.Contains(step.Run, "sha256sum controller/sofa") &&
				strings.Contains(step.Run, "sofa-controller-v1")
		case "Upload pinned controller":
			artifact = strings.HasPrefix(step.Uses, "actions/upload-artifact@") &&
				step.With["name"] == "sofa-controller-${{ inputs.toolkit_sha }}" &&
				step.With["path"] == "controller/" &&
				step.With["if-no-files-found"] == "error" &&
				step.With["retention-days"] == "30"
		}
	}
	if !source || !build || !artifact {
		t.Fatal("controller artifact lost source, binary provenance, or bounded retention")
	}
}
