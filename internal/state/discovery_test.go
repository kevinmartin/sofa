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
	engine := Engine{Store: store, Now: func() time.Time { return now }}
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
	owner := Owner{RunID: "123", RunAttempt: 1}
	fence, err := engine.ClaimDiscovery(ctx, "I_1", owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ClaimDiscovery(ctx, "I_1", owner); !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate claim: %v", err)
	}
	if err := engine.RecoverDiscovery(ctx, "I_1", RunProof{Owner: owner, Status: "in_progress", ObservedAt: now}); !errors.Is(err, ErrActive) {
		t.Fatalf("active run incorrectly reclaimed: %v", err)
	}
	proof := RunProof{Owner: owner, Status: "completed", Conclusion: "cancelled", ObservedAt: now}
	if err := engine.RecoverDiscovery(ctx, "I_1", proof); err != nil {
		t.Fatal(err)
	}
	if err := engine.AssertDiscoveryOwner(ctx, fence); !errors.Is(err, ErrStale) {
		t.Fatalf("stale worker retained ownership: %v", err)
	}
	secondOwner := Owner{RunID: "124", RunAttempt: 1}
	secondFence, err := engine.ClaimDiscovery(ctx, "I_1", secondOwner)
	if err != nil || secondFence.Generation <= fence.Generation {
		t.Fatalf("reclaim: %+v, %v", secondFence, err)
	}
	if err := engine.RecoverDiscovery(ctx, "I_1", RunProof{Owner: secondOwner, Status: "completed", Conclusion: "failure", ObservedAt: now}); err != nil {
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
	engine := Engine{Store: store, Now: func() time.Time { return time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC) }}
	if _, _, err := engine.AdmitDiscovery(ctx, discoveryAdmission("I_1", 1), 2, 1); err != nil {
		t.Fatal(err)
	}
	fence, err := engine.ClaimDiscovery(ctx, "I_1", Owner{RunID: "1", RunAttempt: 1})
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
