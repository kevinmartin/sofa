package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/state"
)

func TestRetainedCandidateAvailabilityFailsClosed(t *testing.T) {
	checkpoint := state.Checkpoint{Version: state.Version, Phase: state.Validating, ArtifactID: "sofa-verified-candidate-42-2", Producer: state.Owner{RunID: "42", RunAttempt: 2}}
	artifact := map[string]any{"id": 17, "name": checkpoint.ArtifactID, "expired": false, "workflow_run": map[string]any{"id": 42}}
	cases := []struct {
		name      string
		code      int
		body      any
		want      bool
		wantError bool
	}{
		{"present", 200, map[string]any{"total_count": 1, "artifacts": []any{artifact}}, true, false},
		{"missing", 200, map[string]any{"total_count": 0, "artifacts": []any{}}, false, false},
		{"expired", 200, map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 17, "name": checkpoint.ArtifactID, "expired": true, "workflow_run": map[string]any{"id": 42}}}}, false, false},
		{"ambiguous", 200, map[string]any{"total_count": 2, "artifacts": []any{artifact, artifact}}, false, true},
		{"wrong-run", 200, map[string]any{"total_count": 1, "artifacts": []any{map[string]any{"id": 17, "name": checkpoint.ArtifactID, "expired": false, "workflow_run": map[string]any{"id": 43}}}}, false, true},
		{"forbidden", 403, map[string]any{}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/fixture/actions/runs/42/artifacts" || r.URL.Query().Get("name") != checkpoint.ArtifactID || r.URL.Query().Get("per_page") != "2" {
					t.Errorf("unexpected artifact request %s", r.URL.Redacted())
				}
				return jsonResponse(tc.code, tc.body), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			available, err := client.RetainedCandidateAvailable(context.Background(), "owner/fixture", checkpoint)
			if available != tc.want || (err != nil) != tc.wantError {
				t.Fatalf("available=%v err=%v", available, err)
			}
		})
	}
	checkpoint.ArtifactID = "wrong"
	client, err := New("fixture-token", roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid identity made an API call")
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RetainedCandidateAvailable(context.Background(), "owner/fixture", checkpoint); err == nil {
		t.Fatal("invalid artifact identity accepted")
	}
}

// This fake models GitHub's non-forced ref update: two commits built on the
// same observed parent cannot both advance a branch. It also preserves a base
// tree when the ledger alone is rewritten.
type stateGitFixture struct {
	mu      sync.Mutex
	ref     string
	serial  int
	blobs   map[string][]byte
	trees   map[string]map[string]string
	commits map[string]struct{ tree, parent string }
}

func newStateGitFixture() *stateGitFixture {
	return &stateGitFixture{blobs: map[string][]byte{}, trees: map[string]map[string]string{}, commits: map[string]struct{ tree, parent string }{}}
}

func (f *stateGitFixture) next() string {
	f.serial++
	h := sha256.Sum256([]byte{byte(f.serial), byte(f.serial >> 8)})
	return hex.EncodeToString(h[:20])
}

