package discovery

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/state"
)

var (
	ErrAuthority = errors.New("discovery lacks trusted Project authority")
	ErrRevision  = errors.New("specification revision requires new review")
)

// Policy comes from trusted repository configuration, never an issue.
// Project ACLs restrict Backlog/Ready; GitHub does not reliably expose the mover.
type Policy struct {
	Repository       string
	RepositoryID     string
	ProjectID        string
	DiscoveryStatus  string
	SpecReviewStatus string
	BacklogStatus    string
	ReadyStatus      string
}

// SpecComment is a live GitHub issue-comment observation, not authority by
// itself. Its immutable identity is bound to the durable Discovery task.
type SpecComment struct {
	ID        int64
	AuthorID  string
	Body      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c SpecComment) valid() bool {
	return c.ID > 0 && c.AuthorID != "" && len(c.AuthorID) <= 512 && !strings.ContainsAny(c.AuthorID, "\x00\r\n\t ") && !c.CreatedAt.IsZero() && !c.UpdatedAt.Before(c.CreatedAt)
}

func (p Policy) valid() bool {
	return p.Repository != "" && p.RepositoryID != "" && p.ProjectID != "" && p.DiscoveryStatus != "" && p.SpecReviewStatus != "" && p.BacklogStatus != "" && p.ReadyStatus != "" && p.DiscoveryStatus != p.SpecReviewStatus && p.SpecReviewStatus != p.BacklogStatus && p.BacklogStatus != p.ReadyStatus
}

func (p Policy) trusted(s admission.Snapshot, status string) bool {
	return p.valid() && s.Complete && s.Open && strings.EqualFold(s.Repository, p.Repository) && s.RepositoryID == p.RepositoryID && s.ProjectID == p.ProjectID && s.ProjectPrivate && s.IssueID != "" && s.Number > 0 && s.ProjectItemID != "" && s.CurrentStatus == status && s.StatusOptionID != "" && !s.StatusUpdatedAt.IsZero()
}

func (p Policy) matches(s admission.Snapshot, r state.SpecRecord) bool {
	return strings.EqualFold(r.Repository, p.Repository) && r.IssueID == s.IssueID && r.Issue == int64(s.Number) && r.ProjectID == s.ProjectID && r.ProjectItemID == s.ProjectItemID
}

func sameComment(c SpecComment, id int64, author string, createdAt, updatedAt time.Time) bool {
	return c.valid() && c.ID == id && c.AuthorID == author && c.CreatedAt.Equal(createdAt) && c.UpdatedAt.Equal(updatedAt)
}

func sourceDigest(s admission.Snapshot) (string, error) {
	_, digest, err := admission.CanonicalSpec(s.Title, s.Body)
	return digest, err
}

func commentDigest(s admission.Snapshot, c SpecComment) ([]byte, string, error) {
	if !c.valid() {
		return nil, "", ErrRevision
	}
	if _, err := Parse(c.Body); err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrRevision, err)
	}
	return admission.CanonicalSpec(s.Title, c.Body)
}

// AuthorizeDiscovery treats the private Project's Discovery status as explicit
// owner admission. Public issue creation alone never authorizes inference.
func AuthorizeDiscovery(p Policy, s admission.Snapshot) (state.DiscoveryAdmission, []byte, error) {
	if !p.trusted(s, p.DiscoveryStatus) {
		return state.DiscoveryAdmission{}, nil, ErrAuthority
	}
	canonical, digest, err := admission.CanonicalSpec(s.Title, s.Body)
	if err != nil {
		return state.DiscoveryAdmission{}, nil, fmt.Errorf("%w: invalid source", ErrRevision)
	}
	return state.DiscoveryAdmission{
		Repository:      strings.ToLower(p.Repository),
		IssueID:         s.IssueID,
		Issue:           int64(s.Number),
		ProjectID:       s.ProjectID,
		ProjectItemID:   s.ProjectItemID,
		StatusOptionID:  s.StatusOptionID,
		StatusUpdatedAt: s.StatusUpdatedAt,
		SourceDigest:    digest,
	}, canonical, nil
}

// ReviewCommentReadyForMove verifies an already published, durable Discovery
// comment before the trusted controller advances Discovery to Spec Review.
// It does not itself mutate the Project or approve the specification.
func ReviewCommentReadyForMove(p Policy, s admission.Snapshot, c SpecComment, task state.DiscoveryTask) error {
	if !p.trusted(s, p.DiscoveryStatus) || task.Phase != state.DiscoveryReview || task.IssueID != s.IssueID || task.Issue != int64(s.Number) || !strings.EqualFold(task.Repository, p.Repository) || task.ProjectID != p.ProjectID || task.ProjectItemID != s.ProjectItemID || task.StatusOptionID != s.StatusOptionID || !task.StatusUpdatedAt.Equal(s.StatusUpdatedAt) || !sameComment(c, task.CommentID, task.CommentAuthorID, task.CommentCreatedAt, task.CommentUpdatedAt) {
		return ErrAuthority
	}
	source, err := sourceDigest(s)
	if err != nil || source != task.SourceDigest || (!s.IssueLastEditedAt.IsZero() && s.IssueLastEditedAt.After(c.CreatedAt)) || c.CreatedAt.Before(task.StatusUpdatedAt) {
		return ErrRevision
	}
	_, digest, err := commentDigest(s, c)
	if err != nil || digest != task.SpecDigest {
		return ErrRevision
	}
	return nil
}

