package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/state"
)

type memorySpecStore struct {
	state.MemoryStore
	specs map[string][]byte
}

func (s *memorySpecStore) SaveSpec(_ context.Context, issueID, digest string, contents []byte) error {
	if s.specs == nil {
		s.specs = map[string][]byte{}
	}
	key := issueID + "/" + digest
	if old, ok := s.specs[key]; ok && string(old) != string(contents) {
		return errors.New("immutable spec collision")
	}
	s.specs[key] = append([]byte(nil), contents...)
	return nil
}

type fakeCommentPublisher struct {
	comments []SpecComment
	posts    int
	failPost bool
}

func (p *fakeCommentPublisher) IssueComment(_ context.Context, _ string, _, id int64) (SpecComment, error) {
	for _, c := range p.comments {
		if c.ID == id {
			return c, nil
		}
	}
	return SpecComment{}, errors.New("comment missing")
}

func (p *fakeCommentPublisher) IssueComments(context.Context, string, int64) ([]SpecComment, error) {
	return append([]SpecComment(nil), p.comments...), nil
}

func (p *fakeCommentPublisher) CreateIssueComment(_ context.Context, _ string, _ int64, body string) (SpecComment, error) {
	p.posts++
	c := SpecComment{
		ID:        int64(100 + p.posts),
		AuthorID:  "BOT_node",
		Body:      body,
		CreatedAt: time.Date(2026, 9, 29, 10, 1, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 29, 10, 1, 0, 0, time.UTC),
	}
	p.comments = append(p.comments, c)
	if p.failPost {
		return SpecComment{}, errors.New("ambiguous transport failure")
	}
	return c, nil
}

