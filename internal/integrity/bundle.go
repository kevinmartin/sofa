// Package integrity validates untrusted candidate data without executing it.
// Validation is necessary but does not establish artifact provenance: callers
// must fetch the bundle and check evidence from the expected trusted run/job.
package integrity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
)

const Version = 1
const RegularMode = "100644"
const MaxEncodedBytes int64 = 16 << 20

type Bundle struct {
	Version         int    `json:"version"`
	Repository      string `json:"repository"`
	AttemptID       string `json:"attempt_id"`
	Generation      uint64 `json:"generation"`
	BaseSHA         string `json:"base_sha"`
	CandidateDigest string `json:"candidate_digest"`
	Files           []File `json:"files"`
}

// Files transport complete regular-file contents, never shell patches. Delete
// and update require the SHA256 of the exact preimage at BaseSHA.
type File struct {
	Path         string `json:"path"`
	Operation    string `json:"operation"`
	Mode         string `json:"mode"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	Content      []byte `json:"content,omitempty"`
}

type Expected struct {
	Repository      string
	AttemptID       string
	Generation      uint64
	BaseSHA         string
	CandidateDigest string
}

// AllowedPaths are exact files or directory prefixes ending in '/'. Globs
// are intentionally unsupported. Limits can only narrow the hard ceilings.
type Policy struct {
	AllowedPaths    []string
	MaxFiles        int
	MaxFileBytes    int
	MaxTotalBytes   int
	ForbiddenValues [][]byte
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var repository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var sha = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var safeChars = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
var deviceName = regexp.MustCompile(`^(com|lpt)[0-9]$`)

func Hash(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// Digest binds identity, fence generation, base and every file field. File
// order is canonicalized, so JSON whitespace/ordering cannot alter identity.
func Digest(b Bundle) (string, error) {
	b.CandidateDigest = ""
	b.Files = append([]File(nil), b.Files...)
	sort.Slice(b.Files, func(i, j int) bool { return b.Files[i].Path < b.Files[j].Path })
	data, err := json.Marshal(b)
	if err != nil {
		return "", errors.New("cannot encode candidate")
	}
	return Hash(data), nil
}

func Seal(b *Bundle) error {
	d, err := Digest(*b)
	if err != nil {
		return err
	}
	b.CandidateDigest = d
	return nil
}

// Decode bounds allocation, rejects unknown fields, duplicate JSON keys and
// trailing values. Validate must still be called before using the result.
func Decode(r io.Reader, maxBytes int64) (Bundle, error) {
	var b Bundle
	if maxBytes <= 0 || maxBytes > MaxEncodedBytes {
		return b, errors.New("invalid artifact size limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return b, errors.New("candidate artifact exceeds limit or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(d); err != nil {
		return b, errors.New("malformed candidate artifact")
	}
	if _, err := d.Token(); err != io.EOF {
		return b, errors.New("trailing candidate data")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&b); err != nil {
		return Bundle{}, errors.New("malformed candidate artifact")
	}
	return b, nil
}

func uniqueJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	v, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch v {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			k, ok := key.(string)
			if !ok || seen[k] {
				return errors.New("duplicate key")
			}
			seen[k] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}

func Validate(b Bundle, e Expected, p Policy) error {
	if b.Version != Version || !repository.MatchString(b.Repository) || !identifier.MatchString(b.AttemptID) || b.Generation == 0 || !sha.MatchString(b.BaseSHA) {
		return errors.New("invalid candidate identity or version")
	}
	if b.Repository != e.Repository || b.AttemptID != e.AttemptID || b.Generation != e.Generation || b.BaseSHA != e.BaseSHA || !digest.MatchString(e.CandidateDigest) || b.CandidateDigest != e.CandidateDigest {
		return errors.New("candidate does not match expected identity")
	}
	d, err := Digest(b)
	if err != nil || d != b.CandidateDigest {
		return errors.New("candidate digest mismatch")
	}
	maxFiles, err := limit(p.MaxFiles, 100)
	if err != nil {
		return err
	}
	maxFile, err := limit(p.MaxFileBytes, 1<<20)
	if err != nil {
		return err
	}
	maxTotal, err := limit(p.MaxTotalBytes, 10<<20)
	if err != nil {
		return err
	}
	if len(p.AllowedPaths) == 0 {
		return errors.New("empty path allowlist")
	}
	for _, rule := range p.AllowedPaths {
		if err := SafePath(strings.TrimSuffix(rule, "/")); err != nil {
			return errors.New("invalid path allowlist")
		}
	}
	if len(b.Files) == 0 || len(b.Files) > maxFiles {
		return errors.New("invalid candidate file count")
	}
	seen := map[string]bool{}
	total := 0
	for _, f := range b.Files {
		if err := SafePath(f.Path); err != nil {
			return err
		}
		key := strings.ToLower(f.Path)
		if seen[key] {
			return errors.New("duplicate candidate path")
		}
		seen[key] = true
		allowed := false
		for _, rule := range p.AllowedPaths {
			if f.Path == rule || (strings.HasSuffix(rule, "/") && strings.HasPrefix(f.Path, rule)) {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("candidate path outside allowlist")
		}
		if f.Mode != RegularMode {
			return errors.New("unsupported candidate file mode")
		}
		switch f.Operation {
		case "add":
			if f.BeforeSHA256 != "" {
				return errors.New("add contains preimage")
			}
		case "update":
			if !digest.MatchString(f.BeforeSHA256) || f.BeforeSHA256 == Hash(f.Content) {
				return errors.New("invalid or unchanged update preimage")
			}
		case "delete":
			if !digest.MatchString(f.BeforeSHA256) || len(f.Content) != 0 {
				return errors.New("invalid delete")
			}
		default:
			return errors.New("unsupported candidate operation")
		}
		if len(f.Content) > maxFile {
			return errors.New("candidate file exceeds limit")
		}
		total += len(f.Content)
		if total > maxTotal {
			return errors.New("candidate exceeds total limit")
		}
		if err := ScanSecrets(f.Content, p.ForbiddenValues); err != nil {
			return err
		}
		if err := ScanSecrets([]byte(f.Path), p.ForbiddenValues); err != nil {
			return err
		}
	}
	// A file cannot also serve as another file's directory, including casefold
	// aliases on macOS/Windows consumer checkouts.
	for key := range seen {
		for parent := path.Dir(key); parent != "."; parent = path.Dir(parent) {
			if seen[parent] {
				return errors.New("candidate file and directory conflict")
			}
		}
	}
	return nil
}

func limit(value, ceiling int) (int, error) {
	if value == 0 {
		return ceiling, nil
	}
	if value < 0 || value > ceiling {
		return 0, errors.New("invalid candidate limit")
	}
	return value, nil
}

// SafePath intentionally accepts a conservative portable subset. Protected
// control files stay forbidden even when a caller allowlists them explicitly.
func SafePath(name string) error {
	if len(name) == 0 || len(name) > 240 || !safeChars.MatchString(name) || path.IsAbs(name) || path.Clean(name) != name || name == "." {
		return errors.New("unsafe candidate path")
	}
	for _, component := range strings.Split(strings.ToLower(name), "/") {
		if component == ".." || strings.HasSuffix(component, ".") || strings.HasPrefix(component, ".git") || strings.HasPrefix(component, ".sofa") || strings.HasPrefix(component, ".env") {
			return errors.New("protected or unsafe candidate path")
		}
		switch component {
		case "action.yml", "action.yaml", "agents.md", "claude.md", "copilot-instructions.md", ".claude", ".codex", ".copilot", "sofa.yml", "sofa.yaml", "sofa.json":
			return errors.New("protected candidate path")
		}
		stem := strings.Split(component, ".")[0]
		if stem == "con" || stem == "prn" || stem == "aux" || stem == "nul" || deviceName.MatchString(stem) {
			return errors.New("reserved candidate path")
		}
	}
	return nil
}

var tokenPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})`),
	regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`),
	regexp.MustCompile(`sk-(?:proj-|ant-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`),
}

// ScanSecrets is a deterministic defense in depth, not proof that arbitrary
// encodings or novel credentials cannot leak. Errors never include values.
func ScanSecrets(data []byte, forbidden [][]byte) error {
	for _, value := range forbidden {
		if len(value) > 0 && bytes.Contains(data, value) {
			return errors.New("candidate contains forbidden sensitive material")
		}
	}
	for _, pattern := range tokenPatterns {
		if pattern.Match(data) {
			return errors.New("candidate contains possible credential material")
		}
	}
	return nil
}

type BaseFile struct {
	Mode    string
	Content []byte
}

// VerifyBase must read the trusted Git tree at BaseSHA, never an agent-provided
// checkout. The callback must return false for missing paths, and preserve Git
// mode information so symlinks and submodules cannot masquerade as files.
func VerifyBase(b Bundle, lookup func(string) (BaseFile, bool, error)) error {
	for _, f := range b.Files {
		if err := SafePath(f.Path); err != nil {
			return err
		}
		for parent := path.Dir(f.Path); parent != "."; parent = path.Dir(parent) {
			base, exists, err := lookup(parent)
			if err != nil {
				return errors.New("cannot verify candidate base parent")
			}
			if exists && base.Mode != "040000" {
				return errors.New("candidate parent is not a directory at base")
			}
		}
		base, exists, err := lookup(f.Path)
		if err != nil {
			return errors.New("cannot verify candidate base")
		}
		if f.Operation == "add" {
			if exists {
				return errors.New("candidate add already exists at base")
			}
		} else if (f.Operation != "update" && f.Operation != "delete") || !exists || base.Mode != RegularMode || Hash(base.Content) != f.BeforeSHA256 {
			return errors.New("candidate preimage mismatch")
		}
	}
	return nil
}

type CheckEvidence struct {
	Version         int    `json:"version"`
	Name            string `json:"name"`
	CandidateDigest string `json:"candidate_digest"`
	Passed          bool   `json:"passed"`
}

// ValidateEvidence checks trusted verifier results. Merely uploading these
// fields from an agent job does not make them trusted verifier evidence.
func ValidateEvidence(b Bundle, evidence []CheckEvidence, required []string) error {
	if len(required) == 0 || !digest.MatchString(b.CandidateDigest) {
		return errors.New("required checks or candidate identity missing")
	}
	seen := map[string]CheckEvidence{}
	for _, item := range evidence {
		if _, exists := seen[item.Name]; exists || !identifier.MatchString(item.Name) || item.Version != Version || item.CandidateDigest != b.CandidateDigest {
			return errors.New("invalid, duplicate or stale check evidence")
		}
		seen[item.Name] = item
	}
	seenRequired := map[string]bool{}
	for _, name := range required {
		if seenRequired[name] || !identifier.MatchString(name) {
			return errors.New("invalid required check policy")
		}
		seenRequired[name] = true
		item, exists := seen[name]
		if !exists || !item.Passed {
			return errors.New("required candidate check has not passed")
		}
	}
	return nil
}
