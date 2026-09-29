package workflow

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

var actionPinLine = regexp.MustCompile(`^([ \t]*(?:- )?uses: [A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)@[0-9a-f]{40} # v[0-9]+\.[0-9]+\.[0-9]+\n?$`)

// CheckSofaQualityContract compares PR workflows with their trusted main-branch
// base. Existing action pins may change; all other workflow bytes must match.
func CheckSofaQualityContract(caller, reusable, baseCaller, baseReusable []byte) error {
	if err := CheckSofaQualityContractStructure(caller, reusable); err != nil {
		return err
	}
	if len(baseCaller) == 0 || len(baseReusable) == 0 || !bytes.Equal(caller, baseCaller) ||
		!sameExceptActionPins(baseReusable, reusable) {
		return errors.New("candidate quality workflow changed beyond action pins")
	}
	return nil
}

func sameExceptActionPins(base, candidate []byte) bool {
	if len(base) == 0 || len(base) > 128<<10 || len(candidate) == 0 || len(candidate) > 128<<10 {
		return false
	}
	before, after := strings.SplitAfter(string(base), "\n"), strings.SplitAfter(string(candidate), "\n")
	if len(before) != len(after) {
		return false
	}
	for i, line := range before {
		if line == after[i] {
			continue
		}
		old, next := actionPinLine.FindStringSubmatch(line), actionPinLine.FindStringSubmatch(after[i])
		if old == nil || next == nil || old[1] != next[1] || strings.HasSuffix(line, "\n") != strings.HasSuffix(after[i], "\n") {
			return false
		}
	}
	return true
}