func (f *stateGitFixture) trip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/repos/owner/fixture")
	switch {
	case r.Method == http.MethodGet && p == "/git/ref/heads/sofa-state":
		if f.ref == "" {
			return jsonResponse(404, map[string]any{}), nil
		}
		return jsonResponse(200, map[string]any{"object": map[string]any{"sha": f.ref}}), nil
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/commits/"):
		sha := strings.TrimPrefix(p, "/git/commits/")
		commit, ok := f.commits[sha]
		if !ok {
			return jsonResponse(404, map[string]any{}), nil
		}
		return jsonResponse(200, map[string]any{"sha": sha, "tree": map[string]any{"sha": commit.tree}}), nil
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/trees/"):
		sha := strings.TrimPrefix(p, "/git/trees/")
		files, ok := f.trees[sha]
		if !ok {
			return jsonResponse(404, map[string]any{}), nil
		}
		entries := make([]map[string]any, 0, len(files))
		for path, blob := range files {
			entries = append(entries, map[string]any{"path": path, "type": "blob", "mode": "100644", "sha": blob})
		}
		return jsonResponse(200, map[string]any{"tree": entries, "truncated": false}), nil
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/blobs/"):
		content, ok := f.blobs[strings.TrimPrefix(p, "/git/blobs/")]
		if !ok {
			return jsonResponse(404, map[string]any{}), nil
		}
		return jsonResponse(200, map[string]any{"content": base64.StdEncoding.EncodeToString(content), "encoding": "base64", "size": len(content)}), nil
	case r.Method == http.MethodPost && p == "/git/blobs":
		var input struct{ Content, Encoding string }
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Encoding != "base64" {
			return jsonResponse(422, map[string]any{}), nil
		}
		content, err := base64.StdEncoding.DecodeString(input.Content)
		if err != nil {
			return jsonResponse(422, map[string]any{}), nil
		}
		sha := f.next()
		f.blobs[sha] = content
		return jsonResponse(201, map[string]any{"sha": sha}), nil
	case r.Method == http.MethodPost && p == "/git/trees":
		var input struct {
			BaseTree string `json:"base_tree"`
			Tree     []struct{ Path, SHA string }
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			return jsonResponse(422, map[string]any{}), nil
		}
		files := map[string]string{}
		if input.BaseTree != "" {
			for path, sha := range f.trees[input.BaseTree] {
				files[path] = sha
			}
		}
		for _, entry := range input.Tree {
			files[entry.Path] = entry.SHA
		}
		sha := f.next()
		f.trees[sha] = files
		return jsonResponse(201, map[string]any{"sha": sha}), nil
	case r.Method == http.MethodPost && p == "/git/commits":
		var input struct {
			Tree    string
			Parents []string
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Parents) > 1 {
			return jsonResponse(422, map[string]any{}), nil
		}
		parent := ""
		if len(input.Parents) == 1 {
			parent = input.Parents[0]
		}
		sha := f.next()
		f.commits[sha] = struct{ tree, parent string }{input.Tree, parent}
		return jsonResponse(201, map[string]any{"sha": sha}), nil
	case r.Method == http.MethodPost && p == "/git/refs":
		if f.ref != "" {
			return jsonResponse(422, map[string]any{}), nil
		}
		var input struct{ SHA string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		f.ref = input.SHA
		return jsonResponse(201, map[string]any{}), nil
	case r.Method == http.MethodPatch && p == "/git/refs/heads/sofa-state":
		var input struct {
			SHA   string
			Force bool
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.Force || f.commits[input.SHA].parent != f.ref {
			return jsonResponse(422, map[string]any{}), nil
		}
		f.ref = input.SHA
		return jsonResponse(200, map[string]any{}), nil
	}
	return jsonResponse(404, map[string]any{}), nil
}

func TestStateStoreRetainsSpecAndRejectsStaleRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newStateGitFixture()
	client, err := New("fixture-token", roundTripFunc(fixture.trip))
	if err != nil {
		t.Fatal(err)
	}
	store := StateStore{Client: client, Repository: "owner/fixture"}
	snap, err := store.Load(ctx)
	if err != nil || snap.Revision != "" || len(snap.State.Attempts) != 0 {
		t.Fatalf("empty state: %#v %v", snap, err)
	}
	spec := []byte(`{"title":"fixture","body":"work"}`)
	digest := sha256.Sum256(spec)
	digestText := hex.EncodeToString(digest[:])
	if err := store.SaveSpec(ctx, "issue-1", digestText, spec); err != nil {
		t.Fatal(err)
	}
	snap, err = store.Load(ctx)
	if err != nil || snap.Revision == "" {
		t.Fatalf("spec initialization: %#v %v", snap, err)
	}
	prior := snap.Revision
	if err := store.CompareAndSwap(ctx, prior, snap.State); err != nil {
		t.Fatal(err)
	}
	if err := store.CompareAndSwap(ctx, prior, snap.State); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	snap, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := client.commit(ctx, store.Repository, snap.Revision)
	if err != nil {
		t.Fatal(err)
	}
	path, err := state.SpecPath("issue-1", digestText)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := store.blobFromTree(ctx, commit.Tree.SHA, path, 64<<10)
	if err != nil || !bytes.Equal(recovered, spec) {
		t.Fatalf("specification lost after ledger update: %v", err)
	}
	if err := store.SaveSpec(ctx, "issue-1", digestText, spec); err != nil {
		t.Fatalf("identical immutable replay failed: %v", err)
	}
}

