// Package admission turns observed GitHub facts into a bounded delivery grant.
// Callers must obtain Snapshot from the authenticated work source, not issue JSON.
package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kevinmartin/sofa/internal/config"
)

type Comment struct {
	ID        string    `json:"id"`
	ActorID   string    `json:"actor_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type StatusEvent struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	ActorID      string    `json:"actor_id"`
	Status       string    `json:"status"`
	WasAutomated bool      `json:"was_automated"`
	CreatedAt    time.Time `json:"created_at"`
}
type Snapshot struct {
	Repository    string        `json:"repository"`
	RepositoryID  string        `json:"repository_id"`
	IssueID       string        `json:"issue_id"`
	Number        int           `json:"number"`
	Title         string        `json:"title"`
	Body          string        `json:"body"`
	Open          bool          `json:"open"`
	ProjectID     string        `json:"project_id"`
	CurrentStatus string        `json:"current_status"`
	BaseSHA       string        `json:"base_sha"`
	Comments      []Comment     `json:"comments"`
	StatusEvents  []StatusEvent `json:"status_events"`
	Complete      bool          `json:"complete"` // all relevant pagination succeeded
}
type Grant struct {
	Version      int    `json:"schema_version"`
	Repository   string `json:"repository"`
	RepositoryID string `json:"repository_id"`
	IssueID      string `json:"issue_id"`
	IssueNumber  int    `json:"issue_number"`
	ProjectID    string `json:"project_id"`
	OwnerID      string `json:"owner_id"`
	SpecDigest   string `json:"spec_digest"`
	ConfigDigest string `json:"config_digest"`
	BaseSHA      string `json:"base_sha"`
	ApprovalID   string `json:"approval_id"`
	ReadyEventID string `json:"ready_event_id"`
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CanonicalSpec normalizes line endings and boundary whitespace only. Changes to
// substantive text always need approval; no model decides whether an edit matters.
func CanonicalSpec(title, body string) ([]byte, string, error) {
	if !utf8.ValidString(title) || !utf8.ValidString(body) || len(title) > 1024 || len(body) > 64<<10 {
		return nil, "", errors.New("invalid or oversized specification")
	}
	normalize := func(s string) string {
		return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n"))
	}
	spec := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{normalize(title), normalize(body)}
	if spec.Title == "" || spec.Body == "" {
		return nil, "", errors.New("specification title and body are required")
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

func Authorize(c config.Config, s Snapshot) (Grant, []byte, error) {
	deny := func() (Grant, []byte, error) {
		return Grant{}, nil, errors.New("current issue lacks matching owner specification approval and Ready authorization")
	}
	configDigest, err := c.Digest()
	if err != nil {
		return Grant{}, nil, err
	}
	if !s.Complete || !s.Open || s.Repository != c.Repository || s.RepositoryID != c.RepositoryID || s.ProjectID != c.ProjectID || s.CurrentStatus != c.ReadyStatus || s.IssueID == "" || s.Number < 1 || !shaPattern.MatchString(s.BaseSHA) {
		return deny()
	}
	spec, digest, err := CanonicalSpec(s.Title, s.Body)
	if err != nil {
		return Grant{}, nil, err
	}
	var ready *StatusEvent
	for i := range s.StatusEvents {
		e := &s.StatusEvents[i]
		if e.ProjectID != c.ProjectID {
			continue
		}
		if e.ID == "" || e.CreatedAt.IsZero() {
			return deny()
		}
		if ready == nil || e.CreatedAt.After(ready.CreatedAt) {
			ready = e
		} else if e.CreatedAt.Equal(ready.CreatedAt) && e.ID != ready.ID {
			return deny()
		}
	}
	if ready == nil || ready.ActorID != c.OwnerID || ready.Status != c.ReadyStatus || ready.WasAutomated {
		return deny()
	}
	var approval *Comment
	command := "/sofa approve-spec " + digest
	for i := range s.Comments {
		comment := &s.Comments[i]
		if comment.ActorID != c.OwnerID || strings.TrimSpace(comment.Body) != command || comment.ID == "" || comment.CreatedAt.IsZero() {
			continue
		}
		// A post-Ready edit cannot retroactively turn an old comment into approval.
		effective := comment.UpdatedAt
		if effective.Before(comment.CreatedAt) {
			effective = comment.CreatedAt
		}
		if effective.After(ready.CreatedAt) {
			continue
		}
		if approval == nil || effective.After(approval.UpdatedAt) {
			approval = comment
		}
	}
	if approval == nil {
		return deny()
	}
	return Grant{Version: 1, Repository: s.Repository, RepositoryID: s.RepositoryID, IssueID: s.IssueID, IssueNumber: s.Number, ProjectID: c.ProjectID, OwnerID: c.OwnerID, SpecDigest: digest, ConfigDigest: configDigest, BaseSHA: s.BaseSHA, ApprovalID: approval.ID, ReadyEventID: ready.ID}, spec, nil
}

// Revalidate retains the admitted base even when the default branch advances.
// New approval, scope, configuration, or Ready evidence requires explicit work.
func Revalidate(c config.Config, s Snapshot, expected Grant) error {
	current, _, err := Authorize(c, s)
	if err != nil {
		return err
	}
	current.BaseSHA = expected.BaseSHA
	if current != expected {
		return errors.New("admitted authority or scope changed")
	}
	return nil
}
