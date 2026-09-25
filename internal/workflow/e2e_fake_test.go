package workflow

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func checkFakeWorkflowContract(data []byte) error {
	w, err := parseContractWorkflow(data)
	if err != nil {
		return err
	}
	for _, name := range []string{"suite_id", "scenario", "denial_kind", "publish_fault", "candidate_sha", "base_sha", "disposable_base_sha", "producer_run_id", "producer_run_attempt"} {
		if w.On.WorkflowCall.Inputs[name].Type != "string" {
			return fmt.Errorf("fake workflow input %s is not typed as string", name)
		}
	}
	if w.On.WorkflowCall.Inputs["reconcile_candidate"].Type != "boolean" {
		return fmt.Errorf("fake recovery input is not boolean")
	}
	execute, verify, publish := w.Jobs["execute"], w.Jobs["verify"], w.Jobs["publish"]
	denial := w.Jobs["assert-denied"]
	for name, job := range map[string]contractJob{"execute": execute, "verify": verify, "publish": publish} {
		if job.Permissions["contents"] != "read" || job.Permissions["contents"] == "write" || job.Permissions["copilot-requests"] != "" || job.Permissions["statuses"] != "" || job.Permissions["pull-requests"] != "" || !strings.Contains(job.If, "github.repository == 'kevinmartin/sofa-disposable'") || !strings.Contains(job.If, "startsWith(github.ref, 'refs/heads/sofa-e2e/')") {
			return fmt.Errorf("fake %s job has an unsafe identity or permission boundary", name)
		}
	}
	if verify.Permissions["actions"] != "read" || execute.Permissions["actions"] != "" || publish.Permissions["actions"] != "" {
		return fmt.Errorf("fake artifact read permission changed")
	}
	for name, job := range map[string]contractJob{"execute": execute, "verify": verify, "publish": publish} {
		if !strings.Contains(job.If, "inputs.scenario == 'edit'") || !strings.Contains(job.If, "inputs.denial_kind == ''") {
			return fmt.Errorf("fake %s job can run for a denial scenario", name)
		}
	}
	if denial.Permissions["contents"] != "read" || len(denial.Permissions) != 1 || !strings.Contains(denial.If, "always()") || !strings.Contains(denial.If, "github.repository == 'kevinmartin/sofa-disposable'") || !strings.Contains(denial.If, "startsWith(github.ref, 'refs/heads/sofa-e2e/')") || !strings.Contains(denial.If, "inputs.scenario == 'denied'") || !strings.Contains(denial.If, "inputs.denial_kind == 'non-ready'") || !strings.Contains(denial.If, "inputs.denial_kind == 'completed-redelivery'") || !strings.Contains(denial.If, "!inputs.reconcile_candidate") {
		return fmt.Errorf("fake denial assertion identity or permission boundary changed")
	}
	if needs, ok := denial.Needs.([]any); !ok || len(needs) != 3 || needs[0] != "execute" || needs[1] != "verify" || needs[2] != "publish" {
		return fmt.Errorf("fake denial assertion does not observe all three scheduler results")
	}
	content := string(data)
	for _, forbidden := range []string{"${{ secrets.", "SOFA_PROJECTS_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_GATE_APP_PRIVATE_KEY", "copilot-requests: write"} {
		if strings.Contains(content, forbidden) {
			return fmt.Errorf("fake workflow references a privileged credential or permission")
		}
	}
	for _, forbidden := range []string{"-e SOFA_E2E_HOST_ONLY_TOKEN", "--env-file", "src=$RUNNER_TEMP"} {
		if strings.Contains(content, forbidden) {
			return fmt.Errorf("fake worker receives a host-only credential")
		}
	}
	if !strings.Contains(content, "--network none") || !strings.Contains(content, "SOFA_MODEL_TOKEN=sofa-fake-acp-inert-token") || !strings.Contains(content, "SOFA_COPILOT_PATH=/toolkit/fake-acp") || !strings.Contains(content, "--platform linux/amd64") {
		return fmt.Errorf("fake worker isolation or identity changed")
	}
	for _, required := range []string{
		"export SOFA_E2E_HOST_ONLY_TOKEN=\"sofa-e2e-host-$(openssl rand -hex 32)\"",
		"host_file_sentinel=\"sofa-e2e-host-file-$(openssl rand -hex 32)\"",
		"host_file=\"$(mktemp \"$RUNNER_TEMP/sofa-e2e-host-only.XXXXXX\")\"",
		"echo \"::add-mask::$SOFA_E2E_HOST_ONLY_TOKEN\"",
		"echo \"::add-mask::$host_file_sentinel\"",
		"-e SOFA_E2E_HOST_ONLY_FILE=\"$host_file\"",
		"if [ \"${SOFA_E2E_HOST_ONLY_TOKEN+x}\" ]; then",
		"if [ -e \"$SOFA_E2E_HOST_ONLY_FILE\" ]; then",
		"done < <(find transport candidate -type f -print0)",
		"grep -Fq -- \"$SOFA_E2E_HOST_ONLY_TOKEN\" \"$artifact\"",
		"grep -Fq -- \"$host_file_sentinel\" \"$artifact\"",
	} {
		if !strings.Contains(content, required) {
			return fmt.Errorf("fake host-only sentinel boundary is missing %q", required)
		}
	}
	if verify.Needs != "execute" || !strings.Contains(verify.If, "always()") || !strings.Contains(verify.If, "needs.execute.result == 'skipped' && inputs.reconcile_candidate") || publish.Needs != "verify" || !strings.Contains(publish.If, "always()") || !strings.Contains(publish.If, "needs.verify.result == 'success'") {
		return fmt.Errorf("fake recovery chain cannot publish after execution skips")
	}
	for _, handoff := range []struct {
		producer, consumer contractJob
		name               string
	}{
		{execute, verify, "sofa-e2e-source-"},
		{execute, verify, "sofa-e2e-candidate-"},
		{verify, publish, "sofa-e2e-verified-"},
	} {
		produced := artifactName(handoff.producer, "actions/upload-artifact", handoff.name)
		consumed := artifactName(handoff.consumer, "actions/download-artifact", handoff.name)
		if produced == "" || produced != consumed {
			return fmt.Errorf("fake artifact %s handoff changed", handoff.name)
		}
	}
	if artifactName(publish, "actions/upload-artifact", "sofa-e2e-report-") == "" {
		return fmt.Errorf("fake scenario report artifact missing")
	}
	for _, required := range []string{
		"name: Inject controlled publication interruption",
		"if: inputs.publish_fault == 'before-publication' && !inputs.reconcile_candidate",
		"exit 42",
		"name: Simulate trusted publication against controlled GitHub API\n        if: inputs.publish_fault == ''",
		"name: Reject unsupported or incompatible publication fault",
		"inputs.reconcile_candidate && inputs.publish_fault != ''",
	} {
		if !strings.Contains(content, required) {
			return fmt.Errorf("controlled publication failure or recovery guard missing %q", required)
		}
	}
	if artifactName(denial, "actions/upload-artifact", "sofa-e2e-denial-") == "" || !strings.Contains(content, "bin/e2e-fixture deny") || !strings.Contains(content, "--execute-result \"$SOFA_E2E_EXECUTE_RESULT\"") || !strings.Contains(content, "--verify-result \"$SOFA_E2E_VERIFY_RESULT\"") || !strings.Contains(content, "--publish-result \"$SOFA_E2E_PUBLISH_RESULT\"") {
		return fmt.Errorf("fake denial report or scheduler assertions are missing")
	}
	return nil
}

