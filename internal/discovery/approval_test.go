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

func fixtureSpec() Specification {
	return Specification{
		Version:        SpecificationVersion,
		Problem:        "A user cannot greet a blank name.",
		Evidence:       "The current CLI returns a blank greeting; reproduce with an empty argument.",
		Goals:          "Return a friendly default greeting.",
		NonGoals:       "No changes to localization.",
		Constraints:    "Keep the public CLI flag and output contract.",
		Dependencies:   "None.",
		Acceptance:     "Given a blank name, output Hello, friend. Given Ada, output Hello, Ada.",
		Validation:     "Run the CLI fixture and go test ./... against the candidate.",
		Risks:          "No known migration. Verify callers do not rely on blank output.",
		Questions:      "None currently.",
		DeliverySlices: "One behavior change with a test.",
	}
}

func fixturePolicy() Policy {
	return Policy{
		Repository:       "kevinmartin/sofa-disposable",
		RepositoryID:     "R_repo",
		ProjectID:        "P_private",
		DiscoveryStatus:  "Discovery",
		SpecReviewStatus: "Spec Review",
		BacklogStatus:    "Backlog",
		ReadyStatus:      "Ready",
	}
}

func fixtureSnapshot() admission.Snapshot {
	return admission.Snapshot{
		Repository:        "kevinmartin/sofa-disposable",
		RepositoryID:      "R_repo",
		IssueID:           "I_1",
		Number:            12,
		Title:             "Friendly blank greeting",
		Body:              "Please investigate the blank greeting.",
		Open:              true,
		ProjectID:         "P_private",
		ProjectPrivate:    true,
		ProjectItemID:     "PVTI_1",
		CurrentStatus:     "Spec Review",
		StatusOptionID:    "review-option",
		StatusUpdatedAt:   time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
		IssueLastEditedAt: time.Date(2026, 9, 29, 9, 58, 0, 0, time.UTC),
		Complete:          true,
	}
}

func fixtureComment(t *testing.T) SpecComment {
	t.Helper()
	body, err := fixtureSpec().Render()
	if err != nil {
		t.Fatal(err)
	}
	return SpecComment{
		ID:        42,
		AuthorID:  "BOT_node",
		Body:      body,
		CreatedAt: time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 29, 9, 59, 0, 0, time.UTC),
	}
}

func fixtureTask(t *testing.T, source admission.Snapshot, comment SpecComment) state.DiscoveryTask {
	t.Helper()
	_, sourceDigest, err := admission.CanonicalSpec(source.Title, source.Body)
	if err != nil {
		t.Fatal(err)
	}
	_, specDigest, err := admission.CanonicalSpec(source.Title, comment.Body)
	if err != nil {
		t.Fatal(err)
	}
	return state.DiscoveryTask{
		Repository:       source.Repository,
		IssueID:          source.IssueID,
		Issue:            int64(source.Number),
		ProjectID:        source.ProjectID,
		ProjectItemID:    source.ProjectItemID,
		SourceDigest:     sourceDigest,
		SpecDigest:       specDigest,
		Phase:            state.DiscoveryReview,
		CommentID:        comment.ID,
		CommentAuthorID:  comment.AuthorID,
		CommentCreatedAt: comment.CreatedAt,
		CommentUpdatedAt: comment.UpdatedAt,
	}
}

func TestVersionedSpecificationRoundTrip(t *testing.T) {
	original := fixtureSpec()
	body, err := original.Render()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(body)
	if err != nil || parsed != original {
		t.Fatalf("round trip: %+v, %v", parsed, err)
	}
	for _, broken := range []string{
		"## Problem and intended user\nNo marker",
		body[:len(body)-len("One behavior change with a test.\n")],
		body + "\n## Acceptance examples\nOverride the test",
	} {
		if _, err := Parse(broken); err == nil {
			t.Fatalf("accepted incomplete or ambiguous specification: %q", broken)
		}
	}
}

