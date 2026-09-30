package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// DefaultCommit is a bounded hint for discovering a possible revert. Its
// message is untrusted; VerifiedRevert must prove the inverse before recording.
type DefaultCommit struct {
	SHA     string
	Message string
}

// RecentDefaultCommits reads only the latest 100 commits reachable from the
// exact observed default head. It does not claim historical completeness.
func (c *Client) RecentDefaultCommits(ctx context.Context, repository, defaultHead string) ([]DefaultCommit, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || !lifecycleSHAPattern.MatchString(defaultHead) {
		return nil, errors.New("invalid default commit identity")
	}
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
		} `json:"commit"`
	}
	path := "/repos/" + repository + "/commits?sha=" + url.QueryEscape(defaultHead) + "&per_page=100&page=1"
	if err := c.Request(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 || raw[0].SHA != defaultHead || len(raw) > 100 {
		return nil, errors.New("default commit page differs from observed head")
	}
	commits := make([]DefaultCommit, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		if !lifecycleSHAPattern.MatchString(item.SHA) || seen[item.SHA] || len(item.Commit.Message) > 64<<10 || strings.ContainsRune(item.Commit.Message, '\x00') {
			return nil, errors.New("default commit identity unavailable")
		}
		seen[item.SHA] = true
		commits = append(commits, DefaultCommit{
			SHA:     item.SHA,
			Message: item.Commit.Message,
		})
	}
	return commits, nil
}
