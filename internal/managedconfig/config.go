// Package managedconfig renders the small set of repository files owned by sofa.
// Project or issue text never becomes workflow YAML or a repository selector.
package managedconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const Marker = "# Managed by sofa config; edit the source manifest in kevinmartin/sofa.\n"

var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type Spec struct {
	Version    int      `yaml:"version"`
	Repository string   `yaml:"repository"`
	Mode       string   `yaml:"mode"`
	Profiles   []string `yaml:"profiles"`
	Dependabot struct {
		Ecosystems []string `yaml:"ecosystems"`
		Day        string   `yaml:"day"`
		Time       string   `yaml:"time"`
		Timezone   string   `yaml:"timezone"`
		Limit      int      `yaml:"open_pull_requests_limit"`
	} `yaml:"dependabot"`
}

func Load(directory, repository string) (Spec, error) {
	if !repoName.MatchString(repository) || strings.Contains(repository, "..") {
		return Spec{}, errors.New("invalid repository identity")
	}
	path := filepath.Join(directory, strings.ReplaceAll(repository, "/", "--")+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("repository is not enrolled: %w", err)
	}
	if len(data) > 16<<10 {
		return Spec{}, errors.New("repository manifest too large")
	}
	var spec Spec
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("invalid repository manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return Spec{}, errors.New("repository manifest has multiple documents")
	} else if !errors.Is(err, io.EOF) {
		return Spec{}, errors.New("invalid trailing manifest document")
	}
	if spec.Repository != repository {
		return Spec{}, errors.New("repository manifest identity mismatch")
	}
	return spec, spec.Validate()
}

func (s Spec) Validate() error {
	if s.Version != 1 || !repoName.MatchString(s.Repository) || strings.Contains(s.Repository, "..") ||
		(s.Mode != "self" && s.Mode != "consumer") || (s.Mode == "self" && s.Repository != "kevinmartin/sofa") {
		return errors.New("invalid managed repository identity or version")
	}
	if len(s.Profiles) == 0 || len(s.Profiles) > 3 {
		return errors.New("quality profiles are required")
	}
	for i, profile := range s.Profiles {
		if !slices.Contains([]string{"go", "typescript", "react"}, profile) || slices.Contains(s.Profiles[:i], profile) {
			return errors.New("invalid or duplicate quality profile")
		}
	}
	if s.Mode == "self" && (len(s.Profiles) != 1 || s.Profiles[0] != "go") {
		return errors.New("sofa must explicitly require its Go profile")
	}
	if len(s.Dependabot.Ecosystems) == 0 || len(s.Dependabot.Ecosystems) > 3 ||
		s.Dependabot.Limit < 1 || s.Dependabot.Limit > 20 ||
		!slices.Contains([]string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}, s.Dependabot.Day) ||
		!regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`).MatchString(s.Dependabot.Time) {
		return errors.New("invalid Dependabot schedule or limits")
	}
	if _, err := time.LoadLocation(s.Dependabot.Timezone); err != nil {
		return errors.New("invalid Dependabot timezone")
	}
	for i, ecosystem := range s.Dependabot.Ecosystems {
		if !slices.Contains([]string{"gomod", "npm", "github-actions"}, ecosystem) || slices.Contains(s.Dependabot.Ecosystems[:i], ecosystem) {
			return errors.New("invalid or duplicate Dependabot ecosystem")
		}
	}
	if !slices.Contains(s.Dependabot.Ecosystems, "github-actions") ||
		(slices.Contains(s.Profiles, "go") && !slices.Contains(s.Dependabot.Ecosystems, "gomod")) ||
		((slices.Contains(s.Profiles, "typescript") || slices.Contains(s.Profiles, "react")) && !slices.Contains(s.Dependabot.Ecosystems, "npm")) {
		return errors.New("dependabot ecosystems do not cover quality profiles")
	}
	return nil
}

func Render(s Spec) (map[string][]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	var caller strings.Builder
	caller.WriteString(Marker)
	if s.Mode == "self" {
		caller.WriteString("# This workflow name is consumed by the trusted hosted-gate bridge.\n")
		caller.WriteString("name: Sofa / PR deterministic checks\n")
	} else {
		caller.WriteString("name: Sofa / PR quality\n")
	}
	caller.WriteString("\non:\n  pull_request:\n    types: [opened, reopened, synchronize, ready_for_review, edited]\n\npermissions:\n  contents: read\n\njobs:\n")
	if s.Mode == "self" {
		caller.WriteString("  quality:\n    permissions:\n      contents: read\n    uses: ./.github/workflows/quality.reusable.yml\n")
	} else {
		caller.WriteString("  quality:\n    permissions:\n      contents: read\n    uses: kevinmartin/sofa/.github/workflows/quality.reusable.yml@v1\n")
	}
	caller.WriteString("    with:\n      profiles: " + strings.Join(s.Profiles, ",") + "\n")
	if s.Mode == "self" {
		caller.WriteString("  # Temporary compatibility check for the existing branch-protection rule.\n")
		caller.WriteString("  deterministic:\n    needs: quality\n    if: always()\n    runs-on: ubuntu-24.04\n    permissions:\n      contents: read\n    steps:\n      - name: Require shared quality gate\n        env:\n          SOFA_QUALITY_RESULT: ${{ needs.quality.result }}\n        run: test \"$SOFA_QUALITY_RESULT\" = success\n")
	}
	var dependabot strings.Builder
	dependabot.WriteString(Marker + "version: 2\nupdates:\n")
	for index, ecosystem := range s.Dependabot.Ecosystems {
		fmt.Fprintf(&dependabot, "  - package-ecosystem: %s\n    directory: /\n    schedule:\n      interval: weekly\n      day: %s\n      time: %q\n      timezone: %s\n    open-pull-requests-limit: %d\n    groups:\n      routine:\n        applies-to: version-updates\n        patterns:\n          - \"*\"\n", ecosystem, s.Dependabot.Day, s.Dependabot.Time, s.Dependabot.Timezone, s.Dependabot.Limit)
		dependabot.WriteString("        update-types:\n          - minor\n          - patch\n")
		if index != len(s.Dependabot.Ecosystems)-1 {
			dependabot.WriteString("\n")
		}
	}
	callerPath := ".github/workflows/sofa-quality.yml"
	if s.Mode == "self" {
		callerPath = ".github/workflows/pr-fast.yml"
	}
	return map[string][]byte{callerPath: []byte(caller.String()), ".github/dependabot.yml": []byte(dependabot.String())}, nil
}

// Difference refuses to take ownership of a hand-maintained file. A file
// already equal to the desired content is safe to adopt without a write.
func Difference(current, desired []byte) (string, error) {
	if bytes.Equal(current, desired) {
		return "current", nil
	}
	if len(current) != 0 && !bytes.HasPrefix(current, []byte(Marker)) {
		return "", errors.New("existing file is not managed by sofa")
	}
	if len(current) == 0 {
		return "create", nil
	}
	return "update", nil
}