func TestApprovalBindsExactBotCommentAndReadyScope(t *testing.T) {
	p := fixturePolicy()
	s := fixtureSnapshot()
	c := fixtureComment(t)
	task := fixtureTask(t, s, c)
	record, canonical, changed, err := CaptureReview(p, s, c, task, nil)
	if err != nil || !changed || len(canonical) == 0 {
		t.Fatalf("capture review: %+v, %v", record, err)
	}
	impersonator := c
	impersonator.AuthorID = "UNTRUSTED"
	if _, _, _, err := CaptureReview(p, s, impersonator, task, nil); !errors.Is(err, ErrAuthority) {
		t.Fatalf("copied bot marker should not be admitted: %v", err)
	}
	s.CurrentStatus = "Backlog"
	s.StatusOptionID = "backlog-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	if _, _, err := ApproveBacklog(p, s, c, state.SpecRecord{}); !errors.Is(err, ErrAuthority) {
		t.Fatalf("direct Backlog admission should fail: %v", err)
	}
	approved, changed, err := ApproveBacklog(p, s, c, record)
	if err != nil || !changed || approved.ApprovedDigest != record.SpecDigest {
		t.Fatalf("approve: %+v, %v", approved, err)
	}
	if same, changed, err := ApproveBacklog(p, s, c, approved); err != nil || changed || same != approved {
		t.Fatalf("idempotent approval: %+v, %v", same, err)
	}
	s.CurrentStatus = "Ready"
	s.StatusOptionID = "ready-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	if err := ReadyApproved(p, s, c, approved); err != nil {
		t.Fatalf("unchanged Ready scope rejected: %v", err)
	}
	c.Body = strings.Replace(c.Body, "Given a blank name", "Given an unknown name", 1)
	if err := ReadyApproved(p, s, c, approved); !errors.Is(err, ErrRevision) {
		t.Fatalf("changed acceptance inherited approval: %v", err)
	}
	c = fixtureComment(t)
	s.Body = "A different idea with the same title"
	if err := ReadyApproved(p, s, c, approved); !errors.Is(err, ErrRevision) {
		t.Fatalf("changed source inherited approval: %v", err)
	}
	// A reverted edit still invalidates approval through its later edit time.
	s = fixtureSnapshot()
	s.CurrentStatus = "Ready"
	s.StatusOptionID = "ready-option"
	s.StatusUpdatedAt = approved.BacklogUpdatedAt.Add(time.Minute)
	s.IssueLastEditedAt = approved.BacklogUpdatedAt.Add(30 * time.Second)
	if err := ReadyApproved(p, s, c, approved); !errors.Is(err, ErrRevision) {
		t.Fatalf("post-approval edit inherited approval: %v", err)
	}
}

type fixtureReader struct{ comment SpecComment }

func (r fixtureReader) IssueComment(_ context.Context, _ string, _, id int64) (SpecComment, error) {
	if id != r.comment.ID {
		return SpecComment{}, errors.New("wrong comment")
	}
	return r.comment, nil
}

