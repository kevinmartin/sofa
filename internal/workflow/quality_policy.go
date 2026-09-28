package workflow

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// CheckSofaQualityContract runs on trusted default-branch code against workflow
// files fetched from a PR as data. It guards the minimum independent PR gate;
// a change to this policy itself still requires ordinary owner review.
func CheckSofaQualityContract(caller, reusable []byte) error {
	// These digests are part of trusted default-branch code, not read from a PR.
	// To change a gate, first merge a Kevin-reviewed policy change that adds
	// its proposed digest; only then can the workflow PR pass this status.
	approvedCaller := map[string]bool{
		"5f21aca5983e0ed8dbeca8747b5e11d0503033d98e605f65ff193e28dcb73c51": true,
	}
	approvedReusable := map[string]bool{
		"5e94d8e0776fdcc65022bbe5d7e0f76cfa7ef0ddb4d48636c3ec4b51defe76f5": true,
	}
	if !approvedCaller[fmt.Sprintf("%x", sha256.Sum256(caller))] || !approvedReusable[fmt.Sprintf("%x", sha256.Sum256(reusable))] {
		return errors.New("candidate quality gate digest lacks prior trusted approval")
	}
	type step struct {
		Name string            `yaml:"name"`
		Uses string            `yaml:"uses"`
		If   string            `yaml:"if"`
		Run  string            `yaml:"run"`
		Env  map[string]string `yaml:"env"`
	}
	type job struct {
		Uses        string            `yaml:"uses"`
		With        map[string]string `yaml:"with"`
		Permissions map[string]string `yaml:"permissions"`
		RunsOn      string            `yaml:"runs-on"`
		If          string            `yaml:"if"`
		Needs       string            `yaml:"needs"`
		Steps       []step            `yaml:"steps"`
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
		return d, nil
	}
	c, err := parse(caller)
	if err != nil {
		return err
	}
	if c.Name != "Sofa / PR deterministic checks" || len(c.On) != 1 || c.On["pull_request"] == nil || len(c.Jobs) != 2 {
		return errors.New("sofa PR quality trigger changed")
	}
	call := c.Jobs["quality"]
	if call.Uses != "./.github/workflows/quality.reusable.yml" || call.If != "" || len(call.With) != 1 || call.With["profiles"] != "go" || len(call.Permissions) != 1 || call.Permissions["contents"] != "read" {
		return errors.New("sofa no longer requires its local Go quality workflow")
	}
	compat := c.Jobs["deterministic"]
	if compat.Needs != "quality" || compat.If != "always()" || compat.RunsOn != "ubuntu-24.04" ||
		len(compat.Permissions) != 1 || compat.Permissions["contents"] != "read" || len(compat.Steps) != 1 ||
		compat.Steps[0].Run != `test "$SOFA_QUALITY_RESULT" = success` ||
		compat.Steps[0].Env["SOFA_QUALITY_RESULT"] != "${{ needs.quality.result }}" {
		return errors.New("required compatibility check no longer follows shared quality")
	}
	r, err := parse(reusable)
	if err != nil {
		return err
	}
	if r.On["workflow_call"] == nil || len(r.On) != 1 || len(r.Jobs) != 1 {
		return errors.New("quality workflow-call contract changed")
	}
	checks := r.Jobs["checks"]
	if checks.RunsOn != "ubuntu-24.04" || checks.If != "" || len(checks.Permissions) != 1 || checks.Permissions["contents"] != "read" {
		return errors.New("quality job runner or permissions changed")
	}
	steps := make(map[string]step, len(checks.Steps))
	pinnedAction := regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$`)
	for _, s := range checks.Steps {
		if s.Name == "" || steps[s.Name].Name != "" {
			return errors.New("quality steps are missing names or duplicated")
		}
		steps[s.Name] = s
		if s.Uses != "" && !pinnedAction.MatchString(s.Uses) {
			return fmt.Errorf("quality action %q is not pinned", s.Name)
		}
	}
	if !strings.Contains(steps["Check out candidate without credentials"].Run+string(reusable), "persist-credentials: false") ||
		steps["Check out candidate without credentials"].Uses == "" {
		return errors.New("quality checkout credentials changed")
	}
	for name, command := range map[string]string{
		"Select and validate quality profiles": "if not selected:",
		"Check Go formatting and module files": "go mod tidy -diff",
		"Lint workflows and embedded shell":    "actionlint",
		"Run Go tests, vet, and Staticcheck":   "go test -count=1 ./...",
		"Install locked Node dependencies":     "npm) npm ci --ignore-scripts ;;",
		"Check TypeScript and React":           "for script in format:check lint typecheck test; do",
	} {
		if !commandLine(steps[name].Run, command) {
			return fmt.Errorf("quality validator %q is missing", name)
		}
	}
	for _, command := range []string{"go vet ./...", "staticcheck ./..."} {
		if !commandLine(steps["Run Go tests, vet, and Staticcheck"].Run, command) {
			return fmt.Errorf("quality validator %q is missing", command)
		}
	}
	if steps["Run Go tests, vet, and Staticcheck"].If != "env.SOFA_GO == 'true'" ||
		steps["Check TypeScript and React"].If != "env.SOFA_NODE == 'true'" ||
		steps["Select and validate quality profiles"].If != "" {
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
