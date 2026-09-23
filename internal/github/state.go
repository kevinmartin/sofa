package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/state"
)

// StateStore implements the state.Store interface through the calling
// repository's Git data API. Each non-forced reference update is a CAS against
// the observed commit parent. GitHub rejects competing sibling commits.
type StateStore struct {
	Client     *Client
	Repository string
}

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var errStateFileMissing = errors.New("state file is missing")

func (s StateStore) prefix() string { return "/repos/" + s.Repository }

func (s StateStore) valid() error {
	if s.Client == nil || !repositoryPattern.MatchString(s.Repository) {
		return errors.New("invalid repository state store")
	}
	return nil
}

func (s StateStore) Load(ctx context.Context) (state.Snapshot, error) {
	if err := s.valid(); err != nil {
		return state.Snapshot{}, err
	}
	ref, err := s.Client.ref(ctx, s.Repository, "sofa-state")
	if isNotFound(err) {
		return state.Snapshot{State: state.Empty()}, nil
	}
	if err != nil {
		return state.Snapshot{}, err
	}
	commit, err := s.Client.commit(ctx, s.Repository, ref)
	if err != nil {
		return state.Snapshot{}, err
	}
	content, err := s.blobFromTree(ctx, commit.Tree.SHA, "ledger.json", 16<<20)
	if err != nil {
		return state.Snapshot{}, err
	}
	ledger, err := state.Decode(content)
	if err != nil {
		return state.Snapshot{}, err
	}
	return state.Snapshot{Revision: ref, State: ledger}, nil
}

func (s StateStore) blobFromTree(ctx context.Context, treeSHA, path string, maxSize int) ([]byte, error) {
	entries, err := s.Client.tree(ctx, s.Repository, treeSHA)
	if err != nil {
		return nil, err
	}
	var blobSHA string
	for _, entry := range entries {
		if entry.Path == path {
			if entry.Type != "blob" || entry.Mode != "100644" {
				return nil, errors.New("state file has unsafe mode")
			}
			blobSHA = entry.SHA
			break
		}
	}
	if blobSHA == "" {
		return nil, errStateFileMissing
	}
	var blob struct {
		Content  string
		Encoding string
		Size     int
	}
	if err := s.Client.Request(ctx, http.MethodGet, s.prefix()+"/git/blobs/"+blobSHA, nil, &blob); err != nil {
		return nil, err
	}
	if blob.Encoding != "base64" || blob.Size < 0 || blob.Size > maxSize {
		return nil, errors.New("state blob exceeds limit")
	}
	content, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(blob.Content, "\n", ""))
	if err != nil || len(content) != blob.Size {
		return nil, errors.New("state blob content invalid")
	}
	return content, nil
}

func (s StateStore) CompareAndSwap(ctx context.Context, expected string, ledger state.State) error {
	if err := s.valid(); err != nil {
		return err
	}
	content, err := state.Encode(ledger)
	if err != nil {
		return err
	}
	current, err := s.Load(ctx)
	if err != nil {
		return err
	}
	if current.Revision != expected {
		return state.ErrConflict
	}
	return s.write(ctx, expected, map[string][]byte{"ledger.json": content}, "sofa ledger v1")
}

// SaveSpec writes an immutable canonical specification outside the compact
// ledger, retaining prior snapshots and retrying only a provable CAS conflict.
func (s StateStore) SaveSpec(ctx context.Context, issueID, digest string, canonical []byte) error {
	if err := s.valid(); err != nil {
		return err
	}
	path, err := state.SpecPath(issueID, digest)
	if err != nil || len(canonical) == 0 || len(canonical) > 64<<10 {
		return errors.New("invalid specification identity or size")
	}
	h := sha256.Sum256(canonical)
	if hex.EncodeToString(h[:]) != digest {
		return errors.New("specification digest mismatch")
	}
	for i := 0; i < 12; i++ {
		current, err := s.Load(ctx)
		if err != nil {
			return err
		}
		if current.Revision != "" {
			commit, err := s.Client.commit(ctx, s.Repository, current.Revision)
			if err != nil {
				return err
			}
			prior, err := s.blobFromTree(ctx, commit.Tree.SHA, path, 64<<10)
			if err == nil {
				if string(prior) == string(canonical) {
					return nil
				}
				return errors.New("immutable specification collision")
			}
			if !errors.Is(err, errStateFileMissing) {
				return err
			}
		}
		updates := map[string][]byte{path: canonical}
		if current.Revision == "" {
			ledger, encodeErr := state.Encode(state.Empty())
			if encodeErr != nil {
				return encodeErr
			}
			updates["ledger.json"] = ledger
		}
		err = s.write(ctx, current.Revision, updates, "sofa specification v1")
		if errors.Is(err, state.ErrConflict) {
			continue
		}
		return err
	}
	return state.ErrConflict
}