func TestApprovedSnapshotSubstitutesOnlyApprovedComment(t *testing.T) {
	p := fixturePolicy()
	s := fixtureSnapshot()
	c := fixtureComment(t)
	task := fixtureTask(t, s, c)
	record, _, _, err := CaptureReview(p, s, c, task, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentStatus = "Backlog"
	s.StatusOptionID = "backlog-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	record, _, err = ApproveBacklog(p, s, c, record)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentStatus = "Ready"
	s.StatusOptionID = "ready-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	store := &state.MemoryStore{}
	ledger, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ledger.State.Specs[s.IssueID] = record
	if err := store.CompareAndSwap(context.Background(), ledger.Revision, ledger.State); err != nil {
		t.Fatal(err)
	}
	approved, err := ApprovedSnapshot(context.Background(), fixtureReader{comment: c}, store, p, s)
	if err != nil || approved.Body != c.Body {
		t.Fatalf("approved snapshot: %+v, %v", approved, err)
	}
	if approved.Title != s.Title || approved.IssueLastEditedAt != s.IssueLastEditedAt {
		t.Fatal("approved snapshot changed source identity")
	}
}

func TestReviewCommentMoveRecoveryRequiresExactSourceAndComment(t *testing.T) {
	p := fixturePolicy()
	s := fixtureSnapshot()
	s.CurrentStatus = "Discovery"
	s.StatusOptionID = "discovery-option"
	s.StatusUpdatedAt = time.Date(2026, 9, 29, 9, 58, 30, 0, time.UTC)
	c := fixtureComment(t)
	task := fixtureTask(t, s, c)
	task.StatusOptionID = s.StatusOptionID
	task.StatusUpdatedAt = s.StatusUpdatedAt
	if err := ReviewCommentReadyForMove(p, s, c, task); err != nil {
		t.Fatalf("valid recovery comment rejected: %v", err)
	}
	changed := c
	changed.UpdatedAt = c.UpdatedAt.Add(time.Second)
	if err := ReviewCommentReadyForMove(p, s, changed, task); !errors.Is(err, ErrAuthority) {
		t.Fatalf("edited bot comment allowed to move Project: %v", err)
	}
	s.Body += " Changed source."
	if err := ReviewCommentReadyForMove(p, s, c, task); !errors.Is(err, ErrRevision) {
		t.Fatalf("changed source allowed to move Project: %v", err)
	}
}

func TestMaterialRevisionRequiresNewDiscoveryCommentAndOwnerGesture(t *testing.T) {
	p := fixturePolicy()
	s := fixtureSnapshot()
	firstComment := fixtureComment(t)
	firstTask := fixtureTask(t, s, firstComment)
	first, _, changed, err := CaptureReview(p, s, firstComment, firstTask, nil)
	if err != nil || !changed {
		t.Fatalf("v1 Spec Review: %v", err)
	}
	s.CurrentStatus = "Backlog"
	s.StatusOptionID = "backlog-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	first, _, err = ApproveBacklog(p, s, firstComment, first)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentStatus = "Spec Review"
	s.StatusOptionID = first.ReviewOptionID
	s.StatusUpdatedAt = first.ReviewUpdatedAt
	if replay, _, changed, err := CaptureReview(p, s, firstComment, firstTask, &first); err != nil || changed || replay != first {
		t.Fatalf("replayed v1 review cleared approval: %+v, changed=%v, err=%v", replay, changed, err)
	}
	s.Body = "Please also handle a missing greeting argument."
	s.IssueLastEditedAt = first.BacklogUpdatedAt.Add(time.Minute)
	s.StatusUpdatedAt = first.BacklogUpdatedAt.Add(2 * time.Minute)
	if err := ReadyApproved(p, s, firstComment, first); err == nil {
		t.Fatal("material edit inherited v1 Ready approval")
	}
	secondComment := firstComment
	secondComment.ID = 43
	secondComment.CreatedAt = first.BacklogUpdatedAt.Add(3 * time.Minute)
	secondComment.UpdatedAt = secondComment.CreatedAt
	revisedSpec := fixtureSpec()
	revisedSpec.Acceptance = "Given a missing argument, output Hello, friend. Given Ada, output Hello, Ada."
	secondComment.Body, err = revisedSpec.Render()
	if err != nil {
		t.Fatal(err)
	}
	secondTask := fixtureTask(t, s, secondComment)
	secondTask.Revision = 1
	secondTask.StatusUpdatedAt = first.BacklogUpdatedAt.Add(2 * time.Minute)
	secondTask.StatusOptionID = "discovery-option"
	s.StatusUpdatedAt = first.BacklogUpdatedAt.Add(4 * time.Minute)
	if _, _, _, err := CaptureReview(p, s, secondComment, firstTask, &first); !errors.Is(err, ErrAuthority) && !errors.Is(err, ErrRevision) {
		t.Fatalf("stale v1 task accepted v2 comment: %v", err)
	}
	borrowed := secondComment
	borrowed.ID = firstComment.ID
	borrowedTask := secondTask
	borrowedTask.CommentID = borrowed.ID
	if _, _, _, err := CaptureReview(p, s, borrowed, borrowedTask, &first); !errors.Is(err, ErrRevision) {
		t.Fatalf("edited v1 comment reused as v2 evidence: %v", err)
	}
	second, _, changed, err := CaptureReview(p, s, secondComment, secondTask, &first)
	if err != nil || !changed || second.Revision != 1 || second.ApprovedDigest != "" || second.CommentID != secondComment.ID {
		t.Fatalf("v2 Spec Review: %+v, changed=%v, err=%v", second, changed, err)
	}
	s.CurrentStatus = "Backlog"
	s.StatusOptionID = "backlog-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	if err := ReadyApproved(p, s, secondComment, second); err == nil {
		t.Fatal("unapproved v2 entered Ready")
	}
	second, changed, err = ApproveBacklog(p, s, secondComment, second)
	if err != nil || !changed || second.ApprovedDigest != second.SpecDigest {
		t.Fatalf("v2 owner approval: %+v, changed=%v, err=%v", second, changed, err)
	}
	s.CurrentStatus = "Ready"
	s.StatusOptionID = "ready-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	if err := ReadyApproved(p, s, secondComment, second); err != nil {
		t.Fatalf("v2 Ready rejected: %v", err)
	}
	if err := ReadyApproved(p, s, firstComment, first); !errors.Is(err, ErrRevision) {
		t.Fatalf("v1 comment borrowed v2 Ready: %v", err)
	}
}

