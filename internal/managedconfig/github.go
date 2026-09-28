package managedconfig

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const configBranch = "sofa/config"

type Client struct {
	HTTP   *http.Client
	APIURL string
	Token  string
}

type Change struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

type Result struct {
	Repository string   `json:"repository"`
	BaseBranch string   `json:"base_branch"`
	Changes    []Change `json:"changes"`
	PullURL    string   `json:"pull_url,omitempty"`
}

type remoteFile struct {
	SHA      string `json:"sha"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	Size     int    `json:"size"`
}

func (c Client) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	base := c.APIURL
	if base == "" {
		base = "https://api.github.com"
	}
	var data io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		data = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, data)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && method == http.MethodGet {
		return response.StatusCode, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
			return response.StatusCode, errors.New("invalid GitHub API response")
		}
	}
	return response.StatusCode, nil
}

func (c Client) file(ctx context.Context, repository, path, ref string) ([]byte, string, error) {
	var file remoteFile
	endpoint := "/repos/" + repository + "/contents/" + path + "?ref=" + url.QueryEscape(ref)
	status, err := c.request(ctx, http.MethodGet, endpoint, nil, &file)
	if status == http.StatusNotFound {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if file.Encoding != "base64" || file.Size < 0 || file.Size > 128<<10 || file.SHA == "" {
		return nil, "", errors.New("repository file is unavailable or too large")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || len(content) != file.Size {
		return nil, "", errors.New("repository file encoding invalid")
	}
	return content, file.SHA, nil
}

func (c Client) Reconcile(ctx context.Context, spec Spec, apply bool) (Result, error) {
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
	var repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
		Archived      bool   `json:"archived"`
		Disabled      bool   `json:"disabled"`
	}
	status, err := c.request(ctx, http.MethodGet, "/repos/"+spec.Repository, nil, &repository)
	if err != nil || status != http.StatusOK || repository.FullName != spec.Repository || repository.DefaultBranch == "" || repository.Archived || repository.Disabled {
		return result, errors.New("enrolled repository unavailable or inactive")
	}
	result = Result{Repository: spec.Repository, BaseBranch: repository.DefaultBranch, Changes: []Change{}}
	paths := make([]string, 0, len(desired))
	for path := range desired {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		current, _, err := c.file(ctx, spec.Repository, path, repository.DefaultBranch)
		if err != nil {
			return result, err
		}
		action, err := Difference(current, desired[path])
		if err != nil {
			return result, fmt.Errorf("%s: %w", path, err)
		}
		if action != "current" {
			result.Changes = append(result.Changes, Change{Path: path, Action: action})
		}
	}
	if !apply || len(result.Changes) == 0 {
		return result, nil
	}
	if c.Token == "" {
		return result, errors.New("SOFA_CONFIG_TOKEN is required to open a config PR")
	}
	var branch struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, err = c.request(ctx, http.MethodGet, "/repos/"+spec.Repository+"/git/ref/heads/"+configBranch, nil, &branch)
	if err != nil {
		return result, err
	}
	if status == http.StatusNotFound {
		var base struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		status, err = c.request(ctx, http.MethodGet, "/repos/"+spec.Repository+"/git/ref/heads/"+repository.DefaultBranch, nil, &base)
		if err != nil || status != http.StatusOK || base.Object.SHA == "" {
			return result, errors.New("default branch reference unavailable")
		}
		_, err = c.request(ctx, http.MethodPost, "/repos/"+spec.Repository+"/git/refs", map[string]string{"ref": "refs/heads/" + configBranch, "sha": base.Object.SHA}, nil)
		if err != nil {
			return result, err
		}
	} else if branch.Object.SHA == "" {
		return result, errors.New("config branch reference unavailable")
	}
	// Refuse a reused branch with unrelated changes. This branch is dedicated to
	// the two rendered files and must never carry an arbitrary issue patch.
	var comparison struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	status, err = c.request(ctx, http.MethodGet, "/repos/"+spec.Repository+"/compare/"+repository.DefaultBranch+"..."+configBranch, nil, &comparison)
	if err != nil || status != http.StatusOK {
		return result, errors.New("config branch comparison unavailable")
	}
	if len(comparison.Files) > len(paths) {
		return result, errors.New("config branch contains unrelated changes")
	}
	for _, file := range comparison.Files {
		if !slices.Contains(paths, file.Filename) {
			return result, errors.New("config branch contains unrelated changes")
		}
	}
	for _, path := range paths {
		content, sha, err := c.file(ctx, spec.Repository, path, configBranch)
		if err != nil {
			return result, err
		}
		action, err := Difference(content, desired[path])
		if err != nil {
			return result, fmt.Errorf("config branch %s: %w", path, err)
		}
		if action == "current" {
			continue
		}
		body := map[string]string{"message": "chore: reconcile sofa-managed configuration", "content": base64.StdEncoding.EncodeToString(desired[path]), "branch": configBranch}
		if sha != "" {
			body["sha"] = sha
		}
		if _, err := c.request(ctx, http.MethodPut, "/repos/"+spec.Repository+"/contents/"+path, body, nil); err != nil {
			return result, err
		}
	}
	owner := strings.SplitN(spec.Repository, "/", 2)[0]
	var pulls []struct {
		HTMLURL string `json:"html_url"`
	}
	status, err = c.request(ctx, http.MethodGet, "/repos/"+spec.Repository+"/pulls?state=open&head="+url.QueryEscape(owner+":"+configBranch)+"&base="+url.QueryEscape(repository.DefaultBranch), nil, &pulls)
	if err != nil || status != http.StatusOK {
		return result, errors.New("config PR lookup unavailable")
	}
	if len(pulls) > 1 {
		return result, errors.New("multiple config PRs for dedicated branch")
	}
	if len(pulls) == 1 {
		result.PullURL = pulls[0].HTMLURL
		return result, nil
	}
	var pull struct {
		HTMLURL string `json:"html_url"`
	}
	_, err = c.request(ctx, http.MethodPost, "/repos/"+spec.Repository+"/pulls", map[string]string{
		"title": "Reconcile sofa-managed quality and dependency configuration",
		"head":  configBranch,
		"base":  repository.DefaultBranch,
		"body":  "Sofa detected drift from its reviewed repository manifest. This PR updates only the managed quality caller and Dependabot configuration. Review and merge under this repository's policy.",
	}, &pull)
	if err != nil || pull.HTMLURL == "" {
		return result, errors.New("unable to create config PR")
	}
	result.PullURL = pull.HTMLURL
	return result, nil
}
