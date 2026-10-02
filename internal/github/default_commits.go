package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// DefaultCommit is a search hint for discovering a possible revert. Its
// message is untrusted; VerifiedRevert must prove the inverse before recording.
type DefaultCommit struct {
	SHA     string
	Message string
}

type DefaultCommitPage struct {
	Commits []DefaultCommit
	Final   bool
}

// DefaultCommitComparisonPage reads one bounded page of commits reachable from
// an exact head but not from an exact ancestor. The caller persists the page
// number so a revert cannot age out when newer commits arrive.
func (c *Client) DefaultCommitComparisonPage(ctx context.Context, repository, base, head string, page int) (DefaultCommitPage, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || !lifecycleSHAPattern.MatchString(base) || !lifecycleSHAPattern.MatchString(head) || base == head || page < 1 || page > 1_000_000 {
		return DefaultCommitPage{}, errors.New("invalid default comparison identity")
	}
	var raw struct {
		Status       string `json:"status"`
		TotalCommits int    `json:"total_commits"`
		BaseCommit   struct {
			SHA string `json:"sha"`
		} `json:"base_commit"`
		MergeBaseCommit struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
		Commits []struct {
			SHA    string `json:"sha"`
			Commit struct {
				Message string `json:"message"`
			} `json:"commit"`
		} `json:"commits"`
	}
	path := "/repos/" + repository + "/compare/" + url.PathEscape(base) + "..." + url.PathEscape(head) + "?per_page=100&page=" + strconv.Itoa(page)
	if err := c.Request(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return DefaultCommitPage{}, err
	}
	if raw.Status != "ahead" || raw.BaseCommit.SHA != base || raw.MergeBaseCommit.SHA != base || raw.TotalCommits < 1 || raw.TotalCommits > 100_000_000 {
		return DefaultCommitPage{}, errors.New("default commit comparison differs from observed ancestry")
	}
	offset := (page - 1) * 100
	if offset >= raw.TotalCommits {
		return DefaultCommitPage{}, errors.New("default commit comparison page outside range")
	}
	expected := raw.TotalCommits - offset
	if expected > 100 {
		expected = 100
	}
	if len(raw.Commits) != expected {
		return DefaultCommitPage{}, errors.New("default commit comparison page incomplete")
	}
	commits := make([]DefaultCommit, 0, len(raw.Commits))
	seen := make(map[string]bool, len(raw.Commits))
	for _, item := range raw.Commits {
		if !lifecycleSHAPattern.MatchString(item.SHA) || seen[item.SHA] {
			return DefaultCommitPage{}, errors.New("default commit identity unavailable")
		}
		seen[item.SHA] = true
		// A malformed message is only an unusable search hint. It must not
		// hold subsequent valid commits or the durable page cursor.
		if len(item.Commit.Message) > 64<<10 || strings.ContainsRune(item.Commit.Message, '\x00') {
			continue
		}
		commits = append(commits, DefaultCommit{SHA: item.SHA, Message: item.Commit.Message})
	}
	final := offset+len(raw.Commits) == raw.TotalCommits
	if final && raw.Commits[len(raw.Commits)-1].SHA != head {
		return DefaultCommitPage{}, errors.New("default commit comparison does not end at observed head")
	}
	return DefaultCommitPage{Commits: commits, Final: final}, nil
}