// CaptureReview records the versioned bot-authored comment already visible in
// Spec Review. A copied marker from another commenter has no authority.
func CaptureReview(p Policy, s admission.Snapshot, c SpecComment, task state.DiscoveryTask, prior *state.SpecRecord) (state.SpecRecord, []byte, bool, error) {
	if !p.trusted(s, p.SpecReviewStatus) || task.Phase != state.DiscoveryReview || task.IssueID != s.IssueID || task.Issue != int64(s.Number) || !strings.EqualFold(task.Repository, p.Repository) || task.ProjectID != p.ProjectID || task.ProjectItemID != s.ProjectItemID || !sameComment(c, task.CommentID, task.CommentAuthorID, task.CommentCreatedAt, task.CommentUpdatedAt) {
		return state.SpecRecord{}, nil, false, ErrAuthority
	}
	if prior != nil && !p.matches(s, *prior) {
		return state.SpecRecord{}, nil, false, ErrAuthority
	}
	source, err := sourceDigest(s)
	if err != nil || source != task.SourceDigest || (!s.IssueLastEditedAt.IsZero() && s.IssueLastEditedAt.After(c.CreatedAt)) {
		return state.SpecRecord{}, nil, false, ErrRevision
	}
	canonical, digest, err := commentDigest(s, c)
	if err != nil || digest != task.SpecDigest || c.UpdatedAt.After(s.StatusUpdatedAt) {
		return state.SpecRecord{}, nil, false, ErrRevision
	}
	record := state.SpecRecord{
		Repository:       strings.ToLower(p.Repository),
		IssueID:          s.IssueID,
		Issue:            int64(s.Number),
		ProjectID:        s.ProjectID,
		ProjectItemID:    s.ProjectItemID,
		SourceDigest:     source,
		SpecDigest:       digest,
		IssueEditedAt:    s.IssueLastEditedAt,
		CommentID:        c.ID,
		CommentAuthorID:  c.AuthorID,
		CommentCreatedAt: c.CreatedAt,
		CommentUpdatedAt: c.UpdatedAt,
		ReviewOptionID:   s.StatusOptionID,
		ReviewUpdatedAt:  s.StatusUpdatedAt,
	}
	return record, canonical, prior == nil || *prior != record, nil
}

func currentRevision(s admission.Snapshot, c SpecComment, r state.SpecRecord) bool {
	source, err := sourceDigest(s)
	if err != nil || source != r.SourceDigest || (!s.IssueLastEditedAt.IsZero() && s.IssueLastEditedAt.After(r.CommentCreatedAt)) || !sameComment(c, r.CommentID, r.CommentAuthorID, r.CommentCreatedAt, r.CommentUpdatedAt) {
		return false
	}
	_, digest, err := commentDigest(s, c)
	return err == nil && digest == r.SpecDigest
}

// ApproveBacklog binds only a prior observed Spec Review proposal to a later
// restricted-Project Backlog transition. It never writes the Project status.
func ApproveBacklog(p Policy, s admission.Snapshot, c SpecComment, prior state.SpecRecord) (state.SpecRecord, bool, error) {
	if !p.trusted(s, p.BacklogStatus) || !p.matches(s, prior) || prior.SpecDigest == "" || prior.ReviewUpdatedAt.IsZero() {
		return prior, false, ErrAuthority
	}
	if !currentRevision(s, c, prior) || !s.StatusUpdatedAt.After(prior.ReviewUpdatedAt) || !s.StatusUpdatedAt.After(c.UpdatedAt) || (!s.IssueLastEditedAt.IsZero() && !s.StatusUpdatedAt.After(s.IssueLastEditedAt)) {
		return prior, false, ErrRevision
	}
	if prior.ApprovedDigest != "" {
		if prior.ApprovedDigest == prior.SpecDigest && prior.BacklogOptionID == s.StatusOptionID && prior.BacklogUpdatedAt.Equal(s.StatusUpdatedAt) {
			return prior, false, nil
		}
		return prior, false, ErrRevision
	}
	prior.ApprovedDigest = prior.SpecDigest
	prior.BacklogOptionID = s.StatusOptionID
	prior.BacklogUpdatedAt = s.StatusUpdatedAt
	return prior, true, nil
}

// ReadyApproved supplements exact-Ready admission with observed Backlog
// approval. A changed spec comment or source idea requires re-review.
func ReadyApproved(p Policy, s admission.Snapshot, c SpecComment, record state.SpecRecord) error {
	if !p.trusted(s, p.ReadyStatus) || !p.matches(s, record) || record.ApprovedDigest == "" || record.BacklogUpdatedAt.IsZero() {
		return ErrAuthority
	}
	if !currentRevision(s, c, record) || record.ApprovedDigest != record.SpecDigest || !s.StatusUpdatedAt.After(record.BacklogUpdatedAt) || (!s.IssueLastEditedAt.IsZero() && (!record.BacklogUpdatedAt.After(s.IssueLastEditedAt) || !s.StatusUpdatedAt.After(s.IssueLastEditedAt))) {
		return ErrRevision
	}
	return nil
}
