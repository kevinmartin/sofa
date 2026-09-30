package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func discoveryAdmission(issue string, number int64) DiscoveryAdmission {
	return DiscoveryAdmission{
		Repository:      "kevinmartin/sofa-disposable",
		IssueID:         issue,
		Issue:           number,
		ProjectID:       "P_private",
		ProjectItemID:   "PVTI_" + issue,
		StatusOptionID:  "discovery-option",
		StatusUpdatedAt: time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC),
		SourceDigest:    strings.Repeat("a", 64),
	}
}

func TestDiscoveryWIPReplayAndTerminalRecovery(t *testing.T) {
	ctx := context.Background()
	store := &MemoryStore{}
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return now },
	}
	first := discoveryAdmission("I_1", 1)
	task, created, err := engine.AdmitDiscovery(ctx, first, 2, 2)
	if err != nil || !created || task.Phase != DiscoveryPending {
		t.Fatalf("first admission: %+v, %v", task, err)
	}
	if _, created, err := engine.AdmitDiscovery(ctx, first, 2, 2); err != nil || created {
		t.Fatalf("duplicate consumed WIP or budget: %v, %v", created, err)
	}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_2", 2), 2, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_3", 3), 2, 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("third active Discovery bypassed WIP: %v", err)
	}
	owner := Owner{
		RunID:      "123",
		RunAttempt: 1,
	}
	fence, err := engine.ClaimDiscovery(ctx, "I_1", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ClaimDiscovery(ctx, "I_1", owner); !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate claim: %v", err)
	}
	if err := engine.RecoverDiscovery(ctx, "I_1", RunProof{
		Owner:      owner,
		Status:     "in_progress",
		ObservedAt: now,
	}); !errors.Is(err, ErrActive) {
		t.Fatalf("active run incorrectly reclaimed: %v", err)
	}
	proof := RunProof{
		Owner:      owner,
		Status:     "completed",
		Conclusion: "cancelled",
		ObservedAt: now,
	}
	if err := engine.RecoverDiscovery(ctx, "I_1", proof); err != nil {
		t.Fatal(err)
	}
	if err := engine.AssertDiscoveryOwner(ctx, fence); !errors.Is(err, ErrStale) {
		t.Fatalf("stale worker retained ownership: %v", err)
	}
	secondOwner := Owner{
		RunID:      "124",
		RunAttempt: 1,
	}
	secondFence, err := engine.ClaimDiscovery(ctx, "I_1", secondOwner)
	if err != nil || secondFence.Generation <= fence.Generation {
		t.Fatalf("reclaim: %+v, %v", secondFence, err)
	}
	if err := engine.RecoverDiscovery(ctx, "I_1", RunProof{
		Owner:      secondOwner,
		Status:     "completed",
		Conclusion: "failure",
		ObservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d := snapshot.State.Discoveries["I_1"]
	if d.ModelCalls != 2 || d.Phase != DiscoveryBlocked {
		t.Fatalf("retries reset aggregate model budget: %+v", d)
	}
	if _, err := engine.ClaimDiscovery(ctx, "I_1", secondOwner); !errors.Is(err, ErrClaimed) {
		t.Fatalf("exhausted task reclaimed: %v", err)
	}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_3", 3), 2, 2); err != nil {
		t.Fatalf("terminal task retained WIP: %v", err)
	}
}

func TestDiscoverySpecCompletionReleasesWIPAndFencesWorker(t *testing.T) {
	ctx := context.Background()
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) },
	}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_1", 1), 2, 1); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, "I_1", Owner{
		RunID:      "1",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	commentTime := time.Date(2026, 9, 29, 10, 1, 0, 0, time.UTC)
	if err := engine.PrepareDiscoveryPublication(ctx, fence, strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	first, err := engine.MarkDiscoveryPostAttempt(ctx, fence, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil || !first {
		t.Fatalf("first POST reservation: %v, %v", first, err)
	}
	first, err = engine.MarkDiscoveryPostAttempt(ctx, fence, strings.Repeat("b", 64), strings.Repeat("c", 64))
	if err != nil || first {
		t.Fatalf("duplicate POST reservation: %v, %v", first, err)
	}
	if err := engine.CompleteDiscovery(ctx, fence, strings.Repeat("b", 64), 42, "BOT_node", commentTime, commentTime); err != nil {
		t.Fatal(err)
	}
	if err := engine.AssertDiscoveryOwner(ctx, fence); !errors.Is(err, ErrStale) {
		t.Fatalf("finished worker retained write authority: %v", err)
	}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_2", 2), 1, 1); err != nil {
		t.Fatalf("review-ready task retained active slot: %v", err)
	}
}

