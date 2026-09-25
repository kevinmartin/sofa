package workflow

import (
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The hosted fake workflow is a secretless surrogate. Keep its shared scheduler
// and artifact edges aligned with work.yml, while pinning the differences that
// prevent this test from being mistaken for a run of the production workflow.
func checkHostedGraphParity(reconcileData, workData, fakeData []byte) error {
	reconcile, err := parseContractWorkflow(reconcileData)
	if err != nil {
		return err
	}
	work, err := parseContractWorkflow(workData)
	if err != nil {
		return err
	}
	fake, err := parseContractWorkflow(fakeData)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(jobNames(work), []string{"execute", "finalize-failure", "publish", "verify"}) ||
		!reflect.DeepEqual(jobNames(fake), []string{"assert-denied", "execute", "publish", "verify"}) ||
		!reflect.DeepEqual(jobNames(reconcile), []string{"admit"}) {
		return fmt.Errorf("production, admission, or fake job inventory changed")
	}
	for _, w := range []contractWorkflow{work, fake} {
		verify, publish := w.Jobs["verify"], w.Jobs["publish"]
		if verify.Needs != "execute" || publish.Needs != "verify" ||
			!strings.Contains(verify.If, "always()") ||
			!strings.Contains(verify.If, "needs.execute.result == 'success'") ||
			!strings.Contains(verify.If, "needs.execute.result == 'skipped' && inputs.reconcile_candidate") ||
			!strings.Contains(publish.If, "always() &&") ||
			!strings.Contains(publish.If, "needs.verify.result == 'success'") {
			return fmt.Errorf("shared execute/verify/publish recovery graph diverged")
		}
	}
	for _, edge := range []struct {
		producer, consumer contractJob
		name               string
	}{
		{reconcile.Jobs["admit"], work.Jobs["execute"], "sofa-manifest-"},
		{work.Jobs["execute"], work.Jobs["verify"], "sofa-candidate-"},
		{work.Jobs["verify"], work.Jobs["publish"], "sofa-evidence-"},
		{work.Jobs["verify"], work.Jobs["publish"], "sofa-verified-candidate-"},
		{fake.Jobs["execute"], fake.Jobs["verify"], "sofa-e2e-source-"},
		{fake.Jobs["execute"], fake.Jobs["verify"], "sofa-e2e-candidate-"},
		{fake.Jobs["verify"], fake.Jobs["publish"], "sofa-e2e-verified-"},
	} {
		produced := artifactName(edge.producer, "actions/upload-artifact", edge.name)
		if produced == "" || produced != artifactName(edge.consumer, "actions/download-artifact", edge.name) {
			return fmt.Errorf("artifact handoff %s diverged", edge.name)
		}
	}
	for _, w := range []contractWorkflow{work, fake} {
		if !jobUploadsPath(w.Jobs["execute"], "candidate/bundle.json") ||
			!jobUploadsPath(w.Jobs["verify"], "candidate/bundle.json") ||
			!jobUploadsPath(w.Jobs["verify"], "evidence/checks.json") {
			return fmt.Errorf("candidate or evidence payload no longer crosses the shared graph")
		}
	}

	// These are intentional differences, not equivalent production coverage.
	if !jobUploadsPath(reconcile.Jobs["admit"], "transport/manifest.json") ||
		!jobUploadsPath(fake.Jobs["execute"], "transport/manifest.json") ||
		!strings.Contains(string(fakeData), "bin/e2e-fixture prepare") ||
		!strings.Contains(work.Jobs["execute"].If, "github.event.repository.default_branch") ||
		!strings.Contains(fake.Jobs["execute"].If, "refs/heads/sofa-e2e/") ||
		work.Jobs["execute"].Permissions["copilot-requests"] != "write" ||
		fake.Jobs["execute"].Permissions["copilot-requests"] != "" ||
		!strings.Contains(string(workData), "${{ secrets.SOFA_PUBLISH_TOKEN }}") ||
		strings.Contains(string(fakeData), "${{ secrets.") ||
		!strings.Contains(string(workData), "./sofa publish") ||
		!strings.Contains(string(fakeData), "TestHostedArtifactPublication") ||
		!strings.Contains(string(fakeData), "--network none") {
		return fmt.Errorf("an intentional production/fake trust or source difference changed")
	}
	return nil
}

func jobNames(w contractWorkflow) []string {
	names := make([]string, 0, len(w.Jobs))
	for name := range w.Jobs {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func jobUploadsPath(job contractJob, path string) bool {
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			for _, candidate := range strings.Fields(step.With["path"]) {
				if candidate == path {
					return true
				}
			}
		}
	}
	return false
}

func TestHostedGraphParityAndBoundaries(t *testing.T) {
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	reconcile := read("../../.github/workflows/reconcile.yml")
	work := read("../../.github/workflows/work.yml")
	fake := read("../../.github/workflows/e2e-fake.yml")
	if err := checkHostedGraphParity(reconcile, work, fake); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, old, next string }{
		{"recovery edge", "needs: verify", "needs: execute"},
		{"verified payload", "            evidence/checks.json", "            evidence/other.json"},
		{"secretless simulation", "TestHostedArtifactPublication", "TestRealPublication"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := strings.Replace(string(fake), test.old, test.next, 1)
			if mutated == string(fake) {
				t.Fatal("mutation did not alter fake workflow")
			}
			if err := checkHostedGraphParity(reconcile, work, []byte(mutated)); err == nil {
				t.Fatal("drifted graph passed parity check")
			}
		})
	}
}