// CheckSofaQualityContractStructure rejects weakened candidate workflows
// before the bridge spends API calls fetching their trusted base versions.
func CheckSofaQualityContractStructure(caller, reusable []byte) error {
	type step struct {
		Name            string            `yaml:"name"`
		Uses            string            `yaml:"uses"`
		If              string            `yaml:"if"`
		Run             string            `yaml:"run"`
		Env             map[string]string `yaml:"env"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	type job struct {
		Uses            string            `yaml:"uses"`
		With            map[string]string `yaml:"with"`
		Permissions     map[string]string `yaml:"permissions"`
		RunsOn          string            `yaml:"runs-on"`
		If              string            `yaml:"if"`
		Needs           any               `yaml:"needs"`
		Steps           []step            `yaml:"steps"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	type document struct {
		Name        string            `yaml:"name"`
		On          map[string]any    `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]job    `yaml:"jobs"`
	}
	parse := func(data []byte) (document, error) {
		var d document
		if len(data) == 0 || len(data) > 128<<10 || yaml.Unmarshal(data, &d) != nil {
			return d, errors.New("invalid quality workflow")
		}
		if strings.Contains(string(data), "${{ secrets.") || strings.Contains(string(data), "secrets: inherit") {
			return d, errors.New("quality workflow requests secrets")
		}
		if len(d.Permissions) != 1 || d.Permissions["contents"] != "read" {
			return d, errors.New("quality workflow permissions are not read-only")
		}
		for _, job := range d.Jobs {
			if job.ContinueOnError != nil {
				return d, errors.New("quality job cannot continue after an error")
			}
			for _, step := range job.Steps {
				if step.ContinueOnError != nil {
					return d, errors.New("quality step cannot continue after an error")
				}
			}
		}
		return d, nil
	}
	c, err := parse(caller)
	if err != nil {
		return err
	}
	if c.Name != "Sofa / PR deterministic checks" || len(c.On) != 1 || c.On["pull_request"] == nil ||
		(len(c.Jobs) != 1 && len(c.Jobs) != 2) {
		return errors.New("sofa PR quality trigger changed")
	}
	call := c.Jobs["quality"]
	if call.Uses != "./.github/workflows/quality.reusable.yml" || call.If != "" || len(call.With) != 1 || call.With["profiles"] != "go" || len(call.Permissions) != 1 || call.Permissions["contents"] != "read" {
		return errors.New("sofa no longer requires its local Go quality workflow")
	}
	if len(c.Jobs) == 2 {
		compat := c.Jobs["deterministic"]
		if compat.Needs != "quality" || compat.If != "always()" || compat.RunsOn != "ubuntu-24.04" ||
			len(compat.Permissions) != 1 || compat.Permissions["contents"] != "read" || len(compat.Steps) != 1 ||
			compat.Steps[0].Run != `test "$SOFA_QUALITY_RESULT" = success` ||
			compat.Steps[0].Env["SOFA_QUALITY_RESULT"] != "${{ needs.quality.result }}" {
			return errors.New("required compatibility check no longer follows shared quality")
		}
	}
	r, err := parse(reusable)
	if err != nil {
		return err
	}
	if r.On["workflow_call"] == nil || len(r.On) != 1 || len(r.Jobs) != 6 {
		return errors.New("quality workflow-call contract changed")
	}
	pinnedAction := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$`)
	steps := make(map[string]step)
	for id, job := range r.Jobs {
		if job.RunsOn != "ubuntu-24.04" || (id != "result" && (len(job.Permissions) != 1 || job.Permissions["contents"] != "read")) ||
			(id == "result" && len(job.Permissions) != 0) {
			return fmt.Errorf("quality job %q runner or permissions changed", id)
		}
		for _, s := range job.Steps {
			key := id + "/" + s.Name
			if s.Name == "" || steps[key].Name != "" {
				return errors.New("quality steps are missing names or duplicated")
			}
			steps[key] = s
			if s.Uses != "" && !pinnedAction.MatchString(s.Uses) {
				return fmt.Errorf("quality action %q is not pinned", s.Name)
			}
		}
	}
	if strings.Count(string(reusable), "persist-credentials: false") != 5 ||
		steps["select/Check out candidate without credentials"].Uses == "" {
		return errors.New("quality checkout credentials changed")
	}
	for name, command := range map[string]string{
		"autodetect/Detect quality profiles":                "if not selected:",
		"select/Select and validate quality profiles":       "if len(parts) != len(selected) or any(p not in {'go', 'typescript', 'react'} for p in parts):",
		"go/Check Go formatting and module files":           "go mod tidy -diff",
		"github-actions/Lint workflows and embedded shell":  "actionlint",
		"typescript-react/Install locked Node dependencies": "npm) npm ci --ignore-scripts ;;",
		"typescript-react/Check TypeScript and React":       "for script in format:check lint typecheck test; do",
		"result/Require every selected quality job":         "[[ \"$SOFA_SELECT_RESULT\" == success && \"$SOFA_ACTIONS_RESULT\" == success ]]",
	} {
		if !commandLine(steps[name].Run, command) {
			return fmt.Errorf("quality validator %q is missing", name)
		}
	}
	goSteps := r.Jobs["go"].Steps
	if len(goSteps) != 7 ||
		steps["go/Vet Go packages"].Run != "go vet ./..." ||
		steps["go/Run Staticcheck"].Run != "staticcheck ./..." ||
		goSteps[len(goSteps)-1].Name != "Test Go packages" ||
		goSteps[len(goSteps)-1].Run != "go test -count=1 ./..." {
		return errors.New("go validators must be separate with tests last")
	}
	if r.Jobs["autodetect"].If != "inputs.profiles == '' || inputs.profiles == 'auto'" ||
		r.Jobs["select"].If != "always()" || r.Jobs["go"].If != "always() && needs.select.result == 'success' && needs.select.outputs.go == 'true'" ||
		r.Jobs["typescript-react"].If != "always() && needs.select.result == 'success' && needs.select.outputs.node == 'true'" ||
		r.Jobs["github-actions"].If != "" || r.Jobs["result"].If != "always()" {
		return errors.New("quality profile execution can be skipped")
	}
	return nil
}

func commandLine(script, command string) bool {
	for line := range strings.SplitSeq(script, "\n") {
		if strings.TrimSpace(line) == command {
			return true
		}
	}
	return false
}