func TestDiscoveryPublicationKeepsOriginalProducerAcrossRecoveryRuns(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return now },
	}
	admission := discoveryAdmission("I_1", 1)
	if _, _, err := engine.AdmitDiscovery(ctx, admission, 2, 1); err != nil {
		t.Fatal(err)
	}
	first := Owner{
		RunID:      "first",
		RunAttempt: 1,
	}
	fence, err := engine.ClaimDiscovery(ctx, admission.IssueID, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.PrepareDiscoveryPublication(ctx, fence, strings.Repeat("b", 64), strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []Owner{
		{
			RunID:      "first",
			RunAttempt: 1,
		},
		{
			RunID:      "second",
			RunAttempt: 1,
		},
	} {
		if err := engine.RecoverDiscovery(ctx, admission.IssueID, RunProof{
			Owner:      owner,
			Status:     "completed",
			Conclusion: "failure",
			ObservedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if owner.RunID == "first" {
			if _, err := engine.ClaimDiscovery(ctx, admission.IssueID, Owner{
				RunID:      "second",
				RunAttempt: 1,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	task := snapshot.State.Discoveries[admission.IssueID]
	if task.Phase != DiscoveryPending || task.ModelCalls != 1 || task.Publication == nil || task.Publication.Producer != first {
		t.Fatalf("recovery changed original publication identity or budget: %+v", task)
	}
}

func TestUnapprovedDiscoveryResetKeepsBudgetAndDiscardsProposal(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(5 * time.Hour) },
	}
	first := discoveryAdmission("I_1", 1)
	if _, _, err := engine.AdmitDiscovery(ctx, first, 2, 3); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, first.IssueID, Owner{
		RunID:      "first",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := strings.Repeat("b", 64)
	if err := engine.PrepareDiscoveryPublication(ctx, fence, proposal, strings.Repeat("c", 64)); err != nil {
		t.Fatal(err)
	}
	commentTime := base.Add(time.Hour)
	if err := engine.CompleteDiscovery(ctx, fence, proposal, 42, "bot", commentTime, commentTime); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State.Specs = map[string]SpecRecord{}
	snapshot.State.Specs[first.IssueID] = SpecRecord{
		Repository:       first.Repository,
		IssueID:          first.IssueID,
		Issue:            first.Issue,
		ProjectID:        first.ProjectID,
		ProjectItemID:    first.ProjectItemID,
		SourceDigest:     first.SourceDigest,
		SpecDigest:       proposal,
		CommentID:        42,
		CommentAuthorID:  "bot",
		CommentCreatedAt: commentTime,
		CommentUpdatedAt: commentTime,
		ReviewOptionID:   "review",
		ReviewUpdatedAt:  base.Add(2 * time.Hour),
	}
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.StatusUpdatedAt = base.Add(3 * time.Hour)
	changed.SourceDigest = strings.Repeat("d", 64)
	task, created, err := engine.AdmitDiscovery(ctx, changed, 2, 3)
	if err != nil || !created || task.Revision != 0 || task.ModelCalls != 1 || task.Phase != DiscoveryPending {
		t.Fatalf("unapproved reset: %+v, created=%v, err=%v", task, created, err)
	}
	snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.State.Specs[first.IssueID]; ok {
		t.Fatal("rejected proposal retained approval authority")
	}
	if len(snapshot.State.DiscoveryHistory[first.IssueID]) != 0 || len(snapshot.State.DiscoveryResetHistory[first.IssueID]) != 1 || snapshot.State.DiscoveryResetHistory[first.IssueID][0].SpecDigest != proposal {
		t.Fatal("rejected proposal was not separated from approved history")
	}
	if err := engine.AssertDiscoveryOwner(ctx, fence); !errors.Is(err, ErrStale) {
		t.Fatalf("old worker retained authority: %v", err)
	}
	if _, created, err := engine.AdmitDiscovery(ctx, changed, 2, 3); err != nil || created {
		t.Fatalf("reset replay changed state: created=%v, err=%v", created, err)
	}
	tooEarly := changed
	tooEarly.StatusUpdatedAt = first.StatusUpdatedAt
	if _, _, err := engine.AdmitDiscovery(ctx, tooEarly, 2, 3); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("old Project transition was accepted: %v", err)
	}
}

func TestFailedDiscoveryResetRequiresLaterOwnerTransition(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(time.Hour) },
	}
	first := discoveryAdmission("I_1", 1)
	if _, _, err := engine.AdmitDiscovery(ctx, first, 2, 2); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, first.IssueID, Owner{
		RunID:      "failed",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.FailDiscovery(ctx, fence, "validation"); err != nil {
		t.Fatal(err)
	}
	if _, created, err := engine.AdmitDiscovery(ctx, first, 2, 2); err != nil || created {
		t.Fatalf("same admission unexpectedly reset failure: %v, %v", created, err)
	}
	next := first
	next.StatusUpdatedAt = first.StatusUpdatedAt.Add(time.Minute)
	task, created, err := engine.AdmitDiscovery(ctx, next, 2, 2)
	if err != nil || !created || task.ModelCalls != 1 || task.Revision != 0 || task.Phase != DiscoveryPending {
		t.Fatalf("later owner transition failed to reset: %+v, %v, %v", task, created, err)
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.DiscoveryResetHistory[first.IssueID]) != 1 || snapshot.State.DiscoveryResetHistory[first.IssueID][0].Failure != "validation" {
		t.Fatal("failed task evidence was lost")
	}
	secondFence, err := engine.ClaimDiscovery(ctx, first.IssueID, Owner{
		RunID:      "second-failure",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.FailDiscovery(ctx, secondFence, "validation"); err != nil {
		t.Fatal(err)
	}
	exhausted := next
	exhausted.StatusUpdatedAt = next.StatusUpdatedAt.Add(time.Minute)
	if _, _, err := engine.AdmitDiscovery(ctx, exhausted, 2, 2); !errors.Is(err, ErrLimit) {
		t.Fatalf("exhausted task produced an unbounded reset: %v", err)
	}
}

func TestUnapprovedLaterRevisionRestoresLastApprovedSpec(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(8 * time.Hour) },
	}
	first := discoveryAdmission("I_1", 1)
	oldSource := first.SourceDigest
	firstSpec := strings.Repeat("b", 64)
	old := DiscoveryTask{
		Repository:       first.Repository,
		IssueID:          first.IssueID,
		Issue:            first.Issue,
		ProjectID:        first.ProjectID,
		ProjectItemID:    first.ProjectItemID,
		StatusOptionID:   first.StatusOptionID,
		StatusUpdatedAt:  first.StatusUpdatedAt,
		SourceDigest:     oldSource,
		Phase:            DiscoveryReview,
		Revision:         0,
		Generation:       1,
		ModelCalls:       1,
		MaxModelCalls:    3,
		SpecDigest:       firstSpec,
		CommentID:        42,
		CommentAuthorID:  "bot",
		CommentCreatedAt: base.Add(time.Hour),
		CommentUpdatedAt: base.Add(time.Hour),
		CreatedAt:        base,
		UpdatedAt:        base.Add(time.Hour),
	}
	newSource := strings.Repeat("c", 64)
	secondSpec := strings.Repeat("d", 64)
	current := old
	current.Revision = 1
	current.Generation++
	current.StatusUpdatedAt = base.Add(3 * time.Hour)
	current.SourceDigest = newSource
	current.SpecDigest = secondSpec
	current.CommentID = 43
	current.CommentCreatedAt = base.Add(4 * time.Hour)
	current.CommentUpdatedAt = current.CommentCreatedAt
	current.ModelCalls = 2
	current.CreatedAt = base.Add(3 * time.Hour)
	current.UpdatedAt = base.Add(4 * time.Hour)
	makeRecord := func(task DiscoveryTask, review, backlog time.Time) SpecRecord {
		return SpecRecord{
			Revision:         task.Revision,
			Repository:       task.Repository,
			IssueID:          task.IssueID,
			Issue:            task.Issue,
			ProjectID:        task.ProjectID,
			ProjectItemID:    task.ProjectItemID,
			SourceDigest:     task.SourceDigest,
			SpecDigest:       task.SpecDigest,
			CommentID:        task.CommentID,
			CommentAuthorID:  task.CommentAuthorID,
			CommentCreatedAt: task.CommentCreatedAt,
			CommentUpdatedAt: task.CommentUpdatedAt,
			ReviewOptionID:   "review",
			ReviewUpdatedAt:  review,
			ApprovedDigest:   "",
			BacklogOptionID:  "",
			BacklogUpdatedAt: backlog,
		}
	}
	approved := makeRecord(old, base.Add(90*time.Minute), base.Add(2*time.Hour))
	approved.ApprovedDigest = firstSpec
	approved.BacklogOptionID = "backlog"
	rejected := makeRecord(current, base.Add(5*time.Hour), time.Time{})
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State.Specs = map[string]SpecRecord{}
	snapshot.State.SpecHistory = map[string][]SpecRecord{}
	snapshot.State.DiscoveryHistory = map[string][]DiscoveryTask{}
	snapshot.State.Discoveries[first.IssueID] = current
	snapshot.State.DiscoveryHistory[first.IssueID] = []DiscoveryTask{old}
	snapshot.State.Specs[first.IssueID] = rejected
	snapshot.State.SpecHistory[first.IssueID] = []SpecRecord{approved}
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	newAdmission := first
	newAdmission.StatusUpdatedAt = base.Add(6 * time.Hour)
	newAdmission.SourceDigest = newSource // A manual return to Discovery can keep the idea unchanged.
	task, created, err := engine.AdmitDiscovery(ctx, newAdmission, 2, 3)
	if err != nil || !created || task.Revision != 1 || task.ModelCalls != 2 {
		t.Fatalf("unapproved v1 reset: %+v, %v, %v", task, created, err)
	}
	snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State.Specs[first.IssueID] != approved || len(snapshot.State.SpecHistory[first.IssueID]) != 0 || len(snapshot.State.DiscoveryHistory[first.IssueID]) != 1 || len(snapshot.State.DiscoveryResetHistory[first.IssueID]) != 1 {
		t.Fatal("last approved specification or discovery histories were corrupted")
	}
}