func (s StateStore) write(ctx context.Context, expected string, updates map[string][]byte, message string) error {
	var baseTree string
	if expected != "" {
		commit, err := s.Client.commit(ctx, s.Repository, expected)
		if err != nil {
			return err
		}
		baseTree = commit.Tree.SHA
	}
	entries := make([]map[string]any, 0, len(updates))
	for path, contents := range updates {
		var blob struct{ SHA string }
		if err := s.Client.Request(ctx, http.MethodPost, s.prefix()+"/git/blobs", map[string]any{"content": base64.StdEncoding.EncodeToString(contents), "encoding": "base64"}, &blob); err != nil {
			return err
		}
		if blob.SHA == "" {
			return errors.New("state blob creation lacked SHA")
		}
		entries = append(entries, map[string]any{"path": path, "mode": "100644", "type": "blob", "sha": blob.SHA})
	}
	input := map[string]any{"tree": entries}
	if baseTree != "" {
		input["base_tree"] = baseTree
	}
	var tree struct{ SHA string }
	if err := s.Client.Request(ctx, http.MethodPost, s.prefix()+"/git/trees", input, &tree); err != nil {
		return err
	}
	if tree.SHA == "" {
		return errors.New("state tree creation lacked SHA")
	}
	parents := []string{}
	if expected != "" {
		parents = append(parents, expected)
	}
	var commit struct{ SHA string }
	if err := s.Client.Request(ctx, http.MethodPost, s.prefix()+"/git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": parents}, &commit); err != nil {
		return err
	}
	if commit.SHA == "" {
		return errors.New("state commit creation lacked SHA")
	}
	var result any
	if expected == "" {
		if err := s.Client.Request(ctx, http.MethodPost, s.prefix()+"/git/refs", map[string]any{"ref": "refs/heads/sofa-state", "sha": commit.SHA}, &result); err != nil {
			return s.mapConflict(ctx, expected, err)
		}
	} else {
		if err := s.Client.Request(ctx, http.MethodPatch, s.prefix()+"/git/refs/heads/sofa-state", map[string]any{"sha": commit.SHA, "force": false}, &result); err != nil {
			return s.mapConflict(ctx, expected, err)
		}
	}
	return nil
}

func (s StateStore) mapConflict(ctx context.Context, expected string, original error) error {
	var api *APIError
	if errors.As(original, &api) && (api.Status == 409 || api.Status == 422) {
		return state.ErrConflict
	}
	current, err := s.Load(ctx)
	if err == nil && current.Revision != expected {
		return state.ErrConflict
	}
	return original
}

// RunProof checks a specific Actions attempt, not a stale heartbeat or elapsed
// timeout. A missing/failed API response never authorizes a replacement owner.
func (c *Client) RunProof(ctx context.Context, repo string, owner state.Owner) (state.RunProof, error) {
	if !repositoryPattern.MatchString(repo) || owner.RunAttempt < 1 {
		return state.RunProof{}, errors.New("invalid run identity")
	}
	id, err := strconv.ParseInt(owner.RunID, 10, 64)
	if err != nil || id <= 0 {
		return state.RunProof{}, errors.New("invalid run ID")
	}
	var run struct {
		ID         int64  `json:"id"`
		RunAttempt int    `json:"run_attempt"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d", repo, id, owner.RunAttempt)
	if err := c.Request(ctx, http.MethodGet, path, nil, &run); err != nil {
		return state.RunProof{}, err
	}
	if run.ID != id || run.RunAttempt != owner.RunAttempt {
		return state.RunProof{}, errors.New("run attempt identity mismatch")
	}
	return state.RunProof{Owner: owner, Status: run.Status, Conclusion: run.Conclusion, ObservedAt: time.Now().UTC()}, nil
}
