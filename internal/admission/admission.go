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

type Snapshot struct {
	Repository        string    `json:"repository"`
	RepositoryID      string    `json:"repository_id"`
	IssueID           string    `json:"issue_id"`
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	Body              string    `json:"body"`
	Open              bool      `json:"open"`
	ProjectID         string    `json:"project_id"`
	ProjectPrivate    bool      `json:"project_private"`
	ProjectItemID     string    `json:"project_item_id"`
	CurrentStatus     string    `json:"current_status"`
	StatusOptionID    string    `json:"status_option_id"`
	StatusUpdatedAt   time.Time `json:"status_updated_at"`
	IssueLastEditedAt time.Time `json:"issue_last_edited_at"`
	BaseSHA           string    `json:"base_sha"`
	Complete          bool      `json:"complete"` // all relevant pagination succeeded
}
type Grant struct {
	Version         int       `json:"schema_version"`
	Repository      string    `json:"repository"`
	RepositoryID    string    `json:"repository_id"`
	IssueID         string    `json:"issue_id"`
	IssueNumber     int       `json:"issue_number"`
	ProjectID       string    `json:"project_id"`
	OwnerID         string    `json:"owner_id"`
	SpecDigest      string    `json:"spec_digest"`
	ConfigDigest    string    `json:"config_digest"`
	BaseSHA         string    `json:"base_sha"`
	ProjectItemID   string    `json:"project_item_id"`
	StatusOptionID  string    `json:"status_option_id"`
	StatusUpdatedAt time.Time `json:"status_updated_at"`
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// CanonicalSpec normalizes line endings and boundary whitespace only. Changes to
// substantive text always require a new Ready transition; no model decides
// whether an edit matters.
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
		return Grant{}, nil, errors.New("current issue lacks an unchanged specification and private Project Ready authorization")
	}
	configDigest, err := c.Digest()
	if err != nil {
		return Grant{}, nil, err
	}
	if !s.Complete || !s.Open || !strings.EqualFold(s.Repository, c.Repository) || s.RepositoryID != c.RepositoryID || s.ProjectID != c.ProjectID || !s.ProjectPrivate || s.ProjectItemID == "" || s.CurrentStatus != c.ReadyStatus || s.StatusOptionID == "" || s.StatusUpdatedAt.IsZero() || (!s.IssueLastEditedAt.IsZero() && !s.IssueLastEditedAt.Before(s.StatusUpdatedAt)) || s.IssueID == "" || s.Number < 1 || !shaPattern.MatchString(s.BaseSHA) {
		return deny()
	}
	spec, digest, err := CanonicalSpec(s.Title, s.Body)
	if err != nil {
		return Grant{}, nil, err
	}
	return Grant{Version: 1, Repository: c.Repository, RepositoryID: s.RepositoryID, IssueID: s.IssueID, IssueNumber: s.Number, ProjectID: c.ProjectID, OwnerID: c.OwnerID, SpecDigest: digest, ConfigDigest: configDigest, BaseSHA: s.BaseSHA, ProjectItemID: s.ProjectItemID, StatusOptionID: s.StatusOptionID, StatusUpdatedAt: s.StatusUpdatedAt}, spec, nil
}

// Revalidate retains the admitted base even when the default branch advances.
// A new status revision, scope, or configuration requires a new issue in v1.
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
