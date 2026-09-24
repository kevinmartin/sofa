package workflow

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

type contractWorkflow struct {
	On struct {
		WorkflowCall struct {
			Inputs map[string]struct {
				Type string `yaml:"type"`
			} `yaml:"inputs"`
		} `yaml:"workflow_call"`
	} `yaml:"on"`
	Jobs map[string]contractJob `yaml:"jobs"`
}

type contractJob struct {
	Needs       any               `yaml:"needs"`
	If          string            `yaml:"if"`
	Uses        string            `yaml:"uses"`
	With        map[string]string `yaml:"with"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []struct {
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	} `yaml:"steps"`
}

var toolkitRef = regexp.MustCompile(`^[0-9a-f]{40}$`)

func parseContractWorkflow(data []byte) (contractWorkflow, error) {
	var workflow contractWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		return workflow, err
	}
	return workflow, nil
}

func artifactName(job contractJob, action string, name string) string {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") && strings.HasPrefix(step.With["name"], name) {
			return step.With["name"]
		}
	}
	return ""
}

func checkDeliveryContracts(callerData, reconcileData, workData []byte) error {
	caller, err := parseContractWorkflow(callerData)
	if err != nil {
		return err
	}
	reconcile, err := parseContractWorkflow(reconcileData)
	if err != nil {
		return err
	}
	work, err := parseContractWorkflow(workData)
	if err != nil {
		return err
	}
	if reconcile.On.WorkflowCall.Inputs["issue_number"].Type != "number" || !strings.Contains(caller.Jobs["reconcile"].With["issue_number"], "fromJSON(inputs.issue_number)") {
		return fmt.Errorf("caller issue number must convert to the numeric reusable input")
	}
	refs := []string{
		strings.TrimPrefix(caller.Jobs["reconcile"].Uses, "kevinmartin/sofa/.github/workflows/reconcile.yml@"),
		caller.Jobs["reconcile"].With["toolkit_sha"],
		strings.TrimPrefix(caller.Jobs["work"].Uses, "kevinmartin/sofa/.github/workflows/work.yml@"),
		caller.Jobs["work"].With["toolkit_sha"],
	}
	for _, ref := range refs {
		if !toolkitRef.MatchString(ref) || ref != refs[0] {
			return fmt.Errorf("caller reusable workflow and toolkit commit refs differ")
		}
	}
	if reconcile.On.WorkflowCall.Inputs["toolkit_sha"].Type != "string" || work.On.WorkflowCall.Inputs["toolkit_sha"].Type != "string" {
		return fmt.Errorf("toolkit SHA input must be a string")
	}
	execute, verify, publish := work.Jobs["execute"], work.Jobs["verify"], work.Jobs["publish"]
	if execute.Permissions["copilot-requests"] != "write" || verify.Permissions["copilot-requests"] != "" || publish.Permissions["copilot-requests"] != "" || verify.Permissions["contents"] != "read" || publish.Permissions["contents"] != "read" {
		return fmt.Errorf("execution, verification, or publication permission boundary changed")
	}
	if !strings.Contains(string(workData), "--platform linux/amd64") || !strings.Contains(string(workData), "github-copilot-1.0.86-linux-x64.tgz") || !strings.Contains(string(workData), "node:24-bookworm@sha256:") {
		return fmt.Errorf("worker package or image architecture pin changed")
	}
	for _, handoff := range []struct {
		producer, consumer contractJob
		name               string
	}{
		{reconcile.Jobs["admit"], execute, "sofa-manifest-"},
		{execute, verify, "sofa-candidate-"},
		{verify, publish, "sofa-evidence-"},
		{verify, publish, "sofa-verified-candidate-"},
	} {
		produced := artifactName(handoff.producer, "actions/upload-artifact", handoff.name)
		consumed := artifactName(handoff.consumer, "actions/download-artifact", handoff.name)
		if produced == "" || produced != consumed {
			return fmt.Errorf("artifact producer and consumer names differ for %s", handoff.name)
		}
	}
	if verify.Needs != "execute" || !strings.Contains(verify.If, "always()") || publish.Needs != "verify" || !strings.Contains(publish.If, "always()") || !strings.Contains(publish.If, "needs.verify.result == 'success'") {
		return fmt.Errorf("recovered candidate cannot reach publication after execution skips")
	}
	return nil
}

func TestDeliveryWorkflowContracts(t *testing.T) {
	caller, err := os.ReadFile("../../examples/consumer/.github/workflows/sofa.yml.example")
	if err != nil {
		t.Fatal(err)
	}
	reconcile, err := os.ReadFile("../../.github/workflows/reconcile.yml")
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.ReadFile("../../.github/workflows/work.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDeliveryContracts(caller, reconcile, work); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		file      string
		old, next string
	}{
		{"numeric conversion", "caller", "fromJSON(inputs.issue_number)", "inputs.issue_number"},
		{"toolkit SHA", "caller", "toolkit_sha: 0000000000000000000000000000000000000000", "toolkit_sha: 1111111111111111111111111111111111111111"},
		{"image architecture", "work", "--platform linux/amd64", "--platform linux/arm64"},
		{"permission boundary", "work", "      contents: read\n      actions: read # Only used by download-artifact", "      contents: write\n      actions: read # Only used by download-artifact"},
		{"artifact handoff", "work", "name: sofa-evidence-${{ github.run_id }}-${{ github.run_attempt }}", "name: sofa-other-${{ github.run_id }}-${{ github.run_attempt }}"},
		{"recovery condition", "work", "if: always() && needs.verify.result == 'success'", "if: needs.verify.result == 'success'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			copyCaller, copyReconcile, copyWork := string(caller), string(reconcile), string(work)
			switch test.file {
			case "caller":
				copyCaller = strings.Replace(copyCaller, test.old, test.next, 1)
			case "work":
				copyWork = strings.Replace(copyWork, test.old, test.next, 1)
			}
			if copyCaller == string(caller) && copyWork == string(work) {
				t.Fatal("mutation did not change workflow")
			}
			if err := checkDeliveryContracts([]byte(copyCaller), []byte(copyReconcile), []byte(copyWork)); err == nil {
				t.Fatal("broken workflow contract was accepted")
			}
		})
	}
}
