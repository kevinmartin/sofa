package state

import (
	"context"
	"errors"
	"testing"
	"time"
)

func boardRecord(stage, option string, at time.Time) BoardProjection {
	return BoardProjection{
		Repository:    "owner/repo",
		IssueID:       "I_1",
		ProjectID:     "P_1",
		ProjectItemID: "PVTI_1",
		Stage:         stage,
		OptionID:      option,
		UpdatedAt:     at,
	}
}

func TestBoardProjectionOwnerHandoffAndPendingRecovery(t *testing.T) {
	ctx := context.Background()
	engine := Engine{
		Store: &MemoryStore{},
	}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	inbox := boardRecord("inbox", "inbox-option", base)
	if err := engine.ObserveBoard(ctx, inbox); err != nil {
		t.Fatal(err)
	}
	ownerDiscovery := boardRecord("discovery", "discovery-option", base.Add(time.Minute))
	if err := engine.ObserveBoard(ctx, ownerDiscovery); err != nil {
		t.Fatalf("restricted Project owner move rejected: %v", err)
	}
	if err := engine.BeginBoardMove(ctx, ownerDiscovery, "spec_review", "review-option"); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginBoardMove(ctx, ownerDiscovery, "spec_review", "review-option"); err != nil {
		t.Fatalf("identical replay was not idempotent: %v", err)
	}
	pending, ok, err := engine.PendingBoardMove(ctx, inbox.IssueID)
	if err != nil || !ok || pending.PendingStage != "spec_review" {
		t.Fatalf("pending move = %+v, %t, %v", pending, ok, err)
	}
	if err := engine.ObserveBoard(ctx, ownerDiscovery); err != nil {
		t.Fatalf("lost response retry source rejected: %v", err)
	}
	review := boardRecord("spec_review", "review-option", base.Add(2*time.Minute))
	if err := engine.ObserveBoard(ctx, review); err != nil {
		t.Fatalf("pending move completion rejected: %v", err)
	}
	if _, ok, err := engine.PendingBoardMove(ctx, inbox.IssueID); err != nil || ok {
		t.Fatalf("pending write not cleared: %t, %v", ok, err)
	}
	backlog := boardRecord("backlog", "backlog-option", base.Add(3*time.Minute))
	if err := engine.ObserveBoard(ctx, backlog); err != nil {
		t.Fatalf("owner Backlog approval move rejected: %v", err)
	}
	ready := boardRecord("ready", "ready-option", base.Add(4*time.Minute))
	if err := engine.ObserveBoard(ctx, ready); err != nil {
		t.Fatalf("owner Ready move rejected: %v", err)
	}
	if err := engine.BeginBoardMove(ctx, ready, "backlog", "backlog-option"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("factory could target owner approval state: %v", err)
	}
}

func TestBoardProjectionNeverFightsManualOrLateChange(t *testing.T) {
	ctx := context.Background()
	engine := Engine{
		Store: &MemoryStore{},
	}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	building := boardRecord("building", "building-option", base)
	if err := engine.ObserveBoard(ctx, building); err != nil {
		t.Fatal(err)
	}
	manual := boardRecord("verification", "verification-option", base.Add(time.Minute))
	if err := engine.ObserveBoard(ctx, manual); !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected manual move was accepted: %v", err)
	}
	if err := engine.ObserveBoard(ctx, boardRecord("ready", "ready-option", base.Add(-time.Minute))); !errors.Is(err, ErrConflict) {
		t.Fatalf("late status overwrote current state: %v", err)
	}
	if err := engine.BeginBoardMove(ctx, building, "verification", "verification-option"); err != nil {
		t.Fatal(err)
	}
	third := boardRecord("review", "review-option", base.Add(2*time.Minute))
	if err := engine.ObserveBoard(ctx, third); !errors.Is(err, ErrConflict) {
		t.Fatalf("third-party move incorrectly fulfilled write intent: %v", err)
	}
	if _, ok, err := engine.PendingBoardMove(ctx, building.IssueID); err != nil || !ok {
		t.Fatalf("conflict erased pending intent: %t, %v", ok, err)
	}
}

func TestBoardProjectionOwnerCanResetSpecReviewToDiscovery(t *testing.T) {
	ctx := context.Background()
	engine := Engine{Store: &MemoryStore{}}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	review := boardRecord("spec_review", "review-option", base)
	if err := engine.ObserveBoard(ctx, review); err != nil {
		t.Fatal(err)
	}
	discovery := boardRecord("discovery", "discovery-option", base.Add(time.Minute))
	if err := engine.ObserveBoard(ctx, discovery); err != nil {
		t.Fatalf("owner revision was rejected: %v", err)
	}
	if err := engine.BeginBoardMove(ctx, discovery, "spec_review", "review-option"); err != nil {
		t.Fatal(err)
	}
	updated := boardRecord("spec_review", "review-option", base.Add(2*time.Minute))
	if err := engine.ObserveBoard(ctx, updated); err != nil {
		t.Fatalf("new specification review was rejected: %v", err)
	}
}

func TestBoardProjectionOwnerResetCancelsPendingFactoryMove(t *testing.T) {
	ctx := context.Background()
	engine := Engine{Store: &MemoryStore{}}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	building := boardRecord("building", "building-option", base)
	if err := engine.ObserveBoard(ctx, building); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginBoardMove(ctx, building, "verification", "verification-option"); err != nil {
		t.Fatal(err)
	}
	discovery := boardRecord("discovery", "discovery-option", base.Add(time.Minute))
	if err := engine.ObserveBoard(ctx, discovery); err != nil {
		t.Fatalf("newer owner reset did not cancel pending move: %v", err)
	}
	if _, pending, err := engine.PendingBoardMove(ctx, building.IssueID); err != nil || pending {
		t.Fatalf("stale factory move survived owner reset: %t, %v", pending, err)
	}
}

func TestCancelBoardMoveRequiresExactUnchangedProjectRevision(t *testing.T) {
	ctx := context.Background()
	engine := Engine{Store: &MemoryStore{}}
	base := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	building := boardRecord("building", "building-option", base)
	if err := engine.ObserveBoard(ctx, building); err != nil {
		t.Fatal(err)
	}
	if err := engine.BeginBoardMove(ctx, building, "verification", "verification-option"); err != nil {
		t.Fatal(err)
	}
	changed := building
	changed.UpdatedAt = base.Add(time.Minute)
	if err := engine.CancelBoardMove(ctx, changed, "review"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed Project revision cancelled move: %v", err)
	}
	if err := engine.CancelBoardMove(ctx, building, "verification"); !errors.Is(err, ErrConflict) {
		t.Fatalf("still-justified target cancelled move: %v", err)
	}
	if err := engine.CancelBoardMove(ctx, building, "building"); err != nil {
		t.Fatalf("obsolete intent not cancelled: %v", err)
	}
	if _, pending, err := engine.PendingBoardMove(ctx, building.IssueID); err != nil || pending {
		t.Fatalf("cancelled move retained an intent: pending=%t err=%v", pending, err)
	}
	if err := engine.BeginBoardMove(ctx, building, "verification", "verification-option"); err != nil {
		t.Fatalf("new decision could not reserve a move: %v", err)
	}
}
