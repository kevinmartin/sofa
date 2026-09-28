package managedconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

func decodeRendered(data []byte, out any) error {
	if len(data) == 0 || len(data) > 128<<10 || !bytes.HasPrefix(data, []byte(Marker)) {
		return errors.New("rendered configuration lacks its management marker")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("invalid rendered YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("rendered configuration has multiple YAML documents")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("rendered configuration has trailing YAML content")
	}
	return nil
}

func validateRendered(s Spec, files map[string][]byte) error {
	callerPath := ".github/workflows/sofa.quality.yml"
	if s.Mode == "self" {
		callerPath = ".github/workflows/pr-fast.yml"
	}
	if len(files) != 2 {
		return errors.New("managed output contains unexpected files")
	}
	var caller struct {
		Name string `yaml:"name"`
		On   map[string]struct {
			Types []string `yaml:"types"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Uses        string            `yaml:"uses"`
			Needs       string            `yaml:"needs"`
			If          string            `yaml:"if"`
			RunsOn      string            `yaml:"runs-on"`
			Permissions map[string]string `yaml:"permissions"`
			With        map[string]string `yaml:"with"`
			Steps       []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := decodeRendered(files[callerPath], &caller); err != nil {
		return fmt.Errorf("%s: %w", callerPath, err)
	}
	if len(caller.On) != 1 || !reflect.DeepEqual(caller.On["pull_request"].Types, []string{"opened", "reopened", "synchronize", "ready_for_review", "edited"}) ||
		!reflect.DeepEqual(caller.Permissions, map[string]string{"contents": "read"}) {
		return errors.New("rendered quality caller trigger or permissions changed")
	}
	quality := caller.Jobs["quality"]
	uses := "kevinmartin/sofa/.github/workflows/quality.reusable.yml@v1"
	if s.Mode == "self" {
		uses = "./.github/workflows/quality.reusable.yml"
	}
	if quality.Uses != uses || !reflect.DeepEqual(quality.Permissions, map[string]string{"contents": "read"}) ||
		!reflect.DeepEqual(quality.With, map[string]string{"profiles": strings.Join(s.Profiles, ",")}) {
		return errors.New("rendered quality caller no longer requires its selected profiles")
	}
	if s.Mode == "self" {
		if caller.Name != "Sofa / PR deterministic checks" || len(caller.Jobs) != 2 ||
			!strings.Contains(string(files[callerPath]), "# This workflow name is consumed by the trusted hosted-gate bridge.") {
			return errors.New("sofa caller name or bridge comment changed")
		}
		compat := caller.Jobs["deterministic"]
		if compat.Needs != "quality" || compat.If != "always()" || compat.RunsOn != "ubuntu-24.04" ||
			!reflect.DeepEqual(compat.Permissions, map[string]string{"contents": "read"}) || len(compat.Steps) != 1 ||
			compat.Steps[0].Run != `test "$SOFA_QUALITY_RESULT" = success` ||
			compat.Steps[0].Env["SOFA_QUALITY_RESULT"] != "${{ needs.quality.result }}" {
			return errors.New("rendered compatibility gate changed")
		}
	} else if caller.Name != "Sofa / PR quality" || len(caller.Jobs) != 1 {
		return errors.New("consumer caller name or jobs changed")
	}
	var dependabot struct {
		Version int `yaml:"version"`
		Updates []struct {
			Ecosystem string `yaml:"package-ecosystem"`
			Directory string `yaml:"directory"`
			Schedule  struct {
				Interval string `yaml:"interval"`
				Day      string `yaml:"day"`
				Time     string `yaml:"time"`
				Timezone string `yaml:"timezone"`
			} `yaml:"schedule"`
			Limit  int `yaml:"open-pull-requests-limit"`
			Groups map[string]struct {
				AppliesTo   string   `yaml:"applies-to"`
				Patterns    []string `yaml:"patterns"`
				UpdateTypes []string `yaml:"update-types"`
			} `yaml:"groups"`
		} `yaml:"updates"`
	}
	if err := decodeRendered(files[".github/dependabot.yml"], &dependabot); err != nil {
		return fmt.Errorf("dependabot: %w", err)
	}
	if dependabot.Version != 2 || len(dependabot.Updates) != len(s.Dependabot.Ecosystems) {
		return errors.New("rendered Dependabot updates changed")
	}
	for i, update := range dependabot.Updates {
		group := update.Groups["routine"]
		if update.Ecosystem != s.Dependabot.Ecosystems[i] || update.Directory != "/" ||
			update.Schedule.Interval != "weekly" || update.Schedule.Day != s.Dependabot.Day ||
			update.Schedule.Time != s.Dependabot.Time || update.Schedule.Timezone != s.Dependabot.Timezone ||
			update.Limit != s.Dependabot.Limit || len(update.Groups) != 1 ||
			group.AppliesTo != "version-updates" || !reflect.DeepEqual(group.Patterns, []string{"*"}) ||
			!reflect.DeepEqual(group.UpdateTypes, []string{"minor", "patch"}) {
			return errors.New("rendered Dependabot update differs from its manifest")
		}
	}
	return nil
}

// ValidateActionlint checks the generated workflow itself before CLI output
// or a remote write. The executable is supplied by the trusted operator/CI.
func ValidateActionlint(ctx context.Context, files map[string][]byte) error {
	var workflow []byte
	for path, content := range files {
		if strings.HasPrefix(path, ".github/workflows/") {
			workflow = content
		}
	}
	if len(workflow) == 0 {
		return errors.New("no generated workflow to lint")
	}
	root, err := os.MkdirTemp("", "sofa-managed-actionlint-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	path := filepath.Join(root, ".github", "workflows", "managed.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(path, workflow, 0600); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "actionlint", "-no-color", path)
	output, err := command.CombinedOutput()
	if err != nil {
		if len(output) > 4096 {
			output = output[:4096]
		}
		return fmt.Errorf("generated workflow actionlint failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}
