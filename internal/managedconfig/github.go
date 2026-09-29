package managedconfig

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kevinmartin/sofa/internal/github"
)

const configBranch = "sofa/config"

type Reconciler struct {
	GitHub *github.Client
}

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type Result struct {
	Repository    string   `json:"repository"`
	BaseBranch    string   `json:"base_branch"`
	Changes       []Change `json:"changes"`
	PullURL       string   `json:"pull_url,omitempty"`
	ClosedPullURL string   `json:"closed_pull_url,omitempty"`
}

func (c Reconciler) Reconcile(ctx context.Context, spec Spec, apply bool) (Result, error) {
	var result Result
	desired, err := Render(spec)
	if err != nil {
		return result, err
	}
	if apply {
		if err := ValidateActionlint(ctx, desired); err != nil {
			return result, err
		}
	}
	if c.GitHub == nil {
		return result, errors.New("GitHub client unavailable")
	}
	repository, err := c.GitHub.RepositoryInfo(ctx, spec.Repository)
	if err != nil || repository.FullName != spec.Repository || repository.DefaultBranch == "" || repository.Archived || repository.Disabled {
		return result, errors.New("enrolled repository unavailable or inactive")
	}
	result = Result{
		Repository: spec.Repository,
		BaseBranch: repository.DefaultBranch,
		Changes:    []Change{},
	}
	paths := make([]string, 0, len(desired))
	for path := range desired {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		current, sha, err := c.GitHub.Content(ctx, spec.Repository, path, repository.DefaultBranch)
		if err != nil {
			return result, err
		}
		action, err := Difference(current, sha != "", desired[path])
		if err != nil {
			return result, fmt.Errorf("%s: %w", path, err)
		}
		if action != "current" {
			result.Changes = append(result.Changes, Change{
				Path:   path,
				Action: action,
			})
		}
	}
	owner, _, _ := strings.Cut(spec.Repository, "/")
	if len(result.Changes) == 0 {
		pulls, err := c.GitHub.OpenPullRequests(ctx, spec.Repository, owner+":"+configBranch, repository.DefaultBranch)
		if err != nil {
			return result, errors.New("config PR lookup unavailable")
		}
		if len(pulls) > 1 {
			return result, errors.New("multiple config PRs for dedicated branch")
		}
		if len(pulls) == 1 {
			result.PullURL = pulls[0].HTMLURL
			if apply {
				if !c.GitHub.Authenticated() {
					return result, errors.New("SOFA_CONFIG_TOKEN is required to close a stale config PR")
				}
				if err := c.GitHub.ClosePullRequest(ctx, spec.Repository, pulls[0].Number); err != nil {
					return result, err
				}
				result.ClosedPullURL = result.PullURL
				result.PullURL = ""
			}
		}
		return result, nil
	}
	if !apply {
		return result, nil
	}
	if !c.GitHub.Authenticated() {
		return result, errors.New("SOFA_CONFIG_TOKEN is required to open a config PR")
	}
	branchSHA, branchFound, err := c.GitHub.Reference(ctx, spec.Repository, configBranch)
	if err != nil {
		return result, err
	}
	if !branchFound {
		baseSHA, baseFound, err := c.GitHub.Reference(ctx, spec.Repository, repository.DefaultBranch)
		if err != nil || !baseFound || baseSHA == "" {
			return result, errors.New("default branch reference unavailable")
		}
		if err := c.GitHub.CreateReference(ctx, spec.Repository, configBranch, baseSHA); err != nil {
			return result, err
		}
	} else if branchSHA == "" {
		return result, errors.New("config branch reference unavailable")
	}
	// Refuse a reused branch with unrelated changes. This branch is dedicated to
	// the two rendered files and must never carry an arbitrary issue patch.
	comparison, err := c.GitHub.ChangedFiles(ctx, spec.Repository, repository.DefaultBranch, configBranch)
	if err != nil {
		return result, errors.New("config branch comparison unavailable")
	}
	if comparison.BehindBy != 0 {
		return result, errors.New("config branch is behind default branch")
	}
	if len(comparison.Files) > len(paths) {
		return result, errors.New("config branch contains unrelated changes")
	}
	for _, file := range comparison.Files {
		if !slices.Contains(paths, file) {
			return result, errors.New("config branch contains unrelated changes")
		}
	}
	for _, path := range paths {
		content, sha, err := c.GitHub.Content(ctx, spec.Repository, path, configBranch)
		if err != nil {
			return result, err
		}
		action, err := Difference(content, sha != "", desired[path])
		if err != nil {
			return result, fmt.Errorf("config branch %s: %w", path, err)
		}
		if action == "current" {
			continue
		}
		if err := c.GitHub.PutContent(ctx, spec.Repository, path, configBranch, sha, desired[path], "chore: reconcile sofa-managed configuration"); err != nil {
			return result, err
		}
	}
	pulls, err := c.GitHub.OpenPullRequests(ctx, spec.Repository, owner+":"+configBranch, repository.DefaultBranch)
	if err != nil {
		return result, errors.New("config PR lookup unavailable")
	}
	if len(pulls) > 1 {
		return result, errors.New("multiple config PRs for dedicated branch")
	}
	if len(pulls) == 1 {
		result.PullURL = pulls[0].HTMLURL
		return result, nil
	}
	url, err := c.GitHub.CreatePullRequest(ctx, spec.Repository,
		"Reconcile sofa-managed quality and dependency configuration",
		configBranch, repository.DefaultBranch,
		"Sofa detected drift from its reviewed repository manifest. This PR updates only the managed quality caller and Dependabot configuration. Review and merge under this repository's policy.")
	if err != nil || url == "" {
		return result, errors.New("unable to create config PR")
	}
	result.PullURL = url
	return result, nil
}
