package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

type CommentPublisher interface {
	CommentReader
	IssueComments(context.Context, string, int64) ([]SpecComment, error)
	CreateIssueComment(context.Context, string, int64, string) (SpecComment, error)
}

// PublishInput is handled by a trusted controller job. Its Guard fetches the
// current issue/Project after the untrusted worker produced DraftBody.
type PublishInput struct {
	Policy           Policy
	IssueID          string
	Fence            state.DiscoveryFence
	DraftBody        string
	ExpectedAuthorID string
	ForbiddenValues  [][]byte
	Guard            func(context.Context) (admission.Snapshot, error)
}

// publicationKey derives a retry locator from the task's source, Project status
// time, and exact draft body; worker ownership changes do not change the key.
func publicationKey(task state.DiscoveryTask, body string) string {
	payload, _ := json.Marshal(struct {
		Repository, IssueID, SourceDigest, Body string
		StatusUpdatedAt                         string
	}{
		Repository:      task.Repository,
		IssueID:         task.IssueID,
		SourceDigest:    task.SourceDigest,
		Body:            body,
		StatusUpdatedAt: task.StatusUpdatedAt.UTC().Format(time.RFC3339Nano),
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// publicationSource checks that Discovery authority and the source digest still
// match the task, treating invalid source content as a mismatch.
func publicationSource(p Policy, s admission.Snapshot, task state.DiscoveryTask) bool {
	if !p.trusted(s, p.DiscoveryStatus) || task.IssueID != s.IssueID || task.Issue != int64(s.Number) || task.ProjectItemID != s.ProjectItemID || task.ProjectID != s.ProjectID || task.StatusOptionID != s.StatusOptionID || !task.StatusUpdatedAt.Equal(s.StatusUpdatedAt) || !strings.EqualFold(task.Repository, p.Repository) {
		return false
	}
	digest, err := sourceDigest(s)
	return err == nil && digest == task.SourceDigest
}

// matchingPublishedComment finds an exact body under the expected key and author.
// No match returns false without error; invalid, edited, or duplicate matching
// comments return an error so callers cannot safely retry the POST.
func matchingPublishedComment(comments []SpecComment, body, key, author string) (SpecComment, bool, error) {
	var match SpecComment
	found := false
	for _, comment := range comments {
		if PublicationKey(comment.Body) != key || comment.AuthorID != author {
			continue
		}
		if comment.Body != body || !comment.valid() {
			return SpecComment{}, false, errors.New("discovery bot comment with this key changed")
		}
		if found {
			return SpecComment{}, false, errors.New("multiple Discovery bot comments share one publication key")
		}
		match = comment
		found = true
	}
	return match, found, nil
}

// MatchPublicationIntent checks an uncertain POST using only its durable
// intent. The publication key hashes the original candidate bytes, so removing
// the inserted key line and recomputing it proves the exact body without
// downloading an artifact or treating the comment's key as authority. The
// digest additionally binds the current issue title and published spec.
func MatchPublicationIntent(comments []SpecComment, task state.DiscoveryTask, title, author string) (SpecComment, bool, error) {
	if task.Publication == nil || !task.Publication.PostAttempted || !keyPattern.MatchString(task.Publication.Key) || !keyPattern.MatchString(task.Publication.Digest) || author == "" {
		return SpecComment{}, false, ErrAuthority
	}
	key := task.Publication.Key
	keyedMarker := marker + "\n" + publicationKeyPrefix + key + " -->"
	var match SpecComment
	found := false
	for _, comment := range comments {
		if PublicationKey(comment.Body) != key || comment.AuthorID != author {
			continue
		}
		if !comment.valid() || !strings.HasPrefix(comment.Body, keyedMarker) {
			return SpecComment{}, false, errors.New("discovery bot comment with this key changed")
		}
		candidate := strings.Replace(comment.Body, keyedMarker, marker, 1)
		body, err := WithPublicationKey(candidate, key)
		if err != nil || body != comment.Body || publicationKey(task, candidate) != key {
			return SpecComment{}, false, errors.New("discovery bot comment with this key changed")
		}
		_, digest, err := admission.CanonicalSpec(title, comment.Body)
		if err != nil || digest != task.Publication.Digest {
			return SpecComment{}, false, errors.New("discovery bot comment digest changed")
		}
		if found {
			return SpecComment{}, false, errors.New("multiple Discovery bot comments share one publication key")
		}
		match = comment
		found = true
	}
	return match, found, nil
}

// PublishSpecification persists the spec snapshot and comment intent before
// posting. A transport-ambiguous POST cannot be repeated: recovery either
// finds the exact bot comment or blocks for investigation.
func PublishSpecification(ctx context.Context, engine state.Engine, store SpecStore, publisher CommentPublisher, in PublishInput) (SpecComment, error) {
	if store == nil || publisher == nil || in.Guard == nil || in.ExpectedAuthorID == "" || in.IssueID == "" || in.Fence.IssueID != in.IssueID || !in.Policy.valid() {
		return SpecComment{}, ErrAuthority
	}
	if _, err := Parse(in.DraftBody); err != nil {
		return SpecComment{}, err
	}
	if err := engine.AssertDiscoveryOwner(ctx, in.Fence); err != nil {
		return SpecComment{}, err
	}
	ledger, err := store.Load(ctx)
	if err != nil {
		return SpecComment{}, err
	}
	task, ok := ledger.State.Discoveries[in.IssueID]
	if !ok || task.Phase != state.DiscoveryRunning {
		return SpecComment{}, ErrAuthority
	}
	source, err := in.Guard(ctx)
	if err != nil {
		return SpecComment{}, err
	}
	if !publicationSource(in.Policy, source, task) {
		return SpecComment{}, ErrRevision
	}
	key := publicationKey(task, in.DraftBody)
	body, err := WithPublicationKey(in.DraftBody, key)
	if err != nil {
		return SpecComment{}, err
	}
	canonical, digest, err := admission.CanonicalSpec(source.Title, body)
	if err != nil {
		return SpecComment{}, err
	}
	if err := integrity.ScanSecrets([]byte(body), in.ForbiddenValues); err != nil {
		return SpecComment{}, errors.New("discovery specification contains sensitive material")
	}
	if err := store.SaveSpec(ctx, task.IssueID, digest, canonical); err != nil {
		return SpecComment{}, err
	}
	if err := engine.PrepareDiscoveryPublication(ctx, in.Fence, digest, key); err != nil {
		return SpecComment{}, err
	}
	comments, err := publisher.IssueComments(ctx, in.Policy.Repository, int64(source.Number))
	if err != nil {
		return SpecComment{}, err
	}
	comment, found, err := matchingPublishedComment(comments, body, key, in.ExpectedAuthorID)
	if err != nil {
		return SpecComment{}, err
	}
	if !found {
		latest, err := store.Load(ctx)
		if err != nil {
			return SpecComment{}, err
		}
		current := latest.State.Discoveries[in.IssueID]
		if current.Publication == nil || current.Publication.Digest != digest || current.Publication.Key != key || current.Publication.PostAttempted {
			return SpecComment{}, errors.New("discovery comment outcome uncertain; inspect exact publication key")
		}
		if err := engine.AssertDiscoveryOwner(ctx, in.Fence); err != nil {
			return SpecComment{}, err
		}
		source, err = in.Guard(ctx)
		if err != nil || !publicationSource(in.Policy, source, task) {
			return SpecComment{}, ErrRevision
		}
		first, err := engine.MarkDiscoveryPostAttempt(ctx, in.Fence, digest, key)
		if err != nil {
			return SpecComment{}, err
		}
		if !first {
			return SpecComment{}, errors.New("discovery comment POST already attempted")
		}
		comment, err = publisher.CreateIssueComment(ctx, in.Policy.Repository, int64(source.Number), body)
		if err != nil {
			return SpecComment{}, err
		}
	}
	if comment.AuthorID != in.ExpectedAuthorID || comment.Body != body || !comment.valid() {
		return SpecComment{}, errors.New("published Discovery comment identity differs")
	}
	if err := engine.AssertDiscoveryOwner(ctx, in.Fence); err != nil {
		return SpecComment{}, err
	}
	if err := engine.CompleteDiscovery(ctx, in.Fence, digest, comment.ID, comment.AuthorID, comment.CreatedAt, comment.UpdatedAt); err != nil {
		return SpecComment{}, err
	}
	return comment, nil
}
