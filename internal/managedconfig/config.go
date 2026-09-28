// Package managedconfig renders the small set of repository files owned by sofa.
// Project or issue text never becomes workflow YAML or a repository selector.
package managedconfig

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

const Marker = "# Managed by sofa config; edit the source manifest in kevinmartin/sofa.\n"

var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

//go:embed templates/*.tmpl
var templates embed.FS

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
	view := struct {
		QualityResultExpression string
		Profiles                string
		Ecosystems              []string
		Day                     string
		Time                    string
		Timezone                string
		Limit                   int
	}{
		Profiles:                strings.Join(s.Profiles, ","),
		Ecosystems:              s.Dependabot.Ecosystems,
		Day:                     s.Dependabot.Day,
		Time:                    s.Dependabot.Time,
		Timezone:                s.Dependabot.Timezone,
		Limit:                   s.Dependabot.Limit,
		QualityResultExpression: "${{ needs.quality.result }}",
	}
	render := func(name string) ([]byte, error) {
		parsed, err := template.New(filepath.Base(name)).Option("missingkey=error").ParseFS(templates, name)
		if err != nil {
			return nil, fmt.Errorf("parse managed template %s: %w", name, err)
		}
		var output bytes.Buffer
		if err := parsed.Execute(&output, view); err != nil {
			return nil, fmt.Errorf("render managed template %s: %w", name, err)
		}
		return output.Bytes(), nil
	}
	callerPath := ".github/workflows/sofa.quality.yml"
	callerTemplate := "templates/sofa.quality.yml.tmpl"
	if s.Mode == "self" {
		callerPath = ".github/workflows/pr-fast.yml"
		callerTemplate = "templates/pr-fast.yml.tmpl"
	}
	caller, err := render(callerTemplate)
	if err != nil {
		return nil, err
	}
	dependabot, err := render("templates/dependabot.yml.tmpl")
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{
		callerPath:               caller,
		".github/dependabot.yml": dependabot,
	}
	if err := validateRendered(s, files); err != nil {
		return nil, err
	}
	return files, nil
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
