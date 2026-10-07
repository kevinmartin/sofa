package workflow

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func checkCLISelectionConditions(job contractJob) error {
	const sourceMode = "inputs.version != '' && !startsWith(inputs.version, 'v')"
	const releaseMode = "inputs.version == '' || startsWith(inputs.version, 'v')"
	for _, step := range job.Steps {
		expected := ""
		switch {
		case strings.HasPrefix(step.Name, "Validate exact source selector"),
			step.Name == "Check out exact source installer without credentials",
			step.Name == "Set up source Sofa CLIs":
			expected = sourceMode
		case step.Name == "Set up released Sofa CLIs":
			expected = releaseMode
		default:
			continue
		}
		if step.If != expected {
			return fmt.Errorf("%s: CLI selection condition allows unvalidated source or overlapping installers", step.Name)
		}
	}
	return nil
}

func TestCLISelectionRejectsChangedConditions(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/work.reusable.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		step      string
		condition string
	}{
		{name: "validation misses source mode", step: "Validate exact source selector before loading its installer", condition: "inputs.version == 'v0'"},
		{name: "checkout is unconditional", step: "Check out exact source installer without credentials", condition: ""},
		{name: "source installer is widened", step: "Set up source Sofa CLIs", condition: "always()"},
		{name: "release overlaps source", step: "Set up released Sofa CLIs", condition: "inputs.version != ''"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, err := parseContractWorkflow(data)
			if err != nil {
				t.Fatal(err)
			}
			mutated := 0
			for name, job := range w.Jobs {
				if err := checkCLISelectionConditions(job); err != nil {
					t.Fatalf("valid %s: %v", name, err)
				}
				for i := range job.Steps {
					if job.Steps[i].Name != tc.step {
						continue
					}
					job.Steps[i].If = tc.condition
					mutated++
					if err := checkCLISelectionConditions(job); err == nil {
						t.Fatalf("%s accepted %s", name, tc.name)
					}
				}
			}
			if mutated == 0 {
				t.Fatal("mutation did not reach a selector step")
			}
		})
	}
}