func TestStateStoreMapsAmbiguousRefUpdateByWrittenRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newStateGitFixture()
	client, err := New("fixture-token", roundTripFunc(fixture.trip))
	if err != nil {
		t.Fatal(err)
	}
	store := StateStore{Client: client, Repository: "owner/fixture"}
	spec := []byte(`{"title":"fixture","body":"work"}`)
	h := sha256.Sum256(spec)
	if err := store.SaveSpec(ctx, "issue-1", hex.EncodeToString(h[:]), spec); err != nil {
		t.Fatal(err)
	}
	first, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lostResponse := errors.New("lost ref-update response")
	if err := store.mapConflict(ctx, "", first.Revision, lostResponse); err != nil {
		t.Fatalf("applied update was misreported as a conflict: %v", err)
	}
	invalid := &APIError{Status: 422}
	if err := store.mapConflict(ctx, first.Revision, strings.Repeat("a", 40), invalid); err != invalid {
		t.Fatalf("unchanged ref hid validation error: %v", err)
	}
	if err := store.CompareAndSwap(ctx, first.Revision, first.State); err != nil {
		t.Fatal(err)
	}
	if err := store.mapConflict(ctx, first.Revision, strings.Repeat("a", 40), invalid); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("concurrent ref change was not a conflict: %v", err)
	}
}

func TestLostRefResponseDoesNotChargeBudgetTwice(t *testing.T) {
	ctx := context.Background()
	fixture := newStateGitFixture()
	loseResponse := false
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		response, err := fixture.trip(r)
		if err != nil || !loseResponse || r.Method != http.MethodPatch || !strings.HasSuffix(r.URL.Path, "/git/refs/heads/sofa-state") {
			return response, err
		}
		loseResponse = false
		_ = response.Body.Close()
		return nil, errors.New("lost response after ref advanced")
	}))
	if err != nil {
		t.Fatal(err)
	}
	store := StateStore{Client: client, Repository: "owner/fixture"}
	engine := state.Engine{Store: store}
	admission := state.Admission{Repository: "owner/fixture", Issue: 1, SpecDigest: strings.Repeat("a", 64), ConfigDigest: strings.Repeat("b", 64), BaseSHA: strings.Repeat("c", 40), ProjectID: "P_1", ProjectItemID: "I_1", StatusOptionID: "ready", StatusUpdatedAt: time.Now().UTC()}
	attempt, _, err := engine.Admit(ctx, admission, state.Limits{ModelCalls: 2, InfrastructureRetries: 1, RuntimeSeconds: 100})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := engine.Claim(ctx, attempt.ID, state.Owner{RunID: "42", RunAttempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	loseResponse = true
	if err := engine.Charge(ctx, fence, state.Counters{ModelCalls: 1}); err != nil {
		t.Fatalf("applied charge returned an error: %v", err)
	}
	snapshot, err := store.Load(ctx)
	if err != nil || snapshot.State.Attempts[attempt.ID].Counts.ModelCalls != 1 {
		t.Fatalf("lost response double-charged budget: %+v, %v", snapshot.State.Attempts[attempt.ID].Counts, err)
	}
}
