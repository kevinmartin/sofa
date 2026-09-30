package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	lifecycleRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	lifecycleSHAPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// PullSnapshot is a current, repository-bound view of a published PR. Review
// and release callers must compare it with the ledger before taking action.
type PullSnapshot struct {
	Number         int64
	URL            string
	State          string
	Draft          bool
	Merged         bool
	MergeCommitSHA string
	MergedAt       time.Time
	HeadSHA        string
	HeadRef        string
	HeadRepository string
	BaseSHA        string
	BaseRef        string
	BaseRepository string
	UpdatedAt      time.Time
}

func (c *Client) Pull(ctx context.Context, repository string, number int64) (PullSnapshot, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || number < 1 {
		return PullSnapshot{}, errors.New("invalid pull request identity")
	}
	var response struct {
		Number         int64     `json:"number"`
		HTMLURL        string    `json:"html_url"`
		State          string    `json:"state"`
		Draft          bool      `json:"draft"`
		Merged         bool      `json:"merged"`
		MergeCommitSHA string    `json:"merge_commit_sha"`
		MergedAt       time.Time `json:"merged_at"`
		UpdatedAt      time.Time `json:"updated_at"`
		Head           struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			SHA  string `json:"sha"`
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	path := "/repos/" + repository + "/pulls/" + strconv.FormatInt(number, 10)
	if err := c.Request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return PullSnapshot{}, err
	}
	expectedURL := fmt.Sprintf("https://github.com/%s/pull/%d", repository, number)
	if response.Number != number || !strings.EqualFold(response.HTMLURL, expectedURL) || !lifecycleSHAPattern.MatchString(response.Head.SHA) || !lifecycleSHAPattern.MatchString(response.Base.SHA) || response.Head.Ref == "" || response.Base.Ref == "" || !strings.EqualFold(response.Head.Repo.FullName, repository) || !strings.EqualFold(response.Base.Repo.FullName, repository) || (response.State != "open" && response.State != "closed") || response.UpdatedAt.IsZero() {
		return PullSnapshot{}, errors.New("pull request identity or revision unavailable")
	}
	if response.Merged && (response.State != "closed" || !lifecycleSHAPattern.MatchString(response.MergeCommitSHA) || response.MergedAt.IsZero()) {
		return PullSnapshot{}, errors.New("merged pull request lacks a merge commit")
	}
	return PullSnapshot{
		Number:         response.Number,
		URL:            response.HTMLURL,
		State:          response.State,
		Draft:          response.Draft,
		Merged:         response.Merged,
		MergeCommitSHA: response.MergeCommitSHA,
		MergedAt:       response.MergedAt,
		HeadSHA:        response.Head.SHA,
		HeadRef:        response.Head.Ref,
		HeadRepository: response.Head.Repo.FullName,
		BaseSHA:        response.Base.SHA,
		BaseRef:        response.Base.Ref,
		BaseRepository: response.Base.Repo.FullName,
		UpdatedAt:      response.UpdatedAt,
	}, nil
}

// PullReview represents a submitted review, never an authorization inferred
// from the review body. UserID is GitHub's immutable user node ID.
type PullReview struct {
	ID          int64
	UserID      string
	State       string
	CommitSHA   string
	Body        string
	SubmittedAt time.Time
}

func (c *Client) PullReviews(ctx context.Context, repository string, number int64) ([]PullReview, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || number < 1 {
		return nil, errors.New("invalid pull request identity")
	}
	var reviews []PullReview
	for page := 1; page <= 20; page++ {
		var response []struct {
			ID          int64     `json:"id"`
			State       string    `json:"state"`
			CommitID    string    `json:"commit_id"`
			Body        string    `json:"body"`
			SubmittedAt time.Time `json:"submitted_at"`
			User        struct {
				NodeID string `json:"node_id"`
			} `json:"user"`
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100&page=%d", repository, number, page)
		if err := c.Request(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response {
			if item.ID < 1 {
				return nil, errors.New("pull request review identity unavailable")
			}
			// Pending reviews, deleted users and oversized feedback cannot grant
			// repair authority; skip them without hiding later valid reviews.
			if item.User.NodeID == "" || item.SubmittedAt.IsZero() || len(item.Body) > 64<<10 {
				continue
			}
			reviews = append(reviews, PullReview{
				ID:          item.ID,
				UserID:      item.User.NodeID,
				State:       item.State,
				CommitSHA:   item.CommitID,
				Body:        item.Body,
				SubmittedAt: item.SubmittedAt,
			})
		}
		if len(response) < 100 {
			return reviews, nil
		}
	}
	return nil, errors.New("pull request review history exceeds bound")
}

// CommitCheck is one GitHub check run fetched for an exact commit SHA.
type CommitCheck struct {
	Name      string
	State     string
	AppID     int64
	UpdatedAt time.Time
	SourceID  int64
}

func (c *Client) CommitChecks(ctx context.Context, repository, sha string) ([]CommitCheck, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || !lifecycleSHAPattern.MatchString(sha) {
		return nil, errors.New("invalid commit check identity")
	}
	var checks []CommitCheck
	for page := 1; page <= 20; page++ {
		var response struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				ID          int64      `json:"id"`
				Name        string     `json:"name"`
				Status      string     `json:"status"`
				Conclusion  string     `json:"conclusion"`
				CompletedAt *time.Time `json:"completed_at"`
				StartedAt   *time.Time `json:"started_at"`
				App         struct {
					ID int64 `json:"id"`
				} `json:"app"`
			} `json:"check_runs"`
		}
		path := fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repository, sha, page)
		if err := c.Request(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		if response.TotalCount < 0 || response.TotalCount > 2000 {
			return nil, errors.New("commit check history exceeds bound")
		}
		for _, run := range response.CheckRuns {
			if run.ID < 1 || run.Name == "" || run.App.ID < 1 {
				return nil, errors.New("commit check identity unavailable")
			}
			state := run.Status
			if run.Status == "completed" {
				state = run.Conclusion
			}
			updated := run.StartedAt
			if run.CompletedAt != nil {
				updated = run.CompletedAt
			}
			if updated == nil || updated.IsZero() {
				return nil, errors.New("commit check timestamp unavailable")
			}
			checks = append(checks, CommitCheck{
				Name:      run.Name,
				State:     state,
				AppID:     run.App.ID,
				UpdatedAt: *updated,
				SourceID:  run.ID,
			})
		}
		if len(response.CheckRuns) < 100 {
			return checks, nil
		}
	}
	return nil, errors.New("commit check history exceeds bound")
}

func (c *Client) IsAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || !lifecycleSHAPattern.MatchString(ancestor) || !lifecycleSHAPattern.MatchString(descendant) {
		return false, errors.New("invalid commit ancestry identity")
	}
	var comparison struct {
		Status string `json:"status"`
	}
	path := "/repos/" + repository + "/compare/" + url.PathEscape(ancestor) + "..." + url.PathEscape(descendant)
	if err := c.Request(ctx, http.MethodGet, path, nil, &comparison); err != nil {
		return false, err
	}
	switch comparison.Status {
	case "identical", "ahead":
		return true, nil
	case "behind", "diverged":
		return false, nil
	default:
		return false, errors.New("commit ancestry unavailable")
	}
}
