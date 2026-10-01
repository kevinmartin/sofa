package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/discovery"
)

type issueCommentJSON struct {
	ID        int64     `json:"id"`
	IssueURL  string    `json:"issue_url"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		NodeID string `json:"node_id"`
	} `json:"user"`
}

// commentPath returns the issue-comment collection path, rejecting malformed
// repository names and nonpositive issue numbers.
func commentPath(repository string, issue int64) (string, error) {
	if !repositoryPattern.MatchString(repository) || issue < 1 {
		return "", errors.New("invalid issue comment target")
	}
	return "/repos/" + repository + "/issues/" + strconv.FormatInt(issue, 10) + "/comments", nil
}

// commentSnapshot converts a response belonging to the expected issue into
// a comment observation. Missing identity, inconsistent timestamps, or a body
// over 64 KiB returns an error.
func commentSnapshot(repository string, issue int64, raw issueCommentJSON) (discovery.SpecComment, error) {
	expected := fmt.Sprintf("https://api.github.com/repos/%s/issues/%d", repository, issue)
	if raw.ID < 1 || !strings.EqualFold(raw.IssueURL, expected) || raw.User.NodeID == "" || raw.CreatedAt.IsZero() || raw.UpdatedAt.Before(raw.CreatedAt) || len(raw.Body) > 64<<10 {
		return discovery.SpecComment{}, errors.New("issue comment identity or content unavailable")
	}
	return discovery.SpecComment{
		ID:        raw.ID,
		AuthorID:  raw.User.NodeID,
		Body:      raw.Body,
		CreatedAt: raw.CreatedAt,
		UpdatedAt: raw.UpdatedAt,
	}, nil
}

// IssueComment reads one exact comment and verifies it belongs to the issue.
// A same-number comment on another issue/repository cannot inherit approval.
func (c *Client) IssueComment(ctx context.Context, repository string, issue, id int64) (discovery.SpecComment, error) {
	var empty discovery.SpecComment
	if _, err := commentPath(repository, issue); err != nil || id < 1 {
		return empty, errors.New("invalid issue comment identity")
	}
	var raw issueCommentJSON
	if err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/issues/comments/"+strconv.FormatInt(id, 10), nil, &raw); err != nil {
		return empty, err
	}
	if raw.ID != id {
		return empty, errors.New("issue comment ID changed")
	}
	return commentSnapshot(repository, issue, raw)
}

// IssueComments scans a bounded issue-comment history, skipping bodies over
// 64 KiB and comments without an author ID. Request or identity errors and a
// history without a short page within 20 pages of 100 return errors; partial
// results cannot be used to recover a prior POST.
func (c *Client) IssueComments(ctx context.Context, repository string, issue int64) ([]discovery.SpecComment, error) {
	path, err := commentPath(repository, issue)
	if err != nil {
		return nil, err
	}
	comments := make([]discovery.SpecComment, 0)
	for page := 1; page <= 20; page++ {
		var raw []issueCommentJSON
		url := path + "?per_page=100&page=" + strconv.Itoa(page)
		if err := c.Request(ctx, http.MethodGet, url, nil, &raw); err != nil {
			return nil, err
		}
		for _, item := range raw {
			// An unrelated comment cannot be our bounded publication. Keep
			// strict validation for the exact comment fetched by IssueComment.
			if len(item.Body) > 64<<10 || item.User.NodeID == "" {
				continue
			}
			comment, err := commentSnapshot(repository, issue, item)
			if err != nil {
				return nil, err
			}
			comments = append(comments, comment)
		}
		if len(raw) < 100 {
			return comments, nil
		}
	}
	return nil, errors.New("issue comment history exceeds recovery bound")
}

// CreateIssueComment posts a nonempty body of at most 64 KiB using the client
// credential and verifies the returned body and issue identity. Callers must
// validate specification syntax. Request and response-validation errors are
// returned even when the comment may already have been created.
func (c *Client) CreateIssueComment(ctx context.Context, repository string, issue int64, body string) (discovery.SpecComment, error) {
	var empty discovery.SpecComment
	path, err := commentPath(repository, issue)
	if err != nil || body == "" || len(body) > 64<<10 {
		return empty, errors.New("invalid specification comment")
	}
	var raw issueCommentJSON
	if err := c.Request(ctx, http.MethodPost, path, map[string]string{"body": body}, &raw); err != nil {
		return empty, err
	}
	if raw.Body != body {
		return empty, errors.New("published specification comment differs")
	}
	return commentSnapshot(repository, issue, raw)
}
