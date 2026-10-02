package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var shaPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var releaseChannelPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)$`)
var releaseExactPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

// ReleaseRecord is the bounded public identity used by distribution automation.
type ReleaseRecord struct {
	ID         int64          `json:"id"`
	Tag        string         `json:"tag_name"`
	Source     string         `json:"target_commitish"`
	Body       string         `json:"body"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Immutable  bool           `json:"immutable"`
	Assets     []ReleaseAsset `json:"assets"`
}

type ReleaseAsset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	State  string `json:"state"`
}

func (c *Client) ReleasesPage(ctx context.Context, repository string, page int) ([]ReleaseRecord, error) {
	if !repositoryPattern.MatchString(repository) || page < 1 || page > 100 {
		return nil, errors.New("invalid release page")
	}
	var records []ReleaseRecord
	err := c.Request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", repository, page), nil, &records)
	if len(records) > 100 {
		return nil, errors.New("release page exceeds bound")
	}
	return records, err
}

func (c *Client) ReleaseRecordByTag(ctx context.Context, repository, tag string) (ReleaseRecord, bool, error) {
	if !repositoryPattern.MatchString(repository) || !releaseExactPattern.MatchString(tag) {
		return ReleaseRecord{}, false, errors.New("invalid release identity")
	}
	var record ReleaseRecord
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/releases/tags/"+url.PathEscape(tag), nil, &record)
	if !notFound(err) {
		return record, true, err
	}
	if !c.Authenticated() {
		return ReleaseRecord{}, false, nil
	}
	// REST's tag endpoint exposes only published releases. Query the pending
	// draft identity, then fetch its full metadata by ID to recover interrupted
	// uploads. This matches GitHub CLI's own draft-release lookup behavior.
	parts := strings.Split(repository, "/")
	var response struct {
		Repository *struct {
			Release *struct {
				ID    int64 `json:"databaseId"`
				Draft bool  `json:"isDraft"`
			} `json:"release"`
		} `json:"repository"`
	}
	query := `query($owner:String!,$name:String!,$tag:String!){repository(owner:$owner,name:$name){release(tagName:$tag){databaseId isDraft}}}`
	if err := c.GraphQL(ctx, query, map[string]any{"owner": parts[0], "name": parts[1], "tag": tag}, &response); err != nil {
		return ReleaseRecord{}, false, err
	}
	if response.Repository == nil || response.Repository.Release == nil {
		return ReleaseRecord{}, false, nil
	}
	draft := response.Repository.Release
	if draft.ID < 1 || !draft.Draft {
		return ReleaseRecord{}, false, errors.New("pending release identity changed during lookup")
	}
	if err := c.Request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/releases/%d", repository, draft.ID), nil, &record); err != nil {
		return ReleaseRecord{}, false, err
	}
	if record.ID != draft.ID || record.Tag != tag {
		return ReleaseRecord{}, false, errors.New("pending release identity mismatch")
	}
	return record, true, nil
}

func (c *Client) CreateDraftRelease(ctx context.Context, repository, tag, source, body string) (ReleaseRecord, error) {
	if !repositoryPattern.MatchString(repository) || !releaseExactPattern.MatchString(tag) || !shaPattern.MatchString(source) || len(body) > 4096 {
		return ReleaseRecord{}, errors.New("invalid draft release identity")
	}
	var record ReleaseRecord
	err := c.Request(ctx, http.MethodPost, "/repos/"+repository+"/releases", map[string]any{
		"tag_name": tag, "target_commitish": source, "name": tag, "body": body, "draft": true, "prerelease": false, "make_latest": "false",
	}, &record)
	return record, err
}

