package workflow

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type reviewStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
	With map[string]string `yaml:"with"`
}

type reviewJob struct {
	Needs       any               `yaml:"needs"`
	If          string            `yaml:"if"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []reviewStep      `yaml:"steps"`
}

type reviewWorkflow struct {
	Name string `yaml:"name"`
	On   struct {
		WorkflowCall struct {
			Inputs  map[string]any `yaml:"inputs"`
			Secrets map[string]struct {
				Required bool `yaml:"required"`
			} `yaml:"secrets"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]reviewJob `yaml:"jobs"`
}

func TestReviewRepairWorkflowKeepsJobAndCredentialBoundaries(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/review.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w reviewWorkflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	if w.Name == "" || len(w.On.WorkflowCall.Inputs) != 3 || len(w.On.WorkflowCall.Secrets) != 4 {
		t.Fatal("review workflow call contract changed")
	}
	for _, name := range []string{"toolkit_sha", "release_version", "issue_number"} {
		if _, ok := w.On.WorkflowCall.Inputs[name]; !ok {
			t.Fatalf("missing %s input", name)
		}
	}
	for _, name := range []string{"SOFA_PROJECTS_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_PUBLISH_APP_ID", "SOFA_PUBLISH_APP_PRIVATE_KEY"} {
		secret, ok := w.On.WorkflowCall.Secrets[name]
		if !ok || secret.Required != (name == "SOFA_PROJECTS_TOKEN") {
			t.Fatalf("missing %s caller-owned secret", name)
		}
	}
	if len(w.Jobs) != 6 || w.Jobs["admit"].Needs != "candidate-build" || w.Jobs["execute"].Needs != "admit" || w.Jobs["verify"].Needs != "execute" || w.Jobs["publish"].Needs != "verify" || !reflect.DeepEqual(w.Jobs["finalize-failure"].Needs, []any{"admit", "execute", "verify", "publish"}) {
		t.Fatal("review job graph changed")
	}
	if !strings.Contains(w.Jobs["admit"].If, "github.event_name == 'workflow_dispatch'") || !strings.Contains(w.Jobs["admit"].If, "github.event.repository.default_branch") || !strings.Contains(w.Jobs["execute"].If, "needs.admit.outputs.dispatch == 'true'") || !strings.Contains(w.Jobs["verify"].If, "always() && needs.execute.result == 'success'") || !strings.Contains(w.Jobs["publish"].If, "always() && needs.verify.result == 'success'") || !strings.Contains(w.Jobs["finalize-failure"].If, "always()") || !strings.Contains(w.Jobs["finalize-failure"].If, "needs.admit.outputs.dispatch == 'true'") {
		t.Fatal("denial, success, or failure path changed")
	}
	for _, stage := range []string{"execute", "verify", "publish"} {
		if !strings.Contains(w.Jobs["finalize-failure"].If, "needs."+stage+".result") {
			t.Errorf("%s failure or cancellation does not reach finalizer", stage)
		}
	}
	if strings.Count(w.Jobs["finalize-failure"].If, `'["failure","cancelled"]'`) != 3 {
		t.Fatal("cancelled repair stage does not reach finalizer")
	}
	for _, step := range w.Jobs["finalize-failure"].Steps {
		if !strings.Contains(step.Run, "./sofa review-repair fail") {
			continue
		}
		if !strings.Contains(step.Run, `"$SOFA_EXECUTE_RESULT" != success`) || !strings.Contains(step.Run, `"$SOFA_VERIFY_RESULT" != success`) {
			t.Fatal("finalizer does not attribute a non-success to the earliest failed repair stage")
		}
	}
	if w.Jobs["execute"].Permissions["copilot-requests"] != "write" || w.Jobs["verify"].Permissions["copilot-requests"] != "" || w.Jobs["publish"].Permissions["copilot-requests"] != "" || w.Jobs["finalize-failure"].Permissions["copilot-requests"] != "" {
		t.Fatal("model permission escaped the worker job")
	}
	if w.Jobs["verify"].Permissions["contents"] != "read" || w.Jobs["publish"].Permissions["contents"] != "read" || w.Jobs["admit"].Permissions["contents"] != "write" || w.Jobs["finalize-failure"].Permissions["contents"] != "write" {
		t.Fatal("state, verifier, or publisher permission changed")
	}
	for jobName, job := range w.Jobs {
		for _, step := range job.Steps {
			if strings.HasPrefix(step.Uses, "actions/create-github-app-token@") {
				if jobName != "publish" || step.If != "steps.publisher-auth.outputs.mode == 'app'" || step.With["permission-contents"] != "write" || step.With["permission-pull-requests"] != "write" || step.With["owner"] != "" || step.With["repositories"] != "" {
					t.Errorf("App publisher credential escaped repository-scoped publish job")
				}
			}
			for name := range step.Env {
				switch name {
				case "SOFA_MODEL_TOKEN":
					if jobName != "execute" {
						t.Errorf("model token present in %s", jobName)
					}
				case "SOFA_PROJECTS_TOKEN":
					if jobName != "admit" && jobName != "publish" {
						t.Errorf("Project token present in %s", jobName)
					}
				case "SOFA_PUBLISH_TOKEN":
					if jobName != "publish" {
						t.Errorf("publisher token present in %s", jobName)
					}
				case "SOFA_HAS_APP_ID", "SOFA_HAS_APP_KEY", "SOFA_HAS_LEGACY_TOKEN":
					if jobName != "publish" {
						t.Errorf("publisher credential selection present in %s", jobName)
					}
				case "SOFA_STATE_TOKEN":
					if jobName != "admit" && jobName != "finalize-failure" {
						t.Errorf("state token present in %s", jobName)
					}
				}
			}
		}
	}
	var appMint, appPublication, legacyPublication int
	for _, step := range w.Jobs["publish"].Steps {
		if strings.HasPrefix(step.Uses, "actions/create-github-app-token@") {
			appMint++
		}
		if strings.Contains(step.Run, "./sofa review-repair publish") {
			switch step.If {
			case "steps.publisher-auth.outputs.mode == 'app'":
				appPublication++
				if step.Env["SOFA_PUBLISH_TOKEN"] != "${{ steps.publisher-app.outputs.token }}" {
					t.Error("App repair publication may fall back to another identity")
				}
			case "steps.publisher-auth.outputs.mode == 'legacy'":
				legacyPublication++
				if step.Env["SOFA_PUBLISH_TOKEN"] != "${{ secrets.SOFA_PUBLISH_TOKEN }}" {
					t.Error("legacy repair publication uses unexpected identity")
				}
			default:
				t.Error("repair publication lacks exclusive credential mode")
			}
		}
	}
	if appMint != 1 || appPublication != 1 || legacyPublication != 1 {
		t.Fatal("repair publisher App or command missing")
	}
	for _, edge := range []struct{ producer, consumer, artifact string }{
		{"admit", "execute", "sofa-review-manifest-"},
		{"admit", "verify", "sofa-review-manifest-"},
		{"admit", "publish", "sofa-review-manifest-"},
		{"admit", "finalize-failure", "sofa-review-manifest-"},
		{"execute", "verify", "sofa-review-candidate-"},
		{"verify", "publish", "sofa-review-evidence-"},
		{"verify", "publish", "sofa-review-verified-"},
	} {
		produced := reviewArtifact(w.Jobs[edge.producer].Steps, "upload-artifact", edge.artifact)
		consumed := reviewArtifact(w.Jobs[edge.consumer].Steps, "download-artifact", edge.artifact)
		if produced == "" || produced != consumed {
			t.Errorf("artifact edge %s -> %s (%s) is broken", edge.producer, edge.consumer, edge.artifact)
		}
	}
	for jobName, command := range map[string]string{
		"admit":            "./sofa review-repair admit",
		"execute":          "/toolkit/sofa review-repair execute",
		"verify":           "./sofa review-repair verify",
		"publish":          "./sofa review-repair publish",
		"finalize-failure": "./sofa review-repair fail",
	} {
		found := false
		for _, step := range w.Jobs[jobName].Steps {
			found = found || strings.Contains(step.Run, command)
		}
		if !found {
			t.Errorf("%s has no %s invocation", jobName, command)
		}
	}
	if !strings.Contains(string(data), "--platform linux/amd64") || !strings.Contains(string(data), "sha256sum --check --status") || !strings.Contains(string(data), "--mount \"type=bind,src=$PWD/transport,dst=/transport,readonly\"") {
		t.Fatal("isolated worker runtime boundary changed")
	}
}

func reviewArtifact(steps []reviewStep, action, prefix string) string {
	for _, step := range steps {
		if strings.HasPrefix(step.Uses, "actions/"+action+"@") && strings.HasPrefix(step.With["name"], prefix) {
			return step.With["name"]
		}
	}
	return ""
}
