package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestApprovedRevisionSupersedesAttemptWithoutResettingBudget(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(9 * time.Hour) },
	}
	oldSpec := strings.Repeat("a", 64)
	newSpec := strings.Repeat("b", 64)
	oldSource := strings.Repeat("c", 64)
	newSource := strings.Repeat("d", 64)
	firstReady := base.Add(3 * time.Hour)
	oldAdmission := Admission{
		Repository:      "owner/consumer",
		Issue:           7,
		SpecDigest:      oldSpec,
		ConfigDigest:    strings.Repeat("e", 64),
		BaseSHA:         strings.Repeat("f", 40),
		ProjectID:       "P",
		ProjectItemID:   "PVTI",
		StatusOptionID:  "ready",
		StatusUpdatedAt: firstReady,
	}
	oldAttempt, created, err := engine.Admit(ctx, oldAdmission, Limits{
		ModelCalls:            3,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        1200,
	})
	if err != nil || !created {
		t.Fatalf("v1 admission: %v", err)
	}
	owner := Owner{
		RunID:      "run-one",
		RunAttempt: 1,
	}
	fence, err := engine.Claim(ctx, oldAttempt.ID, owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Charge(ctx, fence, Counters{
		ModelCalls:     1,
		RuntimeSeconds: 100,
	}); err != nil {
		t.Fatal(err)
	}
	oldTask := DiscoveryTask{
		Repository:       "owner/consumer",
		IssueID:          "I_7",
		Issue:            7,
		ProjectID:        "P",
		ProjectItemID:    "PVTI",
		StatusOptionID:   "discovery",
		StatusUpdatedAt:  base,
		SourceDigest:     oldSource,
		Phase:            DiscoveryReview,
		Generation:       1,
		ModelCalls:       1,
		MaxModelCalls:    1,
		SpecDigest:       oldSpec,
		CommentID:        17,
		CommentAuthorID:  "bot",
		CommentCreatedAt: base.Add(time.Hour),
		CommentUpdatedAt: base.Add(time.Hour),
		CreatedAt:        base,
		UpdatedAt:        base.Add(time.Hour),
	}
	oldRecord := SpecRecord{
		Repository:       "owner/consumer",
		IssueID:          "I_7",
		Issue:            7,
		ProjectID:        "P",
		ProjectItemID:    "PVTI",
		SourceDigest:     oldSource,
		SpecDigest:       oldSpec,
		CommentID:        17,
		CommentAuthorID:  "bot",
		CommentCreatedAt: base.Add(time.Hour),
		CommentUpdatedAt: base.Add(time.Hour),
		ReviewOptionID:   "review",
		ReviewUpdatedAt:  base.Add(90 * time.Minute),
		ApprovedDigest:   oldSpec,
		BacklogOptionID:  "backlog",
		BacklogUpdatedAt: base.Add(2 * time.Hour),
	}
	loaded, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loaded.State.Discoveries = map[string]DiscoveryTask{}
	loaded.State.Specs = map[string]SpecRecord{}
	loaded.State.Discoveries["I_7"] = oldTask
	loaded.State.Specs["I_7"] = oldRecord
	if err := store.CompareAndSwap(ctx, loaded.Revision, loaded.State); err != nil {
		t.Fatal(err)
	}
	changed := DiscoveryAdmission{
		Repository:      "owner/consumer",
		IssueID:         "I_7",
		Issue:           7,
		ProjectID:       "P",
		ProjectItemID:   "PVTI",
		StatusOptionID:  "discovery",
		StatusUpdatedAt: base.Add(4 * time.Hour),
		SourceDigest:    newSource,
	}
	if _, _, err := engine.AdmitDiscovery(ctx, changed, 2, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cap accepted: %v", err)
	}
	tooEarly := changed
	tooEarly.StatusUpdatedAt = oldRecord.BacklogUpdatedAt
	if _, _, err := engine.AdmitDiscovery(ctx, tooEarly, 2, 2); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("owner transition before approval accepted: %v", err)
	}
	wrongItem := changed
	wrongItem.ProjectItemID = "PVTI_other"
	if _, _, err := engine.AdmitDiscovery(ctx, wrongItem, 2, 2); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("cross-item revision accepted: %v", err)
	}
	revised, created, err := engine.AdmitDiscovery(ctx, changed, 2, 1)
	if err != nil || !created || revised.Revision != 1 || revised.ModelCalls != 1 || revised.Phase != DiscoveryBlocked || revised.Failure != "budget" {
		t.Fatalf("bounded v2 budget hold: %+v, created=%v, err=%v", revised, created, err)
	}
	if _, err := engine.ClaimDiscovery(ctx, "I_7", Owner{
		RunID:      "run-two",
		RunAttempt: 1,
	}); !errors.Is(err, ErrClaimed) {
		t.Fatalf("exhausted v2 consumed another prompt: %v", err)
	}
	revised, created, err = engine.AdmitDiscovery(ctx, changed, 2, 2)
	if err != nil || created || revised.Revision != 1 || revised.ModelCalls != 1 || revised.MaxModelCalls != 2 || revised.Phase != DiscoveryPending {
		t.Fatalf("explicit cap increase did not resume v2: %+v, created=%v, err=%v", revised, created, err)
	}
	if err := engine.AssertOwner(ctx, fence); !errors.Is(err, ErrStale) {
		t.Fatalf("v1 worker retained write authority: %v", err)
	}
	if err := engine.MarkDispatched(ctx, oldAttempt.ID); !errors.Is(err, ErrStale) {
		t.Fatalf("v1 dispatch retained authority: %v", err)
	}
	loaded, _ = store.Load(ctx)
	if len(loaded.State.DiscoveryHistory["I_7"]) != 1 || loaded.State.DiscoveryHistory["I_7"][0] != oldTask || loaded.State.Attempts[oldAttempt.ID].SupersededAt.IsZero() {
		t.Fatal("v1 evidence was lost or attempt not superseded")
	}
	if _, found, err := CurrentAttemptForIssue(loaded.State, "I_7"); err != nil || found {
		t.Fatalf("v1 PR borrowed during v2 discovery: %v, found=%v", err, found)
	}
	if _, _, err := engine.Admit(ctx, oldAdmission, oldAttempt.Limits); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("v1 admission replay revived: %v", err)
	}
	newAdmission := oldAdmission
	newAdmission.SpecDigest = newSpec
	newAdmission.StatusUpdatedAt = base.Add(8 * time.Hour)
	if _, _, err := engine.Admit(ctx, newAdmission, oldAttempt.Limits); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("unapproved v2 admitted: %v", err)
	}
	loaded, _ = store.Load(ctx)
	newRecord := oldRecord
	newRecord.Revision = 1
	newRecord.SourceDigest = newSource
	newRecord.SpecDigest = newSpec
	newRecord.ApprovedDigest = newSpec
	newRecord.CommentID = 18
	newRecord.CommentCreatedAt = base.Add(5 * time.Hour)
	newRecord.CommentUpdatedAt = newRecord.CommentCreatedAt
	newRecord.ReviewUpdatedAt = base.Add(6 * time.Hour)
	newRecord.BacklogUpdatedAt = base.Add(7 * time.Hour)
	loaded.State.SpecHistory = map[string][]SpecRecord{}
	loaded.State.SpecHistory["I_7"] = []SpecRecord{oldRecord}
	loaded.State.Specs["I_7"] = newRecord
	if err := store.CompareAndSwap(ctx, loaded.Revision, loaded.State); err != nil {
		t.Fatal(err)
	}
	newAttempt, created, err := engine.Admit(ctx, newAdmission, oldAttempt.Limits)
	if err != nil || !created || newAttempt.SpecRevision != 1 || newAttempt.Counts.ModelCalls != 1 || newAttempt.Counts.RuntimeSeconds != 100 {
		t.Fatalf("v2 Ready admission did not carry budget: %+v, created=%v, err=%v", newAttempt, created, err)
	}
	if replay, created, err := engine.Admit(ctx, newAdmission, oldAttempt.Limits); err != nil || created || replay.ID != newAttempt.ID {
		t.Fatalf("v2 replay duplicated attempt: %+v, created=%v, err=%v", replay, created, err)
	}
	loaded, _ = store.Load(ctx)
	current, found, err := CurrentAttemptForIssue(loaded.State, "I_7")
	if err != nil || !found || current.ID != newAttempt.ID {
		t.Fatalf("current attempt selected stale evidence: %+v, found=%v, err=%v", current, found, err)
	}
	thirdReady := newAdmission
	thirdReady.StatusUpdatedAt = thirdReady.StatusUpdatedAt.Add(time.Minute)
	if _, _, err := engine.Admit(ctx, thirdReady, oldAttempt.Limits); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("same v2 approval minted another attempt: %v", err)
	}
}