func TestFakeHostedWorkflowContract(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/e2e-fake.yml")
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFakeWorkflowContract(data); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, next string }{
		{"network isolation", "--network none", "--network bridge"},
		{"credential boundary", "permissions: {}", "permissions: {}\n# ${{ secrets.SOFA_PUBLISH_TOKEN }}"},
		{"host environment boundary", "if [ \"${SOFA_E2E_HOST_ONLY_TOKEN+x}\" ]; then", "if false; then"},
		{"host file boundary", "if [ -e \"$SOFA_E2E_HOST_ONLY_FILE\" ]; then", "if false; then"},
		{"artifact sentinel scan", "done < <(find transport candidate -type f -print0)", "done < <(find nowhere -type f -print0)"},
		{"host environment injection", "-e SOFA_E2E_HOST_ONLY_FILE=\"$host_file\"", "-e SOFA_E2E_HOST_ONLY_TOKEN -e SOFA_E2E_HOST_ONLY_FILE=\"$host_file\""},
		{"job permission", "      contents: read\n      actions: read", "      contents: write\n      actions: read"},
		{"recovery scheduler", "if: always() && github.repository", "if: github.repository"},
		{"verified artifact", "name: sofa-e2e-verified-${{ inputs.suite_id }}-${{ github.run_id }}-${{ github.run_attempt }}", "name: sofa-e2e-other-${{ inputs.suite_id }}-${{ github.run_id }}-${{ github.run_attempt }}"},
		{"denial execution gate", "inputs.scenario == 'edit' && inputs.denial_kind == ''", "inputs.scenario == 'denied' && inputs.denial_kind == ''"},
		{"denial scheduler result", "--publish-result \"$SOFA_E2E_PUBLISH_RESULT\"", "--publish-result skipped"},
		{"publication failure insertion", "exit 42", "exit 0"},
		{"publication recovery guard", "inputs.reconcile_candidate && inputs.publish_fault != ''", "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := strings.Replace(string(data), test.old, test.next, 1)
			if mutated == string(data) {
				t.Fatal("mutation did not alter workflow")
			}
			if err := checkFakeWorkflowContract([]byte(mutated)); err == nil {
				t.Fatal("unsafe workflow mutation passed contract")
			}
		})
	}
}
