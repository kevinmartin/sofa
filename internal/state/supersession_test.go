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
