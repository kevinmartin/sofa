package workflow

import (
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// These tests check the copyable examples in the distribution plan. They do
// not establish that the planned installer or release selection is implemented.
func distributionExamples(t *testing.T) []distributionExample {
	t.Helper()
	data, err := os.ReadFile("../../docs/milestones/02.1-versioned-distribution.md")
	if err != nil {
		t.Fatal(err)
	}
	var examples []distributionExample
	remaining := string(data)
	for {
		_, body, found := strings.Cut(remaining, "\n```yaml\n")
		if !found {
			break
		}
		block, rest, closed := strings.Cut(body, "\n```\n")
		if !closed {
			t.Fatal("distribution example has an unclosed YAML fence")
		}
		var example distributionExample
		if err := yaml.Unmarshal([]byte(block), &example); err != nil {
			t.Fatalf("parse distribution example %d: %v", len(examples)+1, err)
		}
		examples = append(examples, example)
		remaining = rest
	}
	if len(examples) != 2 {
		t.Fatalf("got %d YAML examples, want automatic selection and version handoff", len(examples))
	}
	return examples
}

type distributionExample struct {
	Jobs map[string]struct {
		Uses    string            `yaml:"uses"`
		Needs   string            `yaml:"needs"`
		RunsOn  string            `yaml:"runs-on"`
		With    map[string]string `yaml:"with"`
		Secrets map[string]string `yaml:"secrets"`
		Outputs map[string]string `yaml:"outputs"`
		Steps   []struct {
			ID   string            `yaml:"id"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func TestDistributionExampleAutomaticSelection(t *testing.T) {
	example := distributionExamples(t)[0]
	job, ok := example.Jobs["lifecycle"]
	if !ok || len(example.Jobs) != 1 {
		t.Fatal("automatic selection example must call lifecycle without a preparation job")
	}
	if want := "kevinmartin/sofa/.github/workflows/lifecycle.reusable.yml@v0"; job.Uses != want {
		t.Errorf("lifecycle uses = %q, want %q", job.Uses, want)
	}
	if job.RunsOn != "" || len(job.Steps) != 0 || job.Needs != "" {
		t.Error("reusable lifecycle call must not require a runner, steps, or another job")
	}
	for _, input := range []string{"toolkit_sha", "release_version"} {
		if value, exists := job.With[input]; exists {
			t.Errorf("automatic selection must omit %s, got %q", input, value)
		}
	}
	if got, want := job.Secrets["SOFA_PROJECTS_TOKEN"], "${{ secrets.SOFA_PROJECTS_TOKEN }}"; got != want {
		t.Errorf("Project credential forwarding = %q, want %q", got, want)
	}
}

func TestDistributionExampleVersionHandoff(t *testing.T) {
	example := distributionExamples(t)[1]
	if len(example.Jobs) != 2 {
		t.Fatalf("version handoff has %d jobs, want a producer and a consumer", len(example.Jobs))
	}
	for _, test := range []struct {
		job     string
		command string
	}{
		{
			job:     "first",
			command: "sofa --version",
		},
		{
			job:     "next",
			command: "sofa-test --version",
		},
	} {
		t.Run(test.job, func(t *testing.T) {
			job, ok := example.Jobs[test.job]
			if !ok || job.RunsOn == "" || job.Uses != "" {
				t.Fatal("direct Action example must define a job with its own runner")
			}
			if len(job.Steps) != 2 {
				t.Fatalf("got %d steps, want installation followed by CLI use", len(job.Steps))
			}
			setup := job.Steps[0]
			if want := "kevinmartin/sofa/.github/actions/setup-cli@v0"; setup.Uses != want || setup.Run != "" {
				t.Errorf("first step must use %q without a run command", want)
			}
			if got := job.Steps[1]; got.Run != test.command || got.Uses != "" {
				t.Errorf("step after installation must run %q", test.command)
			}
			if _, exists := setup.With["toolkit_sha"]; exists {
				t.Error("released installation must not request a candidate source build")
			}
			if test.job == "first" {
				if job.Needs != "" {
					t.Error("producer must run independently of the consumer")
				}
				if _, exists := setup.With["release_version"]; exists {
					t.Error("producer must select the promoted release automatically")
				}
				if setup.ID == "" {
					t.Fatal("producer setup step needs an ID to expose its selected version")
				}
				if got, want := job.Outputs["release_version"], "${{ steps."+setup.ID+".outputs.release_version }}"; got != want {
					t.Errorf("producer output = %q, want %q", got, want)
				}
			} else {
				if job.Needs != "first" {
					t.Errorf("consumer needs = %q, want first", job.Needs)
				}
				if got, want := setup.With["release_version"], "${{ needs.first.outputs.release_version }}"; got != want {
					t.Errorf("consumer release_version = %q, want producer output %q", got, want)
				}
			}
		})
	}
}
