package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/integrity"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}
}

type publishFixture struct {
	branch        bool
	pr            bool
	mutations     int
	guards        int
	extra         bool
	commitMessage string
	content       []byte
}

const testRepo = "owner/fixture"
const testBase = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const testTreeBase = "cccccccccccccccccccccccccccccccccccccccc"
const testTreeNew = "dddddddddddddddddddddddddddddddddddddddd"
const testOldBlob = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func (f *publishFixture) trip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer fixture-token" {
		return jsonResponse(403, map[string]any{"message": "auth"}), nil
	}
	if r.Method == http.MethodPost {
		f.mutations++
	}
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/ref/heads/main"):
		return jsonResponse(200, map[string]any{"object": map[string]any{"sha": testBase}}), nil
	case r.Method == http.MethodGet && strings.Contains(p, "/git/ref/heads/sofa/"):
		if !f.branch {
			return jsonResponse(404, map[string]any{"message": "missing"}), nil
		}
		return jsonResponse(200, map[string]any{"object": map[string]any{"sha": testCommit}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/commits/"+testBase):
		return jsonResponse(200, map[string]any{"sha": testBase, "tree": map[string]any{"sha": testTreeBase}, "parents": []any{}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/commits/"+testCommit):
		return jsonResponse(200, map[string]any{"sha": testCommit, "message": f.commitMessage, "tree": map[string]any{"sha": testTreeNew}, "parents": []any{map[string]any{"sha": testBase}}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/trees/"+testTreeBase):
		return jsonResponse(200, map[string]any{"tree": []any{map[string]any{"path": "fixture/a.go", "mode": "100644", "type": "blob", "sha": testOldBlob}}, "truncated": false}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/trees/"+testTreeNew):
		entries := []any{map[string]any{"path": "fixture/a.go", "mode": "100644", "type": "blob", "sha": blobSHA(f.content)}}
		if f.extra {
			entries = append(entries, map[string]any{"path": "README.md", "mode": "100644", "type": "blob", "sha": testOldBlob})
		}
		return jsonResponse(200, map[string]any{"tree": entries, "truncated": false}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/blobs/"+testOldBlob):
		return jsonResponse(200, map[string]any{"content": base64.StdEncoding.EncodeToString([]byte("old")), "encoding": "base64", "size": 3}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/blobs"):
		var input struct{ Content string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		content, _ := base64.StdEncoding.DecodeString(input.Content)
		if !bytes.Equal(content, f.content) {
			return jsonResponse(422, map[string]any{}), nil
		}
		return jsonResponse(201, map[string]any{"sha": blobSHA(content)}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/trees"):
		return jsonResponse(201, map[string]any{"sha": testTreeNew}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/commits"):
		var input struct{ Message string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		f.commitMessage = input.Message
		return jsonResponse(201, map[string]any{"sha": testCommit}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/refs"):
		f.branch = true
		return jsonResponse(201, map[string]any{"ref": "refs/heads/sofa/test"}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls"):
		if !f.pr {
			return jsonResponse(200, []any{}), nil
		}
		return jsonResponse(200, []any{f.prObject()}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls"):
		f.pr = true
		return jsonResponse(201, f.prObject()), nil
	}
	return jsonResponse(404, map[string]any{"message": "unexpected endpoint"}), nil
}

func (f *publishFixture) prObject() any {
	return map[string]any{"number": 12, "html_url": "https://github.com/owner/fixture/pull/12", "state": "open", "draft": true, "head": map[string]any{"ref": "sofa/" + strings.Repeat("f", 24), "sha": testCommit, "repo": map[string]any{"full_name": testRepo}}}
}

func inputFixture(f *publishFixture) PublishInput {
	content := []byte("package fixture\n")
	f.content = content
	b := integrity.Bundle{Version: 1, Repository: testRepo, AttemptID: strings.Repeat("f", 64), Generation: 1, BaseSHA: testBase, Files: []integrity.File{{Path: "fixture/a.go", Operation: "update", Mode: integrity.RegularMode, BeforeSHA256: integrity.Hash([]byte("old")), Content: content}}}
	_ = integrity.Seal(&b)
	return PublishInput{Bundle: b, Expected: integrity.Expected{Repository: testRepo, AttemptID: b.AttemptID, Generation: 1, BaseSHA: testBase, CandidateDigest: b.CandidateDigest}, Policy: integrity.Policy{AllowedPaths: []string{"fixture/"}, MaxFiles: 2, MaxFileBytes: 2048, MaxTotalBytes: 4096}, Checks: []integrity.CheckEvidence{{Version: 1, Name: "go-test", CandidateDigest: b.CandidateDigest, Passed: true}}, RequiredChecks: []string{"go-test"}, BaseBranch: "main", Title: "Fixture", Body: "Validated fixture", Guard: func(context.Context) error { f.guards++; return nil }}
}

func TestPublishDraftIdempotenceAndRogueTree(t *testing.T) {
	f := new(publishFixture)
	in := inputFixture(f)
	c, err := New("fixture-token", roundTripFunc(f.trip))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := c.PublishDraft(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 12 || !f.branch || !f.pr || f.guards < 4 {
		t.Fatal("publication was incomplete or lacked guards")
	}
	mutations := f.mutations
	pr2, err := c.PublishDraft(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if pr2 != pr || f.mutations != mutations {
		t.Fatal("replay published duplicate objects")
	}
	f.extra = true
	if _, err := c.PublishDraft(context.Background(), in); err == nil {
		t.Fatal("accepted forged branch with extra file under matching commit marker")
	}
}

func TestPublishDraftBlocksMissingEvidence(t *testing.T) {
	f := new(publishFixture)
	in := inputFixture(f)
	in.Checks = nil
	c, _ := New("fixture-token", roundTripFunc(f.trip))
	if _, err := c.PublishDraft(context.Background(), in); err == nil {
		t.Fatal("missing check evidence allowed publication")
	}
	if f.mutations != 0 || f.guards != 0 {
		t.Fatal("publication began before validating evidence")
	}
}
