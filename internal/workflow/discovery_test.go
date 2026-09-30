package workflow

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestDiscoveryWorkflowSeparatesModelAndPublicationAuthority(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/discovery.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	var w reviewWorkflow
	if err := yaml.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.On.WorkflowCall.Secrets["SOFA_PROJECTS_TOKEN"]; !ok || len(w.On.WorkflowCall.Secrets) != 1 {
		t.Fatal("Discovery must receive only the caller-owned Project credential")
	}
	if len(w.Jobs) != 4 || w.Jobs["generate"].Needs != "admit" || !reflect.DeepEqual(w.Jobs["publish"].Needs, []any{"admit", "generate"}) || !reflect.DeepEqual(w.Jobs["finalize-failure"].Needs, []any{"admit", "generate", "publish"}) {
		t.Fatal("Discovery job graph lost admission, generation, publication, or failure fencing")
	}
	finalizer := w.Jobs["finalize-failure"].If
	if !strings.Contains(w.Jobs["admit"].If, "github.event.repository.default_branch") || !strings.Contains(w.Jobs["generate"].If, "recovery_run_id == ''") || !strings.Contains(w.Jobs["publish"].If, "needs.admit.outputs.recovery_run_id != ''") || !strings.Contains(finalizer, "always()") || !strings.Contains(finalizer, "needs.generate.result") || !strings.Contains(finalizer, "needs.publish.result") || !strings.Contains(finalizer, `"failure"`) || !strings.Contains(finalizer, `"cancelled"`) {
		t.Fatal("Discovery execution or terminal recovery condition changed")
	}
	if w.Jobs["generate"].Permissions["copilot-requests"] != "write" || w.Jobs["generate"].Permissions["contents"] != "read" || w.Jobs["admit"].Permissions["copilot-requests"] != "" || w.Jobs["publish"].Permissions["copilot-requests"] != "" || w.Jobs["finalize-failure"].Permissions["copilot-requests"] != "" {
		t.Fatal("model permission escaped the isolated worker")
	}
	if w.Jobs["publish"].Permissions["issues"] != "write" || w.Jobs["generate"].Permissions["issues"] != "" || w.Jobs["admit"].Permissions["issues"] != "read" {
		t.Fatal("issue publication permission escaped the trusted publisher")
	}
	for jobName, job := range w.Jobs {
		for _, step := range job.Steps {
			for name := range step.Env {
				switch name {
				case "SOFA_MODEL_TOKEN":
					if jobName != "generate" {
						t.Errorf("model token entered %s", jobName)
					}
				case "SOFA_PROJECTS_TOKEN":
					if jobName != "admit" && jobName != "publish" {
						t.Errorf("Project token entered %s", jobName)
					}
				case "SOFA_STATE_TOKEN":
					if jobName != "admit" && jobName != "publish" && jobName != "finalize-failure" {
						t.Errorf("state token entered %s", jobName)
					}
				case "SOFA_COMMENT_TOKEN":
					if jobName != "publish" {
						t.Errorf("comment token entered %s", jobName)
					}
				}
			}
		}
	}
	for _, edge := range []struct{ producer, consumer, prefix string }{
		{"admit", "generate", "sofa-discovery-manifest-"},
		{"admit", "publish", "sofa-discovery-manifest-"},
		{"admit", "finalize-failure", "sofa-discovery-manifest-"},
		{"generate", "publish", "sofa-discovery-candidate-"},
	} {
		produced := reviewArtifact(w.Jobs[edge.producer].Steps, "upload-artifact", edge.prefix)
		consumed := reviewArtifact(w.Jobs[edge.consumer].Steps, "download-artifact", edge.prefix)
		if produced == "" || produced != consumed {
			t.Errorf("Discovery artifact edge %s -> %s changed", edge.producer, edge.consumer)
		}
	}
	if !strings.Contains(string(data), "--mount \"type=bind,src=$PWD/transport,dst=/transport,readonly\"") || !strings.Contains(string(data), "--read-only") || !strings.Contains(string(data), "--cap-drop ALL") || !strings.Contains(string(data), "sha256sum --check --status") || strings.Contains(string(data), "discovery move") {
		t.Fatal("worker sandbox, ACP pin, or owner-only approval boundary changed")
	}
}
