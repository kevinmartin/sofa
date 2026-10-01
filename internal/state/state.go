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
	Repository      string    `json:"repository"`
	Issue           int64     `json:"issue"`
	SpecDigest      string    `json:"spec_digest"`
	ConfigDigest    string    `json:"config_digest"`
	BaseSHA         string    `json:"base_sha"`
	ProjectID       string    `json:"project_id"`
	ProjectItemID   string    `json:"project_item_id"`
	StatusOptionID  string    `json:"status_option_id"`
	StatusUpdatedAt time.Time `json:"status_updated_at"`
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
	RunID      string `json:"run_id" validate:"required"`
	RunAttempt int    `json:"run_attempt" validate:"gt=0"`
}

type Fence struct {
	AttemptID  string `json:"attempt_id" validate:"required"`
	Generation int64  `json:"generation" validate:"gt=0"`
	Owner      Owner  `json:"owner" validate:"required"`
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

// RepairIntent reserves an authorized review repair for one exact PR head.
// A later head or another PR cannot reuse the same feedback reservation.
type RepairIntent struct {
	FeedbackID      string `json:"feedback_id"`
	FeedbackHash    string `json:"feedback_hash"`
	PRBaseSHA       string `json:"pr_base_sha"`
	PRHeadSHA       string `json:"pr_head_sha"`
	PRNumber        int64  `json:"pr_number"`
	CandidateDigest string `json:"candidate_digest,omitempty"`
	CandidateSHA    string `json:"candidate_sha,omitempty"`
}

// valid binds repair feedback to a publication and requires the candidate digest
// and SHA to be either both absent or both well formed.
func (r RepairIntent) valid(p *Publication) bool {
	if p == nil || !reference(r.FeedbackID) || !digestPattern.MatchString(r.FeedbackHash) || !shaPattern.MatchString(r.PRBaseSHA) || !shaPattern.MatchString(r.PRHeadSHA) || r.PRNumber <= 0 || p.PRNumber != r.PRNumber || p.HeadSHA != r.PRHeadSHA {
		return false
	}
	return r.CandidateDigest == "" && r.CandidateSHA == "" || digestPattern.MatchString(r.CandidateDigest) && shaPattern.MatchString(r.CandidateSHA)
}

type Attempt struct {
	ID                     string            `json:"id"`
	Admission              Admission         `json:"admission"`
	SpecRevision           int64             `json:"spec_revision,omitempty"`
	SupersededAt           time.Time         `json:"superseded_at,omitempty"`
	SupersededSourceDigest string            `json:"superseded_source_digest,omitempty"`
	Phase                  Phase             `json:"phase"`
	Generation             int64             `json:"generation"`
	Owner                  *Owner            `json:"owner,omitempty"`
	Dispatch               string            `json:"dispatch"`
	Limits                 Limits            `json:"limits"`
	Counts                 Counters          `json:"counts"`
	Checkpoint             *Checkpoint       `json:"checkpoint,omitempty"`
	Publication            *Publication      `json:"publication,omitempty"`
	Repair                 *RepairIntent     `json:"repair,omitempty"`
	RevertScan             *RevertScanCursor `json:"revert_scan,omitempty"`
	Failure                string            `json:"failure,omitempty"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

// RevertScanCursor resumes a bounded default-branch comparison for one
// completed attempt. ActiveBase and ActiveHead are immutable while paging;
// CompletedHead advances only after every page of that range was processed.
type RevertScanCursor struct {
	MergeSHA      string `json:"merge_sha"`
	CompletedHead string `json:"completed_head,omitempty"`
	ActiveBase    string `json:"active_base,omitempty"`
	ActiveHead    string `json:"active_head,omitempty"`
	NextPage      int    `json:"next_page,omitempty"`
}

func (c RevertScanCursor) valid() bool {
	if !shaPattern.MatchString(c.MergeSHA) || c.CompletedHead != "" && !shaPattern.MatchString(c.CompletedHead) {
		return false
	}
	if c.ActiveBase == "" && c.ActiveHead == "" && c.NextPage == 0 {
		return c.CompletedHead != ""
	}
	return shaPattern.MatchString(c.ActiveBase) && shaPattern.MatchString(c.ActiveHead) && c.ActiveBase != c.ActiveHead && c.NextPage > 0 && (c.CompletedHead == "" && c.ActiveBase == c.MergeSHA || c.CompletedHead != "" && c.ActiveBase == c.CompletedHead)
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

// SpecRecord is the observed, version-bound Project approval. A proposal must
// first be seen in Spec Review; only a later, trusted Backlog status revision
// can approve that exact canonical specification. Neither field is an actor
// claim: Project write access is the approval boundary.
type SpecRecord struct {
	Revision         int64     `json:"revision,omitempty"`
	Repository       string    `json:"repository"`
	IssueID          string    `json:"issue_id"`
	Issue            int64     `json:"issue"`
	ProjectID        string    `json:"project_id"`
	ProjectItemID    string    `json:"project_item_id"`
	SourceDigest     string    `json:"source_digest"`
	SpecDigest       string    `json:"spec_digest"`
	IssueEditedAt    time.Time `json:"issue_edited_at"`
	CommentID        int64     `json:"comment_id"`
	CommentAuthorID  string    `json:"comment_author_id"`
	CommentCreatedAt time.Time `json:"comment_created_at"`
	CommentUpdatedAt time.Time `json:"comment_updated_at"`
	ReviewOptionID   string    `json:"review_option_id"`
	ReviewUpdatedAt  time.Time `json:"review_updated_at"`
	ApprovedDigest   string    `json:"approved_digest,omitempty"`
	BacklogOptionID  string    `json:"backlog_option_id,omitempty"`
	BacklogUpdatedAt time.Time `json:"backlog_updated_at,omitempty"`
}

type DiscoveryPhase string

const (
	DiscoveryPending  DiscoveryPhase = "pending"
	DiscoveryRunning  DiscoveryPhase = "running"
	DiscoveryReview   DiscoveryPhase = "spec_review"
	DiscoveryBlocked  DiscoveryPhase = "blocked"
	DiscoveryCanceled DiscoveryPhase = "cancelled"
)

// DiscoveryTask reserves one issue's bounded research work before a model runs.
// Its source and Project admission revision cannot be replaced on retry.
type DiscoveryTask struct {
	Revision         int64                 `json:"revision,omitempty"`
	Repository       string                `json:"repository"`
	IssueID          string                `json:"issue_id"`
	Issue            int64                 `json:"issue"`
	ProjectID        string                `json:"project_id"`
	ProjectItemID    string                `json:"project_item_id"`
	StatusOptionID   string                `json:"status_option_id"`
	StatusUpdatedAt  time.Time             `json:"status_updated_at"`
	SourceDigest     string                `json:"source_digest"`
	Phase            DiscoveryPhase        `json:"phase"`
	Generation       int64                 `json:"generation"`
	Owner            *Owner                `json:"owner,omitempty"`
	ModelCalls       int64                 `json:"model_calls"`
	MaxModelCalls    int64                 `json:"max_model_calls"`
	Publication      *DiscoveryPublication `json:"publication,omitempty"`
	SpecDigest       string                `json:"spec_digest,omitempty"`
	CommentID        int64                 `json:"comment_id,omitempty"`
	CommentAuthorID  string                `json:"comment_author_id,omitempty"`
	CommentCreatedAt time.Time             `json:"comment_created_at,omitempty"`
	CommentUpdatedAt time.Time             `json:"comment_updated_at,omitempty"`
	Failure          string                `json:"failure,omitempty"`
	CreatedAt        time.Time             `json:"created_at"`
	UpdatedAt        time.Time             `json:"updated_at"`
}

// DiscoveryPublication is an unapproved, fenced intent to post one immutable
// comment. It is never accepted as a specification or Backlog approval.
type DiscoveryPublication struct {
	Digest        string `json:"digest"`
	Key           string `json:"key"`
	PostAttempted bool   `json:"post_attempted"`
	Producer      Owner  `json:"producer"`
}

// valid checks the publication digest, retry key, and producer identity formats.
func (p DiscoveryPublication) valid() bool {
	return digestPattern.MatchString(p.Digest) && digestPattern.MatchString(p.Key) && validOwner(p.Producer)
}

// BoardProjection remembers the last accepted Project observation and, when
// present, one fenced write intent. Unexpected manual edits are reported by
// reconciliation rather than overwritten by the factory.
type BoardProjection struct {
	Repository           string    `json:"repository"`
	IssueID              string    `json:"issue_id"`
	ProjectID            string    `json:"project_id"`
	ProjectItemID        string    `json:"project_item_id"`
	Stage                string    `json:"stage"`
	OptionID             string    `json:"option_id"`
	UpdatedAt            time.Time `json:"updated_at"`
	PendingStage         string    `json:"pending_stage,omitempty"`
	PendingOptionID      string    `json:"pending_option_id,omitempty"`
	PendingFromOptionID  string    `json:"pending_from_option_id,omitempty"`
	PendingFromUpdatedAt time.Time `json:"pending_from_updated_at,omitempty"`
}

type PollCursor struct {
	LastPoll   time.Time `json:"last_poll"`
	LastWakeID string    `json:"last_wake_id,omitempty"`
	Generation int64     `json:"generation"`
}

// valid checks that poll generation, observation time, and optional wake ID agree.
func (p PollCursor) valid() bool {
	return p.Generation >= 0 && (p.Generation == 0 && p.LastPoll.IsZero() || p.Generation > 0 && !p.LastPoll.IsZero()) && (p.LastWakeID == "" || reference(p.LastWakeID))
}

// valid checks projection metadata and binds any pending move to the observed
// status revision. It does not check whether the stage transition is authorized.
func (p BoardProjection) valid() bool {
	if !repoPattern.MatchString(p.Repository) || !reference(p.IssueID) || !reference(p.ProjectID) || !reference(p.ProjectItemID) || !validStage(p.Stage) || !reference(p.OptionID) || p.UpdatedAt.IsZero() {
		return false
	}
	if p.PendingStage == "" {
		return p.PendingOptionID == "" && p.PendingFromOptionID == "" && p.PendingFromUpdatedAt.IsZero()
	}
	return validStage(p.PendingStage) && reference(p.PendingOptionID) && p.PendingFromOptionID == p.OptionID && p.PendingFromUpdatedAt.Equal(p.UpdatedAt)
}

// validStage checks a bounded stage label, without requiring a known lifecycle stage.
func validStage(stage string) bool {
	return stage != "" && len(stage) <= 100 && !strings.ContainsAny(stage, "\x00\r\n\t")
}

// valid checks Discovery identity, budget bounds, and phase-dependent ownership
// and publication metadata; it does not read current Project authority.
func (d DiscoveryTask) valid() bool {
	if !repoPattern.MatchString(d.Repository) || !reference(d.IssueID) || d.Issue < 1 || !reference(d.ProjectID) || !reference(d.ProjectItemID) || !reference(d.StatusOptionID) || d.StatusUpdatedAt.IsZero() || !digestPattern.MatchString(d.SourceDigest) || d.Revision < 0 || d.Generation < 0 || d.ModelCalls < 0 || d.MaxModelCalls < 1 || d.MaxModelCalls > 20 || d.ModelCalls > d.MaxModelCalls || d.CreatedAt.IsZero() || d.UpdatedAt.Before(d.CreatedAt) {
		return false
	}
	if d.Owner != nil && (!validOwner(*d.Owner) || d.Generation == 0 || d.Phase != DiscoveryRunning) {
		return false
	}
	if d.Publication != nil && !d.Publication.valid() {
		return false
	}
	switch d.Phase {
	case DiscoveryPending:
		return d.Owner == nil && d.SpecDigest == "" && d.CommentID == 0
	case DiscoveryRunning:
		return d.Owner != nil && d.SpecDigest == "" && d.CommentID == 0 && d.ModelCalls > 0
	case DiscoveryReview:
		return d.Owner == nil && d.Publication == nil && digestPattern.MatchString(d.SpecDigest) && d.CommentID > 0 && reference(d.CommentAuthorID) && !d.CommentCreatedAt.IsZero() && !d.CommentUpdatedAt.Before(d.CommentCreatedAt)
	case DiscoveryBlocked, DiscoveryCanceled:
		return d.Owner == nil && d.Publication == nil && d.SpecDigest == "" && d.CommentID == 0
	default:
		return false
	}
}

// valid checks specification metadata and requires any Backlog approval to match
// the spec digest and follow review and issue-edit timestamps.
func (r SpecRecord) valid() bool {
	if r.Revision < 0 || !repoPattern.MatchString(r.Repository) || !reference(r.IssueID) || r.Issue < 1 || !reference(r.ProjectID) || !reference(r.ProjectItemID) || !digestPattern.MatchString(r.SourceDigest) || !digestPattern.MatchString(r.SpecDigest) || r.CommentID < 1 || !reference(r.CommentAuthorID) || r.CommentCreatedAt.IsZero() || r.CommentUpdatedAt.Before(r.CommentCreatedAt) || !reference(r.ReviewOptionID) || r.ReviewUpdatedAt.IsZero() {
		return false
	}
	if r.ApprovedDigest == "" {
		return r.BacklogOptionID == "" && r.BacklogUpdatedAt.IsZero()
	}
	return r.ApprovedDigest == r.SpecDigest && reference(r.BacklogOptionID) && r.BacklogUpdatedAt.After(r.ReviewUpdatedAt) && r.BacklogUpdatedAt.After(r.IssueEditedAt)
}

type State struct {
	Version               int                        `json:"version"`
	Attempts              map[string]Attempt         `json:"attempts"`
	Observations          []Observation              `json:"observations"`
	Specs                 map[string]SpecRecord      `json:"specs,omitempty"`
	SpecHistory           map[string][]SpecRecord    `json:"spec_history,omitempty"`
	Discoveries           map[string]DiscoveryTask   `json:"discoveries,omitempty"`
	DiscoveryHistory      map[string][]DiscoveryTask `json:"discovery_history,omitempty"`
	DiscoveryResetHistory map[string][]DiscoveryTask `json:"discovery_reset_history,omitempty"`
	Projections           map[string]BoardProjection `json:"projections,omitempty"`
	Poll                  *PollCursor                `json:"poll,omitempty"`
}

type Snapshot struct {
	Revision string
	State    State
}

type Store interface {
	Load(context.Context) (Snapshot, error)
	CompareAndSwap(context.Context, string, State) error
}

// Empty returns a ledger at the current schema version with initialized collections.
func Empty() State {
	return State{
		Version:               Version,
		Attempts:              map[string]Attempt{},
		Observations:          []Observation{},
		Specs:                 map[string]SpecRecord{},
		SpecHistory:           map[string][]SpecRecord{},
		Discoveries:           map[string]DiscoveryTask{},
		DiscoveryHistory:      map[string][]DiscoveryTask{},
		DiscoveryResetHistory: map[string][]DiscoveryTask{},
		Projections:           map[string]BoardProjection{},
	}
}

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}([a-f0-9]{24})?$`)
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (a Admission) Validate() error {
	if !repoPattern.MatchString(a.Repository) || a.Issue <= 0 || !digestPattern.MatchString(a.SpecDigest) || !digestPattern.MatchString(a.ConfigDigest) || !shaPattern.MatchString(a.BaseSHA) || !reference(a.ProjectID) || !reference(a.ProjectItemID) || !reference(a.StatusOptionID) || a.StatusUpdatedAt.IsZero() {
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
		Repository                                string
		Issue                                     int64
		SpecDigest, ProjectItemID, StatusOptionID string
		StatusUpdatedAt                           time.Time
	}{
		Repository:      strings.ToLower(a.Repository),
		Issue:           a.Issue,
		SpecDigest:      a.SpecDigest,
		ProjectItemID:   a.ProjectItemID,
		StatusOptionID:  a.StatusOptionID,
		StatusUpdatedAt: a.StatusUpdatedAt,
	})
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

// Validate checks ledger metadata, revision histories, budgets, and observation
// references without consulting GitHub. Every reported violation wraps ErrInvalid;
// a valid ledger alone does not establish current external authority.
func (s State) Validate() error {
	if s.Version != Version || s.Attempts == nil {
		return fmt.Errorf("%w: ledger version or attempts", ErrInvalid)
	}
	for id, a := range s.Attempts {
		if id != a.ID || id != AttemptID(a.Admission) || a.Admission.Validate() != nil || !a.Limits.valid() || !a.Limits.permits(a.Counts) || a.SpecRevision < 0 || a.Generation < 0 || (a.Owner != nil && (!validOwner(*a.Owner) || a.Generation == 0)) || a.CreatedAt.IsZero() || a.UpdatedAt.Before(a.CreatedAt) {
			return fmt.Errorf("%w: attempt metadata", ErrInvalid)
		}
		if a.SupersededAt.IsZero() != (a.SupersededSourceDigest == "") || !a.SupersededAt.IsZero() && (!digestPattern.MatchString(a.SupersededSourceDigest) || a.Owner != nil || a.Phase != Blocked || a.Failure != "human-change") {
			return fmt.Errorf("%w: superseded attempt", ErrInvalid)
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
		if a.Repair != nil && !a.Repair.valid(a.Publication) {
			return fmt.Errorf("%w: repair intent", ErrInvalid)
		}
		if a.RevertScan != nil && (!a.RevertScan.valid() || a.Publication == nil) {
			return fmt.Errorf("%w: revert scan cursor", ErrInvalid)
		}
	}
	for id, record := range s.Specs {
		if id != record.IssueID || !record.valid() || record.Revision != int64(len(s.SpecHistory[id])) {
			return fmt.Errorf("%w: specification approval", ErrInvalid)
		}
	}
	for id, history := range s.SpecHistory {
		if len(history) > 0 {
			if _, ok := s.Specs[id]; !ok {
				return fmt.Errorf("%w: orphaned specification history", ErrInvalid)
			}
		}
		for i, record := range history {
			if id != record.IssueID || !record.valid() || record.ApprovedDigest == "" || record.Revision != int64(i) {
				return fmt.Errorf("%w: specification history", ErrInvalid)
			}
			if i > 0 {
				previous := history[i-1]
				if record.Repository != previous.Repository || record.Issue != previous.Issue || record.ProjectID != previous.ProjectID || record.ProjectItemID != previous.ProjectItemID || record.SourceDigest == previous.SourceDigest || record.CommentID == previous.CommentID || !record.ReviewUpdatedAt.After(previous.BacklogUpdatedAt) {
					return fmt.Errorf("%w: specification history identity", ErrInvalid)
				}
			}
		}
		if current, ok := s.Specs[id]; ok {
			if current.Revision != int64(len(history)) {
				return fmt.Errorf("%w: specification revision", ErrInvalid)
			}
			if len(history) > 0 {
				previous := history[len(history)-1]
				if current.Repository != previous.Repository || current.Issue != previous.Issue || current.ProjectID != previous.ProjectID || current.ProjectItemID != previous.ProjectItemID || current.SourceDigest == previous.SourceDigest || current.CommentID == previous.CommentID || !current.ReviewUpdatedAt.After(previous.BacklogUpdatedAt) {
					return fmt.Errorf("%w: specification revision identity", ErrInvalid)
				}
			}
		}
	}
	for id, discovery := range s.Discoveries {
		if id != discovery.IssueID || !discovery.valid() || discovery.Revision != int64(len(s.DiscoveryHistory[id])) {
			return fmt.Errorf("%w: discovery task", ErrInvalid)
		}
	}
	for id, history := range s.DiscoveryHistory {
		if len(history) > 0 {
			if _, ok := s.Discoveries[id]; !ok {
				return fmt.Errorf("%w: orphaned discovery history", ErrInvalid)
			}
		}
		for i, task := range history {
			if id != task.IssueID || !task.valid() || task.Phase != DiscoveryReview || task.Revision != int64(i) {
				return fmt.Errorf("%w: discovery history", ErrInvalid)
			}
			if i > 0 {
				previous := history[i-1]
				if task.Repository != previous.Repository || task.Issue != previous.Issue || task.ProjectID != previous.ProjectID || task.ProjectItemID != previous.ProjectItemID || task.SourceDigest == previous.SourceDigest || task.CommentID == previous.CommentID || task.ModelCalls < previous.ModelCalls || !task.StatusUpdatedAt.After(previous.StatusUpdatedAt) {
					return fmt.Errorf("%w: discovery history identity", ErrInvalid)
				}
			}
		}
		if current, ok := s.Discoveries[id]; ok {
			if current.Revision != int64(len(history)) {
				return fmt.Errorf("%w: discovery revision", ErrInvalid)
			}
			if len(history) > 0 {
				previous := history[len(history)-1]
				if current.Repository != previous.Repository || current.Issue != previous.Issue || current.ProjectID != previous.ProjectID || current.ProjectItemID != previous.ProjectItemID || current.SourceDigest == previous.SourceDigest || current.CommentID > 0 && current.CommentID == previous.CommentID || current.ModelCalls < previous.ModelCalls || !current.StatusUpdatedAt.After(previous.StatusUpdatedAt) {
					return fmt.Errorf("%w: discovery revision identity", ErrInvalid)
				}
			}
		}
	}
	for id, history := range s.DiscoveryResetHistory {
		current, ok := s.Discoveries[id]
		if !ok || len(history) == 0 || len(history) > 20 {
			return fmt.Errorf("%w: orphaned Discovery reset history", ErrInvalid)
		}
		for i, task := range history {
			if id != task.IssueID || !task.valid() || task.Phase != DiscoveryReview && task.Phase != DiscoveryBlocked && (task.Phase != DiscoveryPending || task.Owner != nil || task.Publication != nil) || task.Repository != current.Repository || task.Issue != current.Issue || task.ProjectID != current.ProjectID || task.ProjectItemID != current.ProjectItemID || task.Revision > current.Revision || !current.StatusUpdatedAt.After(task.StatusUpdatedAt) || current.ModelCalls < task.ModelCalls {
				return fmt.Errorf("%w: Discovery reset history", ErrInvalid)
			}
			if i > 0 && (!task.StatusUpdatedAt.After(history[i-1].StatusUpdatedAt) || task.ModelCalls < history[i-1].ModelCalls || task.Revision < history[i-1].Revision) {
				return fmt.Errorf("%w: Discovery reset order", ErrInvalid)
			}
		}
	}
	for id, projection := range s.Projections {
		if id != projection.IssueID || !projection.valid() {
			return fmt.Errorf("%w: board projection", ErrInvalid)
		}
	}
	if s.Poll != nil && !s.Poll.valid() {
		return fmt.Errorf("%w: poll cursor", ErrInvalid)
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
	for id, attempt := range s.Attempts {
		if attempt.RevertScan != nil && !recordedDoneForMerge(s, id, attempt.RevertScan.MergeSHA) {
			return fmt.Errorf("%w: revert scan completion", ErrInvalid)
		}
	}
	return nil
}

func (c Checkpoint) valid() bool {
	return c.Version == Version && (c.Phase == Executing || c.Phase == Validating) && reference(c.ArtifactID) && digestPattern.MatchString(c.Digest) && shaPattern.MatchString(c.CandidateSHA) && validOwner(c.Producer) && c.Generation > 0 && !c.AcceptedAt.IsZero() && c.ExpiresAt.After(c.AcceptedAt)
}

func (p Publication) valid() bool {
	return strings.HasPrefix(p.Branch, "sofa/") && reference(p.Branch) && !strings.Contains(p.Branch, "..") && !strings.ContainsAny(p.Branch, "~^:?*[\\") && shaPattern.MatchString(p.ExpectedHead) && digestPattern.MatchString(p.CandidateDigest) && p.PRNumber >= 0 && (p.PRNumber == 0 && p.PRURL == "" && p.HeadSHA == "" || p.PRNumber > 0 && strings.HasPrefix(p.PRURL, "https://") && shaPattern.MatchString(p.HeadSHA))
}