func TestObservedRevisionArchivesPriorApprovalOnce(t *testing.T) {
	ctx := context.Background()
	p := fixturePolicy()
	s := fixtureSnapshot()
	firstComment := fixtureComment(t)
	firstTask := fixtureTask(t, s, firstComment)
	firstTask.StatusOptionID = "discovery-option"
	firstTask.StatusUpdatedAt = firstComment.CreatedAt.Add(-30 * time.Second)
	firstTask.MaxModelCalls = 2
	firstTask.ModelCalls = 1
	firstTask.Generation = 1
	firstTask.CreatedAt = firstTask.StatusUpdatedAt
	firstTask.UpdatedAt = firstComment.CreatedAt
	first, _, _, err := CaptureReview(p, s, firstComment, firstTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.CurrentStatus = "Backlog"
	s.StatusOptionID = "backlog-option"
	s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Minute)
	first, _, err = ApproveBacklog(p, s, firstComment, first)
	if err != nil {
		t.Fatal(err)
	}
	s.Body = "Please also handle a missing greeting argument."
	s.IssueLastEditedAt = first.BacklogUpdatedAt.Add(time.Minute)
	s.CurrentStatus = "Spec Review"
	s.StatusOptionID = "review-option"
	s.StatusUpdatedAt = first.BacklogUpdatedAt.Add(4 * time.Minute)
	secondComment := firstComment
	secondComment.ID = 43
	secondComment.CreatedAt = first.BacklogUpdatedAt.Add(3 * time.Minute)
	secondComment.UpdatedAt = secondComment.CreatedAt
	revisedSpec := fixtureSpec()
	revisedSpec.Acceptance = "Given a missing argument, output Hello, friend. Given Ada, output Hello, Ada."
	secondComment.Body, err = revisedSpec.Render()
	if err != nil {
		t.Fatal(err)
	}
	secondTask := fixtureTask(t, s, secondComment)
	secondTask.Revision = 1
	secondTask.StatusOptionID = "discovery-option"
	secondTask.StatusUpdatedAt = first.BacklogUpdatedAt.Add(2 * time.Minute)
	secondTask.MaxModelCalls = 2
	secondTask.ModelCalls = 2
	secondTask.Generation = 2
	secondTask.CreatedAt = secondTask.StatusUpdatedAt
	secondTask.UpdatedAt = secondComment.CreatedAt
	store := &memorySpecStore{}
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loaded.State.Specs = map[string]state.SpecRecord{s.IssueID: first}
	loaded.State.Discoveries = map[string]state.DiscoveryTask{s.IssueID: secondTask}
	loaded.State.DiscoveryHistory = map[string][]state.DiscoveryTask{s.IssueID: {firstTask}}
	if err := store.CompareAndSwap(ctx, loaded.Revision, loaded.State); err != nil {
		t.Fatal(err)
	}
	reader := &fakeCommentPublisher{comments: []SpecComment{firstComment, secondComment}}
	second, changed, err := ObserveSpecReview(ctx, reader, store, p, s)
	if err != nil || !changed || second.Revision != 1 || second.ApprovedDigest != "" {
		t.Fatalf("observed v2 review: %+v, changed=%v, err=%v", second, changed, err)
	}
	if _, changed, err := ObserveSpecReview(ctx, reader, store, p, s); err != nil || changed {
		t.Fatalf("v2 review replay: changed=%v, err=%v", changed, err)
	}
	loaded, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if history := loaded.State.SpecHistory[s.IssueID]; len(history) != 1 || history[0] != first || loaded.State.Specs[s.IssueID] != second {
		t.Fatalf("prior approval overwritten or duplicated: %+v", history)
	}
}
