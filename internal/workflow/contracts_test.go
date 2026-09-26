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
		Name string            `yaml:"name"`
		Run  string            `yaml:"run"`
		Uses string            `yaml:"uses"`
		With map[string]string `yaml:"with"`
	} `yaml:"steps"`
}

var toolkitRef = regexp.MustCompile(`^[0-9a-f]{40}$`)

const (
	// Independently verified against the Copilot release metadata and the
	// AMD64 OCI manifest; see the milestone 01.1 evidence report.
	copilotArchiveSHA256 = "53284019748ac198c3dbcf9ba17f0541c8fcaaee16e6c644eb0e7285cdfd6112"
	nodeAMD64SHA256      = "b977d0f785d96029d8d4c0790b6bf1c2a4c72e0f26319808e7ba2e9d966a1ac3"
)

var archiveChecksumLine = regexp.MustCompile(`(?m)^[ \t]*echo '([0-9a-f]{64})  '"\$RUNNER_TEMP/github-copilot-linux-x64\.tgz" \| sha256sum --check --status[ \t]*$`)
var dockerAMD64Line = regexp.MustCompile(`(?m)^[ \t]*docker run --rm --platform linux/amd64[ \t]+\\[ \t]*$`)
var nodeImagePin = regexp.MustCompile(`(?m)^[ \t]*node:24-bookworm@sha256:([0-9a-f]{64})[ \t]+\\[ \t]*$`)

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
	if !strings.Contains(string(workData), "github-copilot-1.0.86-linux-x64.tgz") {
		return fmt.Errorf("worker package or image architecture pin changed")
	}
	var fetchRun, workerRun string
	for _, step := range execute.Steps {
		switch step.Name {
		case "Fetch pinned Copilot ACP package":
			if fetchRun != "" {
				return fmt.Errorf("duplicate Copilot package fetch step")
			}
			fetchRun = step.Run
		case "Run isolated candidate worker":
			if workerRun != "" {
				return fmt.Errorf("duplicate isolated worker step")
			}
			workerRun = step.Run
		}
	}
	checksums := archiveChecksumLine.FindAllStringSubmatch(fetchRun, -1)
	if len(checksums) != 1 || checksums[0][1] != copilotArchiveSHA256 || strings.Count(fetchRun, "sha256sum --check --status") != 1 {
		return fmt.Errorf("Copilot archive checksum pin changed")
	}
	dockerLines := dockerAMD64Line.FindAllStringIndex(workerRun, -1)
	images := nodeImagePin.FindAllStringSubmatchIndex(workerRun, -1)
	if len(dockerLines) != 1 || len(images) != 1 || images[0][0] <= dockerLines[0][1] || workerRun[images[0][2]:images[0][3]] != nodeAMD64SHA256 {
		return fmt.Errorf("worker AMD64 image digest pin changed")
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
		{"commented image architecture", "work", "docker run --rm --platform linux/amd64", "# docker run --rm --platform linux/amd64\n          docker run --rm --platform linux/arm64"},
		{"Copilot archive digest", "work", copilotArchiveSHA256, "13284019748ac198c3dbcf9ba17f0541c8fcaaee16e6c644eb0e7285cdfd6112"},
		{"worker image digest", "work", nodeAMD64SHA256, "a977d0f785d96029d8d4c0790b6bf1c2a4c72e0f26319808e7ba2e9d966a1ac3"},
		{"commented worker image", "work", "node:24-bookworm@sha256:" + nodeAMD64SHA256, "# node:24-bookworm@sha256:" + nodeAMD64SHA256 + "\n            node:24-bookworm"},
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
