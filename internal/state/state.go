// Package state implements the versioned, optimistic factory ledger. It stores
// metadata only: specifications, source, credentials and transcripts belong
// outside this ledger. Admission must already be authenticated by the caller.
package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

const Version = 1

var (
	ErrConflict         = errors.New("state revision conflict")
	ErrNotFound         = errors.New("attempt not found")
	ErrClaimed          = errors.New("attempt already claimed")
	ErrStale            = errors.New("stale ownership fence")
	ErrLimit            = errors.New("persistent attempt limit reached")
	ErrActive           = errors.New("owning run is active or its terminal state is unproven")
	ErrInvalid          = errors.New("invalid ledger input")
	ErrAdmissionChanged = errors.New("admitted snapshot changed or issue already has another authorization")
)

type Phase string

const (
	Pending    Phase = "pending"
	Executing  Phase = "executing"
	Validating Phase = "validating"
	Publishing Phase = "publishing"
	Draft      Phase = "draft"
	Blocked    Phase = "blocked"
	Deferred   Phase = "deferred"
)

// Admission contains immutable authorization references, never their bodies.
type Admission struct {
	Repository   string `json:"repository"`
	Issue        int64  `json:"issue"`
	SpecDigest   string `json:"spec_digest"`
	ConfigDigest string `json:"config_digest"`
	BaseSHA      string `json:"base_sha"`
	ApprovalID   string `json:"approval_id"`
	ReadyEventID string `json:"ready_event_id"`
	ActorID      string `json:"actor_id"`
	ProjectID    string `json:"project_id"`
}

type Limits struct {
	ModelCalls            int64 `json:"model_calls"`
	Repairs               int64 `json:"repairs"`
	InfrastructureRetries int64 `json:"infrastructure_retries"`
	RuntimeSeconds        int64 `json:"runtime_seconds"`
}

type Counters struct {
	ModelCalls            int64 `json:"model_calls"`
	Repairs               int64 `json:"repairs"`
	InfrastructureRetries int64 `json:"infrastructure_retries"`
	RuntimeSeconds        int64 `json:"runtime_seconds"`
}

type Owner struct {
	RunID      string `json:"run_id"`
	RunAttempt int    `json:"run_attempt"`
}

type Fence struct {
	AttemptID  string `json:"attempt_id"`
	Generation int64  `json:"generation"`
	Owner      Owner  `json:"owner"`
}

