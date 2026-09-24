package workflow

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// A recovered candidate intentionally skips execute. GitHub Actions adds an
// implicit success() to job conditions that do not contain a status function,
// so publish must opt out of that implicit check after verify succeeds.
func TestRecoveredCandidateCanReachPublisher(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/work.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs any    `yaml:"needs"`
			If    string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	verify, verifyOK := workflow.Jobs["verify"]
	publish, publishOK := workflow.Jobs["publish"]
	if !verifyOK || !publishOK || verify.Needs != "execute" || publish.Needs != "verify" {
		t.Fatal("recovery job chain changed; review intentional execute skip")
	}
	if !strings.Contains(verify.If, "always()") || !strings.Contains(publish.If, "always()") || !strings.Contains(publish.If, "needs.verify.result == 'success'") {
		t.Fatal("recovered candidate cannot pass an intentionally skipped execution job to publication")
	}
}

func TestFailedVerificationReachesFailureFinalizer(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/work.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Needs any    `yaml:"needs"`
			If    string `yaml:"if"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	finalizer, ok := workflow.Jobs["finalize-failure"]
	if !ok {
		t.Fatal("failure finalizer missing")
	}
	needs, ok := finalizer.Needs.([]any)
	if !ok || len(needs) != 2 || needs[0] != "execute" || needs[1] != "verify" {
		t.Fatal("failure finalizer does not wait for both work stages")
	}
	if !strings.Contains(finalizer.If, "always()") || !strings.Contains(finalizer.If, "needs.verify.result == 'failure'") {
		t.Fatal("failed verification cannot reach failure finalizer")
	}
}
