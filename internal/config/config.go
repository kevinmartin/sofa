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
	"slices"
	"strings"

	"github.com/go-playground/validator/v10"
	"go.yaml.in/yaml/v3"
)

const MaxBytes = 64 << 10

type Config struct {
	Version      int        `yaml:"schema_version" json:"schema_version" validate:"eq=1"`
	Repository   string     `yaml:"repository" json:"repository"`
	RepositoryID string     `yaml:"repository_id" json:"repository_id"`
	ProjectID    string     `yaml:"project_id" json:"project_id"`
	OwnerID      string     `yaml:"owner_id" json:"owner_id"`
	ReadyStatus  string     `yaml:"ready_status" json:"ready_status" validate:"required,max=100"`
	AllowedPaths []string   `yaml:"allowed_paths" json:"allowed_paths" validate:"min=1,max=100"`
	Profile      Profile    `yaml:"profile" json:"profile"`
	Limits       Limits     `yaml:"limits" json:"limits"`
	Checks       []Check    `yaml:"checks" json:"checks" validate:"min=1,max=20,dive"`
	Recipe       *Recipe    `yaml:"recipe,omitempty" json:"recipe,omitempty"`
	Lifecycle    *Lifecycle `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
}

// Lifecycle is opt-in so existing delivery-only consumers retain their v1 policy.
// Statuses maps canonical lifecycle names to this Project's display names.
type Lifecycle struct {
	Statuses          map[string]string `yaml:"statuses" json:"statuses" validate:"len=10"`
	PollMinutes       int               `yaml:"poll_minutes,omitempty" json:"poll_minutes,omitempty" validate:"oneof=0 10 60"`
	DiscoveryWIP      int               `yaml:"discovery_wip,omitempty" json:"discovery_wip,omitempty" validate:"min=0,max=20"`
	DeliveryWIP       int               `yaml:"delivery_wip,omitempty" json:"delivery_wip,omitempty" validate:"min=0,max=20"`
	SpecAuthorID      string            `yaml:"spec_author_id,omitempty" json:"spec_author_id,omitempty"`
	DependenciesField string            `yaml:"dependencies_field,omitempty" json:"dependencies_field,omitempty"`
	PriorityField     string            `yaml:"priority_field,omitempty" json:"priority_field,omitempty"`
	Release           Release           `yaml:"release,omitempty" json:"release,omitempty"`
}

type Release struct {
	RequiredChecks []RequiredCheck `yaml:"required_checks,omitempty" json:"required_checks,omitempty" validate:"max=20,dive"`
}

type RequiredCheck struct {
	Name  string `yaml:"name" json:"name" validate:"required,max=128"`
	AppID int64  `yaml:"app_id" json:"app_id" validate:"gt=0"`
}

var lifecycleStages = []string{"inbox", "discovery", "spec_review", "backlog", "ready", "building", "verification", "review", "release", "done"}

func (l Lifecycle) EffectivePollMinutes() int {
	if l.PollMinutes == 0 {
		return 10
	}
	return l.PollMinutes
}

func (l Lifecycle) EffectiveDiscoveryWIP() int {
	if l.DiscoveryWIP == 0 {
		return 2
	}
	return l.DiscoveryWIP
}

func (l Lifecycle) EffectiveDeliveryWIP() int {
	if l.DeliveryWIP == 0 {
		return 1
	}
	return l.DeliveryWIP
}

type Profile struct {
	Agent     string `yaml:"agent" json:"agent" validate:"eq=copilot"`
	SecretEnv string `yaml:"secret_env" json:"secret_env"`
}

type Limits struct {
	AttemptSeconds int `yaml:"attempt_seconds" json:"attempt_seconds" validate:"min=1,max=21600"`
	RepairAttempts int `yaml:"repair_attempts" json:"repair_attempts" validate:"min=0,max=10"`
	InfraRetries   int `yaml:"infra_retries" json:"infra_retries" validate:"min=0,max=10"`
	MaxAgentTurns  int `yaml:"max_agent_turns" json:"max_agent_turns" validate:"min=1,max=100"`
	MaxFiles       int `yaml:"max_files" json:"max_files" validate:"min=1,max=100"`
	MaxFileBytes   int `yaml:"max_file_bytes" json:"max_file_bytes" validate:"min=1,max=1048576"`
	MaxTotalBytes  int `yaml:"max_total_bytes" json:"max_total_bytes" validate:"max=10485760"`
}

type Check struct {
	ID             string   `yaml:"id" json:"id"`
	Argv           []string `yaml:"argv" json:"argv" validate:"min=1,max=32,dive,max=4096"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type Recipe struct {
	Kind  string   `yaml:"kind" json:"kind" validate:"eq=gofmt"`
	Paths []string `yaml:"paths" json:"paths" validate:"min=1"`
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var envPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var configValidator = validator.New(validator.WithRequiredStructEnabled())

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
	// Static field bounds are declarative; policy involving multiple fields or
	// runtime identities remains explicit below. Never include rejected values.
	if err := configValidator.Struct(c); err != nil {
		var fields validator.ValidationErrors
		if errors.As(err, &fields) && len(fields) > 0 {
			return fmt.Errorf("invalid configuration field %s", fields[0].Namespace())
		}
		return errors.New("invalid configuration field")
	}
	if !repositoryPattern.MatchString(c.Repository) {
		return errors.New("repository must be owner/name")
	}
	for _, v := range []string{c.RepositoryID, c.ProjectID, c.OwnerID} {
		if v == "" || len(v) > 128 || strings.ContainsAny(v, " \t\r\n\x00") {
			return errors.New("immutable repository, Project, and owner IDs are required")
		}
	}
	if len(c.ReadyStatus) > 100 || strings.ContainsAny(c.ReadyStatus, "\r\n\x00") {
		return errors.New("ready_status is required")
	}
	if !envPattern.MatchString(c.Profile.SecretEnv) {
		return errors.New("a copilot profile with a credential environment reference is required")
	}
	// These credentials must never be selected as model authentication.
	if slices.Contains([]string{"SOFA_PROJECTS_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_STATE_TOKEN", "SOFA_APP_PRIVATE_KEY"}, c.Profile.SecretEnv) {
		return errors.New("privileged credential cannot be a model profile")
	}
	l := c.Limits
	if l.MaxTotalBytes < l.MaxFileBytes {
		return errors.New("invalid candidate file limits")
	}
	for _, p := range c.AllowedPaths {
		if !SafePath(strings.TrimSuffix(p, "/")) {
			return errors.New("unsafe allowed_paths entry")
		}
	}
	seen := map[string]bool{}
	for _, check := range c.Checks {
		if !idPattern.MatchString(check.ID) || seen[check.ID] || check.TimeoutSeconds < 1 || check.TimeoutSeconds > l.AttemptSeconds {
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
		if len(c.Recipe.Paths) > l.MaxFiles {
			return errors.New("unsupported or unbounded recipe")
		}
		for _, p := range c.Recipe.Paths {
			if !SafePath(p) || !strings.HasSuffix(p, ".go") || !c.Allows(p) {
				return errors.New("recipe file is outside its configured Go-file scope")
			}
		}
	}
	if c.Lifecycle != nil {
		l := c.Lifecycle
		seenNames := map[string]bool{}
		for _, stage := range lifecycleStages {
			name := l.Statuses[stage]
			if name == "" || len(name) > 100 || strings.ContainsAny(name, "\r\n\x00") || seenNames[name] {
				return errors.New("lifecycle requires distinct, bounded status names")
			}
			seenNames[name] = true
		}
		if l.Statuses["ready"] != c.ReadyStatus {
			return errors.New("lifecycle ready status must match ready_status")
		}
		if l.SpecAuthorID != "" && (len(l.SpecAuthorID) > 128 || strings.ContainsAny(l.SpecAuthorID, " \t\r\n\x00")) {
			return errors.New("invalid specification author identity")
		}
		for _, field := range []string{l.DependenciesField, l.PriorityField} {
			if len(field) > 100 || strings.ContainsAny(field, "\r\n\x00") || field != "" && strings.TrimSpace(field) != field {
				return errors.New("invalid lifecycle owner field name")
			}
		}
		if l.DependenciesField != "" && l.DependenciesField == l.PriorityField {
			return errors.New("lifecycle owner fields must be distinct")
		}
		seenChecks := map[RequiredCheck]bool{}
		for _, check := range l.Release.RequiredChecks {
			if len(check.Name) > 128 || strings.ContainsAny(check.Name, "\r\n\x00") || seenChecks[check] {
				return errors.New("invalid or duplicate release check")
			}
			seenChecks[check] = true
		}
	}
	return nil
}

// SafePath rejects portable path ambiguities before any filesystem or Git use.
func SafePath(p string) bool {
	if p == "" || p == "." || len(p) > 512 || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\:\x00\r\n\t*?[]") {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
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