type Checkpoint struct {
	Version      int       `json:"version"`
	Phase        Phase     `json:"phase"`
	ArtifactID   string    `json:"artifact_id"`
	Digest       string    `json:"digest"`
	CandidateSHA string    `json:"candidate_sha"`
	Producer     Owner     `json:"producer"`
	Generation   int64     `json:"generation"`
	AcceptedAt   time.Time `json:"accepted_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Publication struct {
	Branch          string `json:"branch"`
	ExpectedHead    string `json:"expected_head"`
	CandidateDigest string `json:"candidate_digest"`
	HeadSHA         string `json:"head_sha,omitempty"`
	PRNumber        int64  `json:"pr_number,omitempty"`
	PRURL           string `json:"pr_url,omitempty"`
}

type Attempt struct {
	ID          string       `json:"id"`
	Admission   Admission    `json:"admission"`
	Phase       Phase        `json:"phase"`
	Generation  int64        `json:"generation"`
	Owner       *Owner       `json:"owner,omitempty"`
	Dispatch    string       `json:"dispatch"`
	Limits      Limits       `json:"limits"`
	Counts      Counters     `json:"counts"`
	Checkpoint  *Checkpoint  `json:"checkpoint,omitempty"`
	Publication *Publication `json:"publication,omitempty"`
	Failure     string       `json:"failure,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// Observation is compact machine-observed metadata, not model-generated prose.
// Callers must redact references and must not put arbitrary logs in these fields.
type Observation struct {
	Version         int       `json:"version"`
	ID              string    `json:"id"`
	AttemptID       string    `json:"attempt_id"`
	Stage           string    `json:"stage"`
	Outcome         string    `json:"outcome"`
	Revision        string    `json:"revision,omitempty"`
	EvidenceRef     string    `json:"evidence_ref,omitempty"`
	ModelCalls      int64     `json:"model_calls"`
	DurationSeconds int64     `json:"duration_seconds"`
	RecordedAt      time.Time `json:"recorded_at"`
}

type State struct {
	Version      int                `json:"version"`
	Attempts     map[string]Attempt `json:"attempts"`
	Observations []Observation      `json:"observations"`
}

type Snapshot struct {
	Revision string
	State    State
}

type Store interface {
	Load(context.Context) (Snapshot, error)
	CompareAndSwap(context.Context, string, State) error
}

func Empty() State {
	return State{Version: Version, Attempts: map[string]Attempt{}, Observations: []Observation{}}
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (a Admission) Validate() error {
	if !repoPattern.MatchString(a.Repository) || a.Issue <= 0 || !digestPattern.MatchString(a.SpecDigest) || !digestPattern.MatchString(a.ConfigDigest) || !shaPattern.MatchString(a.BaseSHA) || !reference(a.ApprovalID) || !reference(a.ReadyEventID) || !reference(a.ActorID) || !reference(a.ProjectID) {
		return fmt.Errorf("%w: admission references", ErrInvalid)
	}
	return nil
}

func reference(s string) bool {
	return len(s) > 0 && len(s) <= 512 && !strings.ContainsAny(s, "\x00\r\n\t ")
}

// SpecPath stores opaque GitHub issue IDs without assuming an alphabet for
// those IDs. The hash keeps Git paths portable and avoids exposing IDs there.
func SpecPath(issueID, specDigest string) (string, error) {
	if !reference(issueID) || !digestPattern.MatchString(specDigest) {
		return "", fmt.Errorf("%w: specification identity", ErrInvalid)
	}
	h := sha256.Sum256([]byte(issueID))
	return "specs/" + hex.EncodeToString(h[:]) + "/" + specDigest + ".json", nil
}
func validOwner(o Owner) bool { return reference(o.RunID) && o.RunAttempt > 0 }
func (l Limits) valid() bool {
	return l.ModelCalls >= 0 && l.Repairs >= 0 && l.InfrastructureRetries >= 0 && l.RuntimeSeconds > 0
}
func (c Counters) valid() bool {
	return c.ModelCalls >= 0 && c.Repairs >= 0 && c.InfrastructureRetries >= 0 && c.RuntimeSeconds >= 0
}
func (l Limits) permits(c Counters) bool {
	return c.valid() && c.ModelCalls <= l.ModelCalls && c.Repairs <= l.Repairs && c.InfrastructureRetries <= l.InfrastructureRetries && c.RuntimeSeconds <= l.RuntimeSeconds
}

func AttemptID(a Admission) string {
	b, _ := json.Marshal(struct {
		Repository                           string
		Issue                                int64
		SpecDigest, ApprovalID, ReadyEventID string
	}{strings.ToLower(a.Repository), a.Issue, a.SpecDigest, a.ApprovalID, a.ReadyEventID})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func Encode(s State) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func Decode(b []byte) (State, error) {
	var s State
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, fmt.Errorf("%w: decode ledger", ErrInvalid)
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return s, fmt.Errorf("%w: trailing ledger content", ErrInvalid)
	}
	return s, s.Validate()
}

func (s State) Validate() error {
	if s.Version != Version || s.Attempts == nil {
		return fmt.Errorf("%w: ledger version or attempts", ErrInvalid)
	}
	for id, a := range s.Attempts {
		if id != a.ID || id != AttemptID(a.Admission) || a.Admission.Validate() != nil || !a.Limits.valid() || !a.Limits.permits(a.Counts) || a.Generation < 0 || (a.Owner != nil && (!validOwner(*a.Owner) || a.Generation == 0)) || a.CreatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
			return fmt.Errorf("%w: attempt metadata", ErrInvalid)
		}
		switch a.Phase {
		case Pending, Executing, Validating, Publishing, Draft, Blocked, Deferred:
		default:
			return fmt.Errorf("%w: phase", ErrInvalid)
		}
		if a.Dispatch != "pending" && a.Dispatch != "sent" && a.Dispatch != "claimed" {
			return fmt.Errorf("%w: dispatch", ErrInvalid)
		}
		if a.Checkpoint != nil && !a.Checkpoint.valid() {
			return fmt.Errorf("%w: checkpoint", ErrInvalid)
		}
		if a.Publication != nil && !a.Publication.valid() {
			return fmt.Errorf("%w: publication", ErrInvalid)
		}
	}
	seen := map[string]bool{}
	for _, o := range s.Observations {
		if o.Version != Version || !reference(o.ID) || seen[o.ID] || !reference(o.Stage) || !reference(o.Outcome) || o.ModelCalls < 0 || o.DurationSeconds < 0 || o.RecordedAt.IsZero() || len(o.EvidenceRef) > 2048 || strings.ContainsAny(o.EvidenceRef, "\x00\n\r") {
			return fmt.Errorf("%w: observation", ErrInvalid)
		}
		if _, ok := s.Attempts[o.AttemptID]; !ok {
			return fmt.Errorf("%w: observation attempt", ErrInvalid)
		}
		seen[o.ID] = true
	}
	return nil
}

func (c Checkpoint) valid() bool {
	return c.Version == Version && (c.Phase == Executing || c.Phase == Validating) && reference(c.ArtifactID) && digestPattern.MatchString(c.Digest) && shaPattern.MatchString(c.CandidateSHA) && validOwner(c.Producer) && c.Generation > 0 && !c.AcceptedAt.IsZero() && c.ExpiresAt.After(c.AcceptedAt)
}

func (p Publication) valid() bool {
	return strings.HasPrefix(p.Branch, "sofa/") && reference(p.Branch) && !strings.Contains(p.Branch, "..") && !strings.ContainsAny(p.Branch, "~^:?*[\\") && shaPattern.MatchString(p.ExpectedHead) && digestPattern.MatchString(p.CandidateDigest) && p.PRNumber >= 0 && (p.PRNumber == 0 && p.PRURL == "" && p.HeadSHA == "" || p.PRNumber > 0 && strings.HasPrefix(p.PRURL, "https://") && shaPattern.MatchString(p.HeadSHA))
}