func discoveryPublishFixture(t *testing.T) (state.Engine, *memorySpecStore, admission.Snapshot, state.DiscoveryFence, PublishInput) {
	t.Helper()
	ctx := context.Background()
	s := fixtureSnapshot()
	s.CurrentStatus = "Discovery"
	s.StatusOptionID = "discovery-option"
	p := fixturePolicy()
	grant, _, err := AuthorizeDiscovery(p, s)
	if err != nil {
		t.Fatal(err)
	}
	store := &memorySpecStore{}
	engine := state.Engine{
		Store: store,
		Now:   func() time.Time { return time.Date(2026, 9, 29, 10, 2, 0, 0, time.UTC) },
	}
	if _, _, err := engine.AdmitDiscovery(ctx, grant, 2, 1); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, s.IssueID, state.Owner{
		RunID:      "100",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := fixtureSpec().Render()
	in := PublishInput{
		Policy:           p,
		IssueID:          s.IssueID,
		Fence:            fence,
		DraftBody:        body,
		ExpectedAuthorID: "BOT_node",
		Guard:            func(context.Context) (admission.Snapshot, error) { return s, nil },
	}
	return engine, store, s, fence, in
}

func TestCommentTransportAmbiguityRecoversWithoutSecondPost(t *testing.T) {
	ctx := context.Background()
	engine, store, s, _, in := discoveryPublishFixture(t)
	publisher := &fakeCommentPublisher{
		failPost: true,
	}
	if _, err := PublishSpecification(ctx, engine, store, publisher, in); err == nil {
		t.Fatal("ambiguous POST unexpectedly reported success")
	}
	if publisher.posts != 1 {
		t.Fatalf("expected one POST, got %d", publisher.posts)
	}
	proof := state.RunProof{
		Owner:      in.Fence.Owner,
		Status:     "completed",
		Conclusion: "failure",
		ObservedAt: time.Date(2026, 9, 29, 10, 2, 0, 0, time.UTC),
	}
	if err := engine.RecoverDiscovery(ctx, s.IssueID, proof); err != nil {
		t.Fatal(err)
	}
	newFence, err := engine.ClaimDiscovery(ctx, s.IssueID, state.Owner{
		RunID:      "101",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Fence = newFence
	publisher.failPost = false
	comment, err := PublishSpecification(ctx, engine, store, publisher, in)
	if err != nil || comment.ID != 101 || publisher.posts != 1 {
		t.Fatalf("recovery duplicated or lost comment: %+v, posts=%d, err=%v", comment, publisher.posts, err)
	}
	ledger, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if task := ledger.State.Discoveries[s.IssueID]; task.Phase != state.DiscoveryReview || task.ModelCalls != 1 || task.SpecDigest == "" || task.Publication != nil {
		t.Fatalf("recovery changed budget or approval state: %+v", task)
	}
}

func TestAmbiguousPostWithoutMatchingBotCommentBlocksRetry(t *testing.T) {
	ctx := context.Background()
	engine, store, s, _, in := discoveryPublishFixture(t)
	publisher := &fakeCommentPublisher{
		failPost: true,
	}
	_, _ = PublishSpecification(ctx, engine, store, publisher, in)
	publisher.comments = nil // Ambiguous outcome: never issue a second POST.
	proof := state.RunProof{
		Owner:      in.Fence.Owner,
		Status:     "completed",
		Conclusion: "failure",
		ObservedAt: time.Date(2026, 9, 29, 10, 2, 0, 0, time.UTC),
	}
	if err := engine.RecoverDiscovery(ctx, s.IssueID, proof); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, s.IssueID, state.Owner{
		RunID:      "101",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	in.Fence = fence
	if _, err := PublishSpecification(ctx, engine, store, publisher, in); err == nil || !strings.Contains(err.Error(), "outcome uncertain") {
		t.Fatalf("ambiguous POST not blocked: %v", err)
	}
	if publisher.posts != 1 {
		t.Fatalf("retry posted again: %d", publisher.posts)
	}
}

func TestObservedSpecReviewAndBacklogPersistExactSnapshot(t *testing.T) {
	ctx := context.Background()
	engine, store, source, _, in := discoveryPublishFixture(t)
	publisher := &fakeCommentPublisher{}
	comment, err := PublishSpecification(ctx, engine, store, publisher, in)
	if err != nil {
		t.Fatal(err)
	}
	source.CurrentStatus = "Spec Review"
	source.StatusOptionID = "review-option"
	source.StatusUpdatedAt = time.Date(2026, 9, 29, 10, 3, 0, 0, time.UTC)
	record, changed, err := ObserveSpecReview(ctx, publisher, store, fixturePolicy(), source)
	if err != nil || !changed || record.CommentID != comment.ID || record.ApprovedDigest != "" {
		t.Fatalf("Spec Review observation: %+v, changed=%v, err=%v", record, changed, err)
	}
	if len(store.specs[source.IssueID+"/"+record.SpecDigest]) == 0 {
		t.Fatal("approved canonical snapshot was not persisted")
	}
	if _, changed, err := ObserveSpecReview(ctx, publisher, store, fixturePolicy(), source); err != nil || changed {
		t.Fatalf("duplicate Spec Review changed ledger: %v, %v", changed, err)
	}
	source.CurrentStatus = "Backlog"
	source.StatusOptionID = "backlog-option"
	source.StatusUpdatedAt = source.StatusUpdatedAt.Add(time.Minute)
	record, changed, err = ObserveBacklog(ctx, publisher, store, fixturePolicy(), source)
	if err != nil || !changed || record.ApprovedDigest != record.SpecDigest {
		t.Fatalf("Backlog approval: %+v, changed=%v, err=%v", record, changed, err)
	}
	source.CurrentStatus = "Ready"
	source.StatusOptionID = "ready-option"
	source.StatusUpdatedAt = source.StatusUpdatedAt.Add(time.Minute)
	approved, err := ApprovedSnapshot(ctx, publisher, store, fixturePolicy(), source)
	if err != nil || approved.Body != comment.Body {
		t.Fatalf("approved comment was not substituted: %+v, %v", approved, err)
	}
	publisher.comments[0].UpdatedAt = publisher.comments[0].UpdatedAt.Add(time.Second)
	if _, err := VerifyApprovedRevision(ctx, publisher, store, fixturePolicy(), source); !errors.Is(err, ErrRevision) {
		t.Fatalf("edited comment retained approval: %v", err)
	}
}

func TestSensitiveSpecificationCannotEnterStateOrIssue(t *testing.T) {
	ctx := context.Background()
	engine, store, _, _, in := discoveryPublishFixture(t)
	publisher := &fakeCommentPublisher{}
	in.ForbiddenValues = [][]byte{[]byte("Hello, friend")}
	if _, err := PublishSpecification(ctx, engine, store, publisher, in); err == nil {
		t.Fatal("sensitive specification accepted")
	}
	if len(store.specs) != 0 || publisher.posts != 0 {
		t.Fatalf("sensitive text persisted or published: snapshots=%d posts=%d", len(store.specs), publisher.posts)
	}
}