func TestReadyAdmitsLaterApprovedRevisionWithoutIntermediateAttempt(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(12 * time.Hour) },
	}
	digest := func(char string) string { return strings.Repeat(char, 64) }
	firstReady := Admission{
		Repository:      "owner/consumer",
		Issue:           7,
		SpecDigest:      digest("a"),
		ConfigDigest:    digest("f"),
		BaseSHA:         strings.Repeat("e", 40),
		ProjectID:       "P",
		ProjectItemID:   "PVTI",
		StatusOptionID:  "ready",
		StatusUpdatedAt: base.Add(3 * time.Hour),
	}
	limits := Limits{
		ModelCalls:            3,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        1200,
	}
	prior, created, err := engine.Admit(ctx, firstReady, limits)
	if err != nil || !created {
		t.Fatalf("first admission: %+v, %v", prior, err)
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prior = snapshot.State.Attempts[prior.ID]
	prior.Generation++
	prior.Counts.ModelCalls = 1
	prior.Phase = Blocked
	prior.Failure = "human-change"
	prior.SupersededAt = base.Add(4 * time.Hour)
	prior.SupersededSourceDigest = digest("2")
	snapshot.State.Attempts[prior.ID] = prior
	makeRecord := func(revision int64, source, spec string, comment int64, offset time.Duration) SpecRecord {
		return SpecRecord{
			Revision:         revision,
			Repository:       firstReady.Repository,
			IssueID:          "I_7",
			Issue:            firstReady.Issue,
			ProjectID:        firstReady.ProjectID,
			ProjectItemID:    firstReady.ProjectItemID,
			SourceDigest:     source,
			SpecDigest:       spec,
			CommentID:        comment,
			CommentAuthorID:  "bot",
			CommentCreatedAt: base.Add(offset),
			CommentUpdatedAt: base.Add(offset),
			ReviewOptionID:   "review",
			ReviewUpdatedAt:  base.Add(offset + 10*time.Minute),
			ApprovedDigest:   spec,
			BacklogOptionID:  "backlog",
			BacklogUpdatedAt: base.Add(offset + 20*time.Minute),
		}
	}
	v0 := makeRecord(0, digest("1"), firstReady.SpecDigest, 10, time.Hour)
	v1 := makeRecord(1, digest("2"), digest("b"), 11, 5*time.Hour)
	v2 := makeRecord(2, digest("3"), digest("c"), 12, 7*time.Hour)
	snapshot.State.Specs = map[string]SpecRecord{"I_7": v2}
	snapshot.State.SpecHistory = map[string][]SpecRecord{"I_7": {v0, v1}}
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	latest := firstReady
	latest.SpecDigest = v2.SpecDigest
	latest.StatusUpdatedAt = base.Add(9 * time.Hour)
	badSnapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	badPrior := badSnapshot.State.Attempts[prior.ID]
	badPrior.SupersededSourceDigest = digest("9")
	badSnapshot.State.Attempts[prior.ID] = badPrior
	if err := store.CompareAndSwap(ctx, badSnapshot.Revision, badSnapshot.State); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Admit(ctx, latest, limits); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("unrelated supersession source admitted: %v", err)
	}
	goodSnapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	goodSnapshot.State.Attempts[prior.ID] = prior
	if err := store.CompareAndSwap(ctx, goodSnapshot.Revision, goodSnapshot.State); err != nil {
		t.Fatal(err)
	}
	result, created, err := engine.Admit(ctx, latest, limits)
	if err != nil || !created || result.SpecRevision != 2 || result.Counts.ModelCalls != 1 {
		t.Fatalf("later approved revision blocked: %+v, created=%v, err=%v", result, created, err)
	}
	if replay, created, err := engine.Admit(ctx, latest, limits); err != nil || created || replay.ID != result.ID {
		t.Fatalf("later revision replay changed attempt: %+v, created=%v, err=%v", replay, created, err)
	}
	// The supersession must bind to v1, the record that immediately
	// followed the old attempt, rather than accepting an unrelated digest.
	wrong := firstReady
	wrong.SpecDigest = digest("d")
	wrong.StatusUpdatedAt = latest.StatusUpdatedAt.Add(time.Minute)
	if _, _, err := engine.Admit(ctx, wrong, limits); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("unapproved digest accepted: %v", err)
	}
}

