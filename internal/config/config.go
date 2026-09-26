// Package config decodes trusted, versioned consumer configuration. Issue content
// is deliberately not a configuration source.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

const MaxBytes = 64 << 10

type Config struct {
	Version      int      `yaml:"schema_version" json:"schema_version"`
	Repository   string   `yaml:"repository" json:"repository"`
	RepositoryID string   `yaml:"repository_id" json:"repository_id"`
	ProjectID    string   `yaml:"project_id" json:"project_id"`
	OwnerID      string   `yaml:"owner_id" json:"owner_id"`
	ReadyStatus  string   `yaml:"ready_status" json:"ready_status"`
	AllowedPaths []string `yaml:"allowed_paths" json:"allowed_paths"`
	Profile      Profile  `yaml:"profile" json:"profile"`
	Limits       Limits   `yaml:"limits" json:"limits"`
	Checks       []Check  `yaml:"checks" json:"checks"`
	Recipe       *Recipe  `yaml:"recipe,omitempty" json:"recipe,omitempty"`
}

type Profile struct {
	Agent     string `yaml:"agent" json:"agent"`
	SecretEnv string `yaml:"secret_env" json:"secret_env"`
}

type Limits struct {
	AttemptSeconds int `yaml:"attempt_seconds" json:"attempt_seconds"`
	RepairAttempts int `yaml:"repair_attempts" json:"repair_attempts"`
	InfraRetries   int `yaml:"infra_retries" json:"infra_retries"`
	MaxAgentTurns  int `yaml:"max_agent_turns" json:"max_agent_turns"`
	MaxFiles       int `yaml:"max_files" json:"max_files"`
	MaxFileBytes   int `yaml:"max_file_bytes" json:"max_file_bytes"`
	MaxTotalBytes  int `yaml:"max_total_bytes" json:"max_total_bytes"`
}

type Check struct {
	ID             string   `yaml:"id" json:"id"`
	Argv           []string `yaml:"argv" json:"argv"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type Recipe struct {
	Kind  string   `yaml:"kind" json:"kind"`
	Paths []string `yaml:"paths" json:"paths"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var envPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func Decode(r io.Reader) (Config, error) {
	var c Config
	b, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return c, errors.New("read configuration")
	}
	if len(b) > MaxBytes {
		return c, errors.New("configuration exceeds size limit")
	}
	// Reject aliases before typed decoding: policy has no need for indirection.
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return c, errors.New("invalid configuration YAML")
	}
	var inspect func(*yaml.Node) error
	inspect = func(n *yaml.Node) error {
		if n.Kind == yaml.AliasNode || n.Anchor != "" {
			return errors.New("configuration aliases and anchors are unsupported")
		}
		for _, child := range n.Content {
			if err := inspect(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := inspect(&node); err != nil {
		return c, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return c, errors.New("configuration has invalid, duplicate, or unknown fields")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return Config{}, errors.New("exactly one configuration document is required")
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("unsupported configuration schema_version")
	}
	if !repositoryPattern.MatchString(c.Repository) {
		return errors.New("repository must be owner/name")
	}
	for _, v := range []string{c.RepositoryID, c.ProjectID, c.OwnerID} {
		if v == "" || len(v) > 128 || strings.ContainsAny(v, " \t\r\n\x00") {
			return errors.New("immutable repository, Project, and owner IDs are required")
		}
	}
	if c.ReadyStatus == "" || len(c.ReadyStatus) > 100 || strings.ContainsAny(c.ReadyStatus, "\r\n\x00") {
		return errors.New("ready_status is required")
	}
	if c.Profile.Agent != "copilot" || !envPattern.MatchString(c.Profile.SecretEnv) {
		return errors.New("a copilot profile with a credential environment reference is required")
	}
	// These credentials must never be selected as model authentication.
	for _, reserved := range []string{"SOFA_PROJECTS_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_STATE_TOKEN", "SOFA_APP_PRIVATE_KEY"} {
		if c.Profile.SecretEnv == reserved {
			return errors.New("privileged credential cannot be a model profile")
		}
	}
	l := c.Limits
	if l.AttemptSeconds < 1 || l.AttemptSeconds > 21600 || l.RepairAttempts < 0 || l.RepairAttempts > 10 || l.InfraRetries < 0 || l.InfraRetries > 10 || l.MaxAgentTurns < 1 || l.MaxAgentTurns > 100 {
		return errors.New("finite execution and retry limits are required")
	}
	if l.MaxFiles < 1 || l.MaxFiles > 100 || l.MaxFileBytes < 1 || l.MaxFileBytes > 1<<20 || l.MaxTotalBytes < l.MaxFileBytes || l.MaxTotalBytes > 10<<20 {
		return errors.New("invalid candidate file limits")
	}
	if len(c.AllowedPaths) == 0 || len(c.AllowedPaths) > 100 {
		return errors.New("bounded allowed_paths are required")
	}
	for _, p := range c.AllowedPaths {
		if !SafePath(strings.TrimSuffix(p, "/")) {
			return errors.New("unsafe allowed_paths entry")
		}
	}
	if len(c.Checks) == 0 || len(c.Checks) > 20 {
		return errors.New("at least one bounded deterministic check is required")
	}
	seen := map[string]bool{}
	for _, check := range c.Checks {
		if !idPattern.MatchString(check.ID) || seen[check.ID] || len(check.Argv) == 0 || len(check.Argv) > 32 || check.TimeoutSeconds < 1 || check.TimeoutSeconds > l.AttemptSeconds {
			return errors.New("invalid or duplicate check")
		}
		seen[check.ID] = true
		for _, arg := range check.Argv {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return errors.New("invalid check argument")
			}
		}
		if check.Argv[0] == "" {
			return errors.New("check executable is required")
		}
	}
	if c.Recipe != nil {
		if c.Recipe.Kind != "gofmt" || len(c.Recipe.Paths) == 0 || len(c.Recipe.Paths) > l.MaxFiles {
			return errors.New("unsupported or unbounded recipe")
		}
		for _, p := range c.Recipe.Paths {
			if !SafePath(p) || !strings.HasSuffix(p, ".go") || !c.Allows(p) {
				return errors.New("recipe file is outside its configured Go-file scope")
			}
		}
	}
	return nil
}

// SafePath rejects portable path ambiguities before any filesystem or Git use.
func SafePath(p string) bool {
	if p == "" || p == "." || len(p) > 512 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00\r\n\t*?[]") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return false
		}
		lower := strings.ToLower(part)
		if lower == ".git" || strings.HasPrefix(lower, ".git:") {
			return false
		}
	}
	return true
}

func (c Config) Allows(p string) bool {
	if !SafePath(p) {
		return false
	}
	for _, allowed := range c.AllowedPaths {
		if strings.HasSuffix(allowed, "/") {
			if strings.HasPrefix(p, allowed) {
				return true
			}
		} else if p == allowed {
			return true
		}
	}
	return false
}

func (c Config) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode configuration: %w", err)
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]), nil
}
