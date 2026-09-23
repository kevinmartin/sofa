package github

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/kevinmartin/sofa/internal/integrity"
)

// Publisher receives a token scoped to the calling repository only. The caller
// must authenticate artifact and check-job provenance before constructing Input.
// Guard rereads live authority and a fenced owner before each external write.
type PublishInput struct {
	Bundle         integrity.Bundle
	Expected       integrity.Expected
	Policy         integrity.Policy
	Checks         []integrity.CheckEvidence
	RequiredChecks []string
	BaseBranch     string
	Title          string
	Body           string
	Guard          func(context.Context) error
}

type DraftPR struct {
	Number                 int64
	URL, Branch, CommitSHA string
}

func (c *Client) PublishDraft(ctx context.Context, in PublishInput) (DraftPR, error) {
	var empty DraftPR
	if in.Guard == nil {
		return empty, errors.New("publication guard is required")
	}
	if err := integrity.Validate(in.Bundle, in.Expected, in.Policy); err != nil {
		return empty, err
	}
	if err := integrity.ValidateEvidence(in.Bundle, in.Checks, in.RequiredChecks); err != nil {
		return empty, err
	}
	if !safeBranch(in.BaseBranch) || len(in.Title) == 0 || len(in.Title) > 240 || len(in.Body) > 4096 {
		return empty, errors.New("invalid publication metadata")
	}
	repo := in.Bundle.Repository
	branch := "sofa/" + in.Bundle.AttemptID[:24]
	if err := in.Guard(ctx); err != nil {
		return empty, err
	}
	baseHead, err := c.ref(ctx, repo, in.BaseBranch)
	if err != nil {
		return empty, err
	}
	if baseHead != in.Bundle.BaseSHA {
		return empty, errors.New("default branch changed since admission")
	}
	marker := fmt.Sprintf("sofa-attempt=%s; generation=%d; candidate=%s", in.Bundle.AttemptID, in.Bundle.Generation, in.Bundle.CandidateDigest)
	var commitSHA string
	currentBranch, err := c.ref(ctx, repo, branch)
	if err != nil && !isNotFound(err) {
		return empty, err
	}
	if currentBranch != "" {
		if err := c.verifyExisting(ctx, in.Bundle, currentBranch, marker); err != nil {
			return empty, err
		}
		commitSHA = currentBranch
	} else {
		if err := c.verifyBase(ctx, in.Bundle); err != nil {
			return empty, err
		}
		commitSHA, err = c.commitCandidate(ctx, in, marker)
		if err != nil {
			return empty, err
		}
		if err := in.Guard(ctx); err != nil {
			return empty, err
		}
		// Recheck the target immediately before creating a reference. A server-side
		// create is exclusive; a race is resolved by fetching and verifying it.
		baseHead, err = c.ref(ctx, repo, in.BaseBranch)
		if err != nil {
			return empty, err
		}
		if baseHead != in.Bundle.BaseSHA {
			return empty, errors.New("default branch changed before publication")
		}
		var out struct{ Ref string }
		err = c.Request(ctx, http.MethodPost, "/repos/"+repo+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": commitSHA}, &out)
		if err != nil {
			currentBranch, readErr := c.ref(ctx, repo, branch)
			if readErr != nil {
				return empty, err
			}
			if err := c.verifyExisting(ctx, in.Bundle, currentBranch, marker); err != nil {
				return empty, err
			}
			// A competing delivery can create an equivalent commit with a
			// different timestamp. Use the already published, verified head.
			commitSHA = currentBranch
		}
	}
	if err := in.Guard(ctx); err != nil {
		return empty, err
	}
	// This lookup makes crash-after-PR-before-acknowledgement retry idempotent.
	pr, found, err := c.findPR(ctx, repo, branch, in.BaseBranch)
	if err != nil {
		return empty, err
	}
	if found {
		return verifyDraft(pr, repo, branch, commitSHA)
	}
	if err := in.Guard(ctx); err != nil {
		return empty, err
	}
	if head, err := c.ref(ctx, repo, branch); err != nil || head != commitSHA {
		return empty, errors.New("candidate branch changed before PR creation")
	}
	var created prJSON
	err = c.Request(ctx, http.MethodPost, "/repos/"+repo+"/pulls", map[string]any{"title": in.Title, "body": in.Body, "head": branch, "base": in.BaseBranch, "draft": true}, &created)
	if err != nil {
		// GitHub may have committed the PR before a transport error. Reconcile
		// rather than creating another PR on blind retry.
		pr, found, readErr := c.findPR(ctx, repo, branch, in.BaseBranch)
		if readErr == nil && found {
			return verifyDraft(pr, repo, branch, commitSHA)
		}
		return empty, err
	}
	return verifyDraft(created, repo, branch, commitSHA)
}

func safeBranch(name string) bool {
	if name == "" || len(name) > 100 || name[0] == '/' || strings.Contains(name, "..") || strings.ContainsAny(name, "\\~^:?*[ \t\r\n") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '/' || r == '.') {
			return false
		}
	}
	return true
}