func (c *Client) PublishDraftRelease(ctx context.Context, repository string, id int64) (ReleaseRecord, error) {
	if !repositoryPattern.MatchString(repository) || id < 1 {
		return ReleaseRecord{}, errors.New("invalid draft release identity")
	}
	var record ReleaseRecord
	err := c.Request(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/releases/%d", repository, id), map[string]any{"draft": false, "make_latest": "false"}, &record)
	return record, err
}

func (c *Client) ImmutableReleasesEnabled(ctx context.Context, repository string) (bool, error) {
	if !repositoryPattern.MatchString(repository) {
		return false, errors.New("invalid immutable release repository")
	}
	var settings struct {
		Enabled bool `json:"enabled"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/immutable-releases", nil, &settings)
	if notFound(err) {
		return false, nil
	}
	return settings.Enabled, err
}

// UploadReleaseAsset derives the only credentialed upload host from fixed API
// constants rather than accepting a release's remote upload_url or redirect.
func (c *Client) UploadReleaseAsset(ctx context.Context, repository string, id int64, name string, content []byte) (ReleaseAsset, error) {
	if !repositoryPattern.MatchString(repository) || id < 1 || (name != "sofa-linux-amd64.tar.gz" && name != "release.json") || len(content) == 0 || len(content) > 128<<20 {
		return ReleaseAsset{}, errors.New("invalid release asset")
	}
	u := fmt.Sprintf("https://uploads.github.com/repos/%s/releases/%d/assets?name=%s", repository, id, url.QueryEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(content))
	if err != nil {
		return ReleaseAsset{}, errors.New("construct release asset upload")
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ReleaseAsset{}, ctx.Err()
		}
		return ReleaseAsset{}, errors.New("release upload unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return ReleaseAsset{}, &APIError{Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return ReleaseAsset{}, errors.New("release upload response unavailable")
	}
	var asset ReleaseAsset
	if json.Unmarshal(data, &asset) != nil || asset.ID < 1 || asset.Name != name || asset.Size != int64(len(content)) || asset.State != "uploaded" {
		return ReleaseAsset{}, errors.New("release upload identity mismatch")
	}
	return asset, nil
}

// DeleteEmptyDraftUpload removes only GitHub's empty upstream-failure starter
// placeholder. Recheck the reserved draft and exact asset immediately before
// deletion. This is not an asset-replacement API; published/uploaded/nonempty
// assets and changed reservations always fail closed. Release serialization
// and exclusive writer rulesets still apply because DELETE has no CAS field.
func (c *Client) DeleteEmptyDraftUpload(ctx context.Context, repository string, expected ReleaseRecord, asset ReleaseAsset) error {
	if !repositoryPattern.MatchString(repository) || expected.ID < 1 || !releaseExactPattern.MatchString(expected.Tag) || !shaPattern.MatchString(expected.Source) || !expected.Draft || expected.Immutable || expected.Prerelease || !strings.HasPrefix(expected.Body, "sofa-release-plan:v1\n") || len(expected.Body) > 4096 || asset.ID < 1 || (asset.Name != "sofa-linux-amd64.tar.gz" && asset.Name != "release.json") || asset.State != "starter" || asset.Size != 0 || asset.Digest != "" {
		return errors.New("invalid empty draft upload cleanup")
	}
	var current ReleaseRecord
	if err := c.Request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/releases/%d", repository, expected.ID), nil, &current); err != nil {
		return err
	}
	if current.ID != expected.ID || current.Tag != expected.Tag || current.Source != expected.Source || current.Body != expected.Body || !current.Draft || current.Immutable || current.Prerelease {
		return errors.New("draft identity changed before upload cleanup")
	}
	found := false
	for _, candidate := range current.Assets {
		if candidate.ID != asset.ID && candidate.Name != asset.Name {
			continue
		}
		if found || candidate != asset {
			return errors.New("draft upload changed before cleanup")
		}
		found = true
	}
	if !found {
		return errors.New("empty draft upload unavailable for cleanup")
	}
	err := c.Request(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/releases/assets/%d", repository, asset.ID), nil, nil)
	if notFound(err) {
		return nil // Another authorized retry already removed the same empty ID.
	}
	return err
}

func validReleaseRef(tag string) bool {
	if len(tag) < 2 || len(tag) > 80 || tag[0] != 'v' {
		return false
	}
	for _, r := range tag[1:] {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// ReleaseTag resolves lightweight and annotated tags to their commit identity.
func (c *Client) ReleaseTag(ctx context.Context, repository, tag string) (string, bool, error) {
	if !repositoryPattern.MatchString(repository) || !validReleaseRef(tag) {
		return "", false, errors.New("invalid release tag")
	}
	var ref struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		} `json:"object"`
	}
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/git/ref/tags/"+url.PathEscape(tag), nil, &ref)
	if notFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for depth := 0; depth < 4; depth++ {
		if !shaPattern.MatchString(ref.Object.SHA) {
			return "", false, errors.New("release tag object identity invalid")
		}
		if ref.Object.Type == "commit" {
			return ref.Object.SHA, true, nil
		}
		if ref.Object.Type != "tag" {
			return "", false, errors.New("release tag is not a commit")
		}
		if err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/git/tags/"+ref.Object.SHA, nil, &ref); err != nil {
			return "", false, err
		}
	}
	return "", false, errors.New("release tag nesting exceeds bound")
}

// UpdateReleaseChannel must run only in the serialized release workflow, with
// rulesets excluding other writers. GitHub's refs REST API has no CAS field.
// Recheck expected prior identity before its atomic direct update, never delete.
func (c *Client) UpdateReleaseChannel(ctx context.Context, repository, tag, expected, source string) error {
	if !repositoryPattern.MatchString(repository) || !releaseChannelPattern.MatchString(tag) || !shaPattern.MatchString(source) || (expected != "" && !shaPattern.MatchString(expected)) {
		return errors.New("invalid release channel update")
	}
	current, found, err := c.ReleaseTag(ctx, repository, tag)
	if err != nil {
		return err
	}
	if current == source && found {
		return nil
	}
	if current != expected || found != (expected != "") {
		return errors.New("release channel changed before promotion")
	}
	if !found {
		return c.Request(ctx, http.MethodPost, "/repos/"+repository+"/git/refs", map[string]string{"ref": "refs/tags/" + tag, "sha": source}, nil)
	}
	return c.Request(ctx, http.MethodPatch, "/repos/"+repository+"/git/refs/tags/"+url.PathEscape(tag), map[string]any{"sha": source, "force": true}, nil)
}

type ReleaseCanaryRun struct {
	ID         int64     `json:"id"`
	Title      string    `json:"display_title"`
	Event      string    `json:"event"`
	Path       string    `json:"path"`
	HeadSHA    string    `json:"head_sha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	CreatedAt  time.Time `json:"created_at"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	HeadRepository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

func (c *Client) DispatchReleaseCanary(ctx context.Context, repository, ref, version, source, correlation, releaseRunID, releaseRunAttempt string) error {
	if repository != "kevinmartin/sofa-disposable" || ref == "" || !validReleaseRef(version) || !shaPattern.MatchString(source) || !shaPattern.MatchString(correlation) {
		return errors.New("invalid release canary dispatch")
	}
	if id, err := strconv.ParseInt(releaseRunID, 10, 64); err != nil || id < 1 {
		return errors.New("invalid release run identity")
	}
	if attempt, err := strconv.Atoi(releaseRunAttempt); err != nil || attempt < 1 {
		return errors.New("invalid release attempt identity")
	}
	return c.Request(ctx, http.MethodPost, "/repos/"+repository+"/actions/workflows/sofa-release-canary.yml/dispatches", map[string]any{"ref": ref, "inputs": map[string]string{"release_version": version, "source_sha": source, "correlation": correlation, "release_run_id": releaseRunID, "release_run_attempt": releaseRunAttempt}}, nil)
}

func (c *Client) ReleaseCanaryRuns(ctx context.Context, repository string, page int, since time.Time) ([]ReleaseCanaryRun, error) {
	if repository != "kevinmartin/sofa-disposable" || page < 1 || page > 10 || since.IsZero() || since.After(time.Now().UTC()) {
		return nil, errors.New("invalid release canary page")
	}
	var response struct {
		Runs []ReleaseCanaryRun `json:"workflow_runs"`
	}
	created := url.QueryEscape(">=" + since.UTC().Format(time.RFC3339))
	err := c.Request(ctx, http.MethodGet, "/repos/"+repository+"/actions/workflows/sofa-release-canary.yml/runs?event=workflow_dispatch&per_page=100&page="+strconv.Itoa(page)+"&created="+created, nil, &response)
	if len(response.Runs) > 100 {
		return nil, errors.New("release canary page exceeds bound")
	}
	return response.Runs, err
}

func (c *Client) ReleaseCanaryRun(ctx context.Context, repository string, id int64) (ReleaseCanaryRun, error) {
	if repository != "kevinmartin/sofa-disposable" || id < 1 {
		return ReleaseCanaryRun{}, errors.New("invalid release canary identity")
	}
	var run ReleaseCanaryRun
	err := c.Request(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repository, id), nil, &run)
	return run, err
}