func TestReadyAfterUnapprovedRevisionResetRetainsOriginalSupersession(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	store := &MemoryStore{}
	engine := Engine{
		Store: store,
		Now:   func() time.Time { return base.Add(12 * time.Hour) },
	}
	digest := func(char string) string { return strings.Repeat(char, 64) }
	limits := Limits{
		ModelCalls:            4,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        1200,
	}
	ready0 := Admission{
		Repository:      "owner/consumer",
		Issue:           7,
		SpecDigest:      digest("a"),
		ConfigDigest:    digest("f"),
		BaseSHA:         strings.Repeat("e", 40),
		ProjectID:       "P",
		ProjectItemID:   "PVTI",
		StatusOptionID:  "ready",
		StatusUpdatedAt: base.Add(3 * time.Hour),
	}
	oldAttempt, _, err := engine.Admit(ctx, ready0, limits)
	if err != nil {
		t.Fatal(err)
	}
	oldFence, err := engine.Claim(ctx, oldAttempt.ID, Owner{
		RunID:      "old",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Charge(ctx, oldFence, Counters{
		ModelCalls: 1,
	}); err != nil {
		t.Fatal(err)
	}
	oldTask := DiscoveryTask{
		Repository:       ready0.Repository,
		IssueID:          "I_7",
		Issue:            ready0.Issue,
		ProjectID:        ready0.ProjectID,
		ProjectItemID:    ready0.ProjectItemID,
		StatusOptionID:   "discovery",
		StatusUpdatedAt:  base,
		SourceDigest:     digest("1"),
		Phase:            DiscoveryReview,
		Generation:       1,
		ModelCalls:       1,
		MaxModelCalls:    4,
		SpecDigest:       ready0.SpecDigest,
		CommentID:        10,
		CommentAuthorID:  "bot",
		CommentCreatedAt: base.Add(time.Hour),
		CommentUpdatedAt: base.Add(time.Hour),
		CreatedAt:        base,
		UpdatedAt:        base.Add(time.Hour),
	}
	oldSpec := SpecRecord{
		Repository:       oldTask.Repository,
		IssueID:          oldTask.IssueID,
		Issue:            oldTask.Issue,
		ProjectID:        oldTask.ProjectID,
		ProjectItemID:    oldTask.ProjectItemID,
		SourceDigest:     oldTask.SourceDigest,
		SpecDigest:       oldTask.SpecDigest,
		CommentID:        oldTask.CommentID,
		CommentAuthorID:  oldTask.CommentAuthorID,
		CommentCreatedAt: oldTask.CommentCreatedAt,
		CommentUpdatedAt: oldTask.CommentUpdatedAt,
		ReviewOptionID:   "review",
		ReviewUpdatedAt:  base.Add(90 * time.Minute),
		ApprovedDigest:   oldTask.SpecDigest,
		BacklogOptionID:  "backlog",
		BacklogUpdatedAt: base.Add(2 * time.Hour),
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State.Discoveries = map[string]DiscoveryTask{"I_7": oldTask}
	snapshot.State.Specs = map[string]SpecRecord{"I_7": oldSpec}
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	firstSource := digest("2")
	firstDiscovery := DiscoveryAdmission{
		Repository:      ready0.Repository,
		IssueID:         oldTask.IssueID,
		Issue:           oldTask.Issue,
		ProjectID:       oldTask.ProjectID,
		ProjectItemID:   oldTask.ProjectItemID,
		StatusOptionID:  "discovery",
		StatusUpdatedAt: base.Add(4 * time.Hour),
		SourceDigest:    firstSource,
	}
	if _, created, err := engine.AdmitDiscovery(ctx, firstDiscovery, 2, 4); err != nil || !created {
		t.Fatalf("first revision admission: %v, %v", created, err)
	}
	if err := engine.AssertOwner(ctx, oldFence); !errors.Is(err, ErrStale) {
		t.Fatalf("old delivery worker retained authority: %v", err)
	}
	firstFence, err := engine.ClaimDiscovery(ctx, oldTask.IssueID, Owner{
		RunID:      "first-discovery",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstProposal := digest("b")
	if err := engine.PrepareDiscoveryPublication(ctx, firstFence, firstProposal, digest("c")); err != nil {
		t.Fatal(err)
	}
	if err := engine.CompleteDiscovery(ctx, firstFence, firstProposal, 11, "bot", base.Add(5*time.Hour), base.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstRecord := oldSpec
	firstRecord.Revision = 1
	firstRecord.SourceDigest = firstSource
	firstRecord.SpecDigest = firstProposal
	firstRecord.CommentID = 11
	firstRecord.CommentCreatedAt = base.Add(5 * time.Hour)
	firstRecord.CommentUpdatedAt = firstRecord.CommentCreatedAt
	firstRecord.ReviewUpdatedAt = base.Add(5*time.Hour + 10*time.Minute)
	firstRecord.ApprovedDigest = ""
	firstRecord.BacklogOptionID = ""
	firstRecord.BacklogUpdatedAt = time.Time{}
	snapshot.State.SpecHistory = map[string][]SpecRecord{"I_7": {oldSpec}}
	snapshot.State.Specs[oldTask.IssueID] = firstRecord
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	resetDiscovery := firstDiscovery
	resetDiscovery.StatusUpdatedAt = base.Add(6 * time.Hour)
	resetDiscovery.SourceDigest = digest("3")
	resetTask, created, err := engine.AdmitDiscovery(ctx, resetDiscovery, 2, 4)
	if err != nil || !created || resetTask.Revision != 1 || resetTask.ModelCalls != 2 {
		t.Fatalf("unapproved reset: %+v, %v, %v", resetTask, created, err)
	}
	finalFence, err := engine.ClaimDiscovery(ctx, oldTask.IssueID, Owner{
		RunID:      "final-discovery",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalProposal := digest("d")
	if err := engine.PrepareDiscoveryPublication(ctx, finalFence, finalProposal, digest("e")); err != nil {
		t.Fatal(err)
	}
	if err := engine.CompleteDiscovery(ctx, finalFence, finalProposal, 12, "bot", base.Add(7*time.Hour), base.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	finalRecord := oldSpec
	finalRecord.Revision = 1
	finalRecord.SourceDigest = resetDiscovery.SourceDigest
	finalRecord.SpecDigest = finalProposal
	finalRecord.CommentID = 12
	finalRecord.CommentCreatedAt = base.Add(7 * time.Hour)
	finalRecord.CommentUpdatedAt = finalRecord.CommentCreatedAt
	finalRecord.ReviewUpdatedAt = base.Add(7*time.Hour + 10*time.Minute)
	finalRecord.ApprovedDigest = finalProposal
	finalRecord.BacklogUpdatedAt = base.Add(8 * time.Hour)
	snapshot.State.SpecHistory = map[string][]SpecRecord{"I_7": {oldSpec}}
	snapshot.State.Specs[oldTask.IssueID] = finalRecord
	if err := store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	ready1 := ready0
	ready1.SpecDigest = finalProposal
	ready1.StatusUpdatedAt = base.Add(9 * time.Hour)
	newAttempt, created, err := engine.Admit(ctx, ready1, limits)
	if err != nil || !created || newAttempt.SpecRevision != 1 || newAttempt.Counts.ModelCalls != 1 {
		t.Fatalf("Ready after unapproved reset was rejected: %+v, %v, %v", newAttempt, created, err)
	}
	snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.DiscoveryResetHistory[oldTask.IssueID]) != 1 || snapshot.State.DiscoveryResetHistory[oldTask.IssueID][0].SourceDigest != firstSource || snapshot.State.Attempts[oldAttempt.ID].SupersededSourceDigest != firstSource {
		t.Fatal("original supersession source was not preserved")
	}
}