func isNotFound(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusNotFound
}

func (c *Client) ref(ctx context.Context, repo, branch string) (string, error) {
	var out struct{ Object struct{ SHA string } }
	err := c.Request(ctx, http.MethodGet, "/repos/"+repo+"/git/ref/heads/"+branch, nil, &out)
	if err != nil {
		return "", err
	}
	if out.Object.SHA == "" {
		return "", errors.New("GitHub reference lacks commit")
	}
	return out.Object.SHA, nil
}

type gitCommit struct {
	SHA     string
	Message string
	Tree    struct{ SHA string }
	Parents []struct{ SHA string }
}

func (c *Client) commit(ctx context.Context, repo, sha string) (gitCommit, error) {
	var out gitCommit
	err := c.Request(ctx, http.MethodGet, "/repos/"+repo+"/git/commits/"+sha, nil, &out)
	if err != nil {
		return out, err
	}
	if out.SHA != sha {
		return gitCommit{}, errors.New("GitHub commit identity mismatch")
	}
	return out, nil
}

func (c *Client) verifyExisting(ctx context.Context, bundle integrity.Bundle, sha, marker string) error {
	repo := bundle.Repository
	commit, err := c.commit(ctx, repo, sha)
	if err != nil {
		return err
	}
	if commit.Message != marker || len(commit.Parents) != 1 || commit.Parents[0].SHA != bundle.BaseSHA {
		return errors.New("existing branch does not match admitted candidate")
	}
	base, err := c.commit(ctx, repo, bundle.BaseSHA)
	if err != nil {
		return err
	}
	oldTree, err := c.tree(ctx, repo, base.Tree.SHA)
	if err != nil {
		return err
	}
	newTree, err := c.tree(ctx, repo, commit.Tree.SHA)
	if err != nil {
		return err
	}
	old := map[string]treeEntry{}
	for _, entry := range oldTree {
		if entry.Type != "tree" {
			old[entry.Path] = entry
		}
	}
	newEntries := map[string]treeEntry{}
	for _, entry := range newTree {
		if entry.Type != "tree" {
			newEntries[entry.Path] = entry
		}
	}
	if len(newEntries)-len(old) > len(bundle.Files) || len(old)-len(newEntries) > len(bundle.Files) {
		return errors.New("existing candidate tree has extra changes")
	}
	changed := map[string]integrity.File{}
	for _, file := range bundle.Files {
		changed[file.Path] = file
	}
	for path, before := range old {
		after, exists := newEntries[path]
		file, expectedChange := changed[path]
		if !expectedChange {
			if !exists || before != after {
				return errors.New("existing candidate tree has unapproved changes")
			}
			continue
		}
		if file.Operation == "delete" {
			if exists {
				return errors.New("candidate delete differs from bundle")
			}
			continue
		}
		if !exists || after.Mode != integrity.RegularMode || after.SHA != blobSHA(file.Content) {
			return errors.New("existing candidate contents differ from bundle")
		}
	}
	for path, after := range newEntries {
		if _, exists := old[path]; exists {
			continue
		}
		file, expectedChange := changed[path]
		if !expectedChange || file.Operation != "add" || after.Mode != integrity.RegularMode || after.SHA != blobSHA(file.Content) {
			return errors.New("existing candidate includes unapproved addition")
		}
	}
	return nil
}

func blobSHA(contents []byte) string {
	data := append([]byte(fmt.Sprintf("blob %d\x00", len(contents))), contents...)
	h := sha1.Sum(data)
	return hex.EncodeToString(h[:])
}

func (c *Client) tree(ctx context.Context, repo, sha string) ([]treeEntry, error) {
	var tree struct {
		Tree      []treeEntry
		Truncated bool
	}
	if sha == "" {
		return nil, errors.New("commit lacks tree")
	}
	if err := c.Request(ctx, http.MethodGet, "/repos/"+repo+"/git/trees/"+sha+"?recursive=1", nil, &tree); err != nil {
		return nil, err
	}
	if tree.Truncated {
		return nil, errors.New("tree listing truncated")
	}
	return tree.Tree, nil
}

type treeEntry struct {
	Path string
	Mode string
	Type string
	SHA  string
}

