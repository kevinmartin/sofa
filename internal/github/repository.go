package github

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// RepositoryInfo contains the fields needed to reconcile managed files.
type RepositoryInfo struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
}

func (c *Client) RepositoryInfo(ctx context.Context, repository string) (RepositoryInfo, error) {
	var info RepositoryInfo
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository, nil, &info)
	return info, err
}

// Content returns nil content and an empty SHA when the path does not exist.
func (c *Client) Content(ctx context.Context, repository, path, ref string) ([]byte, string, error) {
	var file struct {
		SHA      string `json:"sha"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/contents/"+path+"?ref="+url.QueryEscape(ref), nil, &file)
	if notFound(err) {
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

// Reference distinguishes a missing branch from a malformed existing ref.
func (c *Client) Reference(ctx context.Context, repository, branch string) (string, bool, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/git/ref/heads/"+branch, nil, &ref)
	if notFound(err) {
		return "", false, nil
	}
	return ref.Object.SHA, true, err
}

func (c *Client) CreateReference(ctx context.Context, repository, branch, sha string) error {
	return c.Request(ctx, http.MethodPost, "/repos/"+repository+"/git/refs", map[string]string{
		"ref": "refs/heads/" + branch,
		"sha": sha,
	}, nil)
}

func (c *Client) ChangedFiles(ctx context.Context, repository, base, head string) ([]string, error) {
	var comparison struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/compare/"+base+"..."+head, nil, &comparison)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(comparison.Files))
	for _, file := range comparison.Files {
		files = append(files, file.Filename)
	}
	return files, nil
}

func (c *Client) PutContent(ctx context.Context, repository, path, branch, sha string, content []byte, message string) error {
	input := map[string]string{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(content),
		"branch":  branch,
	}
	if sha != "" {
		input["sha"] = sha
	}
	return c.Request(ctx, http.MethodPut, "/repos/"+repository+"/contents/"+path, input, nil)
}

func (c *Client) OpenPullRequests(ctx context.Context, repository, head, base string) ([]string, error) {
	var pulls []struct {
		HTMLURL string `json:"html_url"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/pulls?state=open&head="+url.QueryEscape(head)+"&base="+url.QueryEscape(base), nil, &pulls)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(pulls))
	for _, pull := range pulls {
		urls = append(urls, pull.HTMLURL)
	}
	return urls, nil
}

func (c *Client) CreatePullRequest(ctx context.Context, repository, title, head, base, body string) (string, error) {
	var pull struct {
		HTMLURL string `json:"html_url"`
	}
	err := c.Request(ctx, http.MethodPost, "/repos/"+repository+"/pulls", map[string]string{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}, &pull)
	return pull.HTMLURL, err
}

func notFound(err error) bool {
	var apiError *APIError
	return errors.As(err, &apiError) && apiError.Status == http.StatusNotFound
}