func (c *Client) verifyBase(ctx context.Context, b integrity.Bundle) error {
	commit, err := c.commit(ctx, b.Repository, b.BaseSHA)
	if err != nil {
		return err
	}
	if commit.Tree.SHA == "" {
		return errors.New("base commit has no tree")
	}
	entriesList, err := c.tree(ctx, b.Repository, commit.Tree.SHA)
	if err != nil {
		return err
	}
	entries := map[string]treeEntry{}
	for _, e := range entriesList {
		entries[e.Path] = e
	}
	return integrity.VerifyBase(b, func(path string) (integrity.BaseFile, bool, error) {
		e, ok := entries[path]
		if !ok {
			return integrity.BaseFile{}, false, nil
		}
		file := integrity.BaseFile{Mode: e.Mode}
		if e.Mode != integrity.RegularMode {
			return file, true, nil
		}
		var blob struct {
			Content  string
			Encoding string
			Size     int
		}
		if err := c.Request(ctx, http.MethodGet, "/repos/"+b.Repository+"/git/blobs/"+e.SHA, nil, &blob); err != nil {
			return integrity.BaseFile{}, false, err
		}
		if blob.Encoding != "base64" || blob.Size < 0 || blob.Size > 1<<20 {
			return integrity.BaseFile{}, false, errors.New("base blob exceeds supported content bounds")
		}
		content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
		if err != nil || len(content) != blob.Size {
			return integrity.BaseFile{}, false, errors.New("base blob content invalid")
		}
		file.Content = content
		return file, true, nil
	})
}

func (c *Client) commitCandidate(ctx context.Context, in PublishInput, marker string) (string, error) {
	repo := in.Bundle.Repository
	base, err := c.commit(ctx, repo, in.Bundle.BaseSHA)
	if err != nil {
		return "", err
	}
	entries := make([]map[string]any, 0, len(in.Bundle.Files))
	for _, file := range in.Bundle.Files {
		if file.Operation == "delete" {
			entries = append(entries, map[string]any{"path": file.Path, "mode": file.Mode, "type": "blob", "sha": nil})
			continue
		}
		if err := in.Guard(ctx); err != nil {
			return "", err
		}
		var blob struct{ SHA string }
		err := c.Request(ctx, http.MethodPost, "/repos/"+repo+"/git/blobs", map[string]any{"content": base64.StdEncoding.EncodeToString(file.Content), "encoding": "base64"}, &blob)
		if err != nil {
			return "", err
		}
		if blob.SHA == "" {
			return "", errors.New("blob creation lacked SHA")
		}
		entries = append(entries, map[string]any{"path": file.Path, "mode": file.Mode, "type": "blob", "sha": blob.SHA})
	}
	if err := in.Guard(ctx); err != nil {
		return "", err
	}
	var tree struct{ SHA string }
	err = c.Request(ctx, http.MethodPost, "/repos/"+repo+"/git/trees", map[string]any{"base_tree": base.Tree.SHA, "tree": entries}, &tree)
	if err != nil {
		return "", err
	}
	if tree.SHA == "" {
		return "", errors.New("tree creation lacked SHA")
	}
	if err := in.Guard(ctx); err != nil {
		return "", err
	}
	var candidate struct{ SHA string }
	err = c.Request(ctx, http.MethodPost, "/repos/"+repo+"/git/commits", map[string]any{"message": marker, "tree": tree.SHA, "parents": []string{in.Bundle.BaseSHA}}, &candidate)
	if err != nil {
		return "", err
	}
	if candidate.SHA == "" {
		return "", errors.New("candidate commit lacked SHA")
	}
	return candidate.SHA, nil
}

type prJSON struct {
	Number  int64  `json:"number"`
	HTMLURL string `json:"html_url"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Head    struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	}
}

func (c *Client) findPR(ctx context.Context, repo, branch, base string) (prJSON, bool, error) {
	var empty prJSON
	owner := strings.Split(repo, "/")[0]
	query := "?state=all&head=" + url.QueryEscape(owner+":"+branch) + "&base=" + url.QueryEscape(base) + "&per_page=100"
	var prs []prJSON
	if err := c.Request(ctx, http.MethodGet, "/repos/"+repo+"/pulls"+query, nil, &prs); err != nil {
		return empty, false, err
	}
	if len(prs) > 1 {
		return empty, false, errors.New("multiple PRs found for candidate branch")
	}
	if len(prs) == 0 {
		return empty, false, nil
	}
	return prs[0], true, nil
}

func verifyDraft(pr prJSON, repo, branch, sha string) (DraftPR, error) {
	if pr.Number <= 0 || pr.HTMLURL == "" || !strings.HasPrefix(pr.HTMLURL, "https://github.com/"+repo+"/pull/") || pr.State != "open" || !pr.Draft || pr.Head.Ref != branch || pr.Head.SHA != sha || pr.Head.Repo.FullName != repo {
		return DraftPR{}, errors.New("published PR is missing, changed, closed, or no longer draft")
	}
	return DraftPR{Number: pr.Number, URL: pr.HTMLURL, Branch: branch, CommitSHA: sha}, nil
}
