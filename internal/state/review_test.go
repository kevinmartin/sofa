package state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReviewRepairReservationAndSamePRPublication(t *testing.T) {
	ctx := context.Background()
	e := engine()
	a := admitted(t, e)
	f := claimed(t, e, a.ID)
	if err := e.Advance(ctx, f, Validating); err != nil {
		t.Fatal(err)
	}
	p := Publication{
		Branch:          "sofa/task",
		ExpectedHead:    a.Admission.BaseSHA,
		CandidateDigest: strings.Repeat("d", 64),
	}
	if err := e.BeginPublication(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	p.HeadSHA = strings.Repeat("e", 40)
	p.PRNumber = 7
	p.PRURL = "https://github.com/owner/consumer/pull/7"
	if err := e.MarkPublished(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	intent := RepairIntent{
		FeedbackID:   "review-7-8-abcd",
		FeedbackHash: strings.Repeat("f", 64),
		PRBaseSHA:    strings.Repeat("b", 40),
		PRHeadSHA:    p.HeadSHA,
		PRNumber:     p.PRNumber,
	}
	if ok, err := e.ReserveReviewRepair(ctx, a.ID, intent); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}
	if ok, err := e.ReserveReviewRepair(ctx, a.ID, intent); err != nil || ok {
		t.Fatalf("replayed reservation consumed budget: %v, %v", ok, err)
	}
	reserved := snapshot(t, e, a.ID)
	if reserved.Phase != Pending || reserved.Counts.Repairs != 1 || reserved.Repair == nil || reserved.Publication == nil || *reserved.Publication != p {
		t.Fatalf("repair did not preserve same PR and budget: %+v", reserved)
	}
	changed := intent
	changed.FeedbackID = "review-7-9-abcd"
	if _, err := e.ReserveReviewRepair(ctx, a.ID, changed); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("other feedback borrowed active reservation: %v", err)
	}
	wrong := intent
	wrong.PRHeadSHA = strings.Repeat("c", 40)
	if _, err := e.ReserveReviewRepair(ctx, a.ID, wrong); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other PR head borrowed reservation: %v", err)
	}
	repairFence, err := e.Claim(ctx, a.ID, Owner{
		RunID:      "43",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Advance(ctx, repairFence, Validating); err != nil {
		t.Fatal(err)
	}
	newDigest := strings.Repeat("1", 64)
	newHead := strings.Repeat("2", 40)
	if err := e.BeginRepairPublication(ctx, repairFence, newDigest, newHead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ClaimReviewRepair(ctx, a.ID, Owner{
		RunID:      "44",
		RunAttempt: 1,
	}, Counters{
		ModelCalls:     1,
		RuntimeSeconds: 600,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("prepared candidate was sent to a new model run: %v", err)
	}
	if err := e.BeginRepairPublication(ctx, repairFence, newDigest, newHead); err != nil {
		t.Fatalf("same candidate intent not idempotent: %v", err)
	}
	if err := e.BeginRepairPublication(ctx, repairFence, newDigest, strings.Repeat("3", 40)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed commit accepted: %v", err)
	}
	newPublication := p
	newPublication.ExpectedHead = p.HeadSHA
	newPublication.CandidateDigest = newDigest
	newPublication.HeadSHA = newHead
	if err := e.MarkReviewRepairPublished(ctx, repairFence, newPublication); err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, e, a.ID)
	if got.Phase != Draft || got.Repair != nil || got.Counts.Repairs != 1 || got.Publication == nil || *got.Publication != newPublication {
		t.Fatalf("same PR was not updated: %+v", got)
	}
	if err := e.MarkReviewRepairPublished(ctx, repairFence, newPublication); err != nil {
		t.Fatalf("same final acknowledgement not idempotent: %v", err)
	}
}

func TestFailedUnpreparedReviewRepairReturnsToDraftUntilBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	e := engine()
	a := admitted(t, e)
	f := claimed(t, e, a.ID)
	if err := e.Advance(ctx, f, Validating); err != nil {
		t.Fatal(err)
	}
	p := Publication{Branch: "sofa/task", ExpectedHead: a.Admission.BaseSHA, CandidateDigest: strings.Repeat("d", 64)}
	if err := e.BeginPublication(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	p.HeadSHA, p.PRNumber, p.PRURL = strings.Repeat("e", 40), 7, "https://github.com/owner/consumer/pull/7"
	if err := e.MarkPublished(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	intent := RepairIntent{FeedbackID: "review-first", FeedbackHash: strings.Repeat("f", 64), PRBaseSHA: strings.Repeat("b", 40), PRHeadSHA: p.HeadSHA, PRNumber: p.PRNumber}
	for turn := 1; turn <= 2; turn++ {
		if turn == 2 {
			intent.FeedbackID = "review-second"
		}
		if reserved, err := e.ReserveReviewRepair(ctx, a.ID, intent); err != nil || !reserved {
			t.Fatalf("reserve turn %d: %v, %v", turn, reserved, err)
		}
		fence, err := e.ClaimReviewRepair(ctx, a.ID, Owner{RunID: "repair-" + intent.FeedbackID, RunAttempt: 1}, Counters{ModelCalls: 1, RuntimeSeconds: 600})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.FailReviewRepair(ctx, fence); err != nil {
			t.Fatal(err)
		}
		got := snapshot(t, e, a.ID)
		wantPhase := Draft
		if turn == 2 {
			wantPhase = Blocked
		}
		if got.Phase != wantPhase || got.Repair != nil || got.Owner != nil || got.Counts.Repairs != int64(turn) || got.Counts.ModelCalls != int64(turn) || got.Publication == nil || *got.Publication != p {
			t.Fatalf("turn %d lost identity or charged counters: %+v", turn, got)
		}
		if _, err := e.ReserveReviewRepair(ctx, a.ID, intent); !errors.Is(err, ErrConflict) {
			t.Fatalf("replayed feedback was not a conflict on turn %d: %v", turn, err)
		}
	}
}

func TestReviewRepairClaimReleasesReservationWhenFullChargeExceedsBudget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits Limits
	}{
		{
			name: "model calls",
			limits: Limits{
				ModelCalls:            1,
				Repairs:               2,
				InfrastructureRetries: 2,
				RuntimeSeconds:        1200,
			},
		},
		{
			name: "runtime",
			limits: Limits{
				ModelCalls:            2,
				Repairs:               2,
				InfrastructureRetries: 2,
				RuntimeSeconds:        600,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			e := engine()
			a, created, err := e.Admit(ctx, admission(), tc.limits)
			if err != nil || !created {
				t.Fatalf("admit: %v, %v", created, err)
			}
			fence := claimed(t, e, a.ID)
			if err := e.Charge(ctx, fence, Counters{ModelCalls: 1, RuntimeSeconds: 600}); err != nil {
				t.Fatal(err)
			}
			if err := e.Advance(ctx, fence, Validating); err != nil {
				t.Fatal(err)
			}
			publication := Publication{
				Branch:          "sofa/task",
				ExpectedHead:    a.Admission.BaseSHA,
				CandidateDigest: strings.Repeat("d", 64),
			}
			if err := e.BeginPublication(ctx, fence, publication); err != nil {
				t.Fatal(err)
			}
			publication.HeadSHA = strings.Repeat("e", 40)
			publication.PRNumber = 7
			publication.PRURL = "https://github.com/owner/consumer/pull/7"
			if err := e.MarkPublished(ctx, fence, publication); err != nil {
				t.Fatal(err)
			}
			intent := RepairIntent{
				FeedbackID:   "review-budget",
				FeedbackHash: strings.Repeat("f", 64),
				PRBaseSHA:    strings.Repeat("b", 40),
				PRHeadSHA:    publication.HeadSHA,
				PRNumber:     publication.PRNumber,
			}
			if reserved, err := e.ReserveReviewRepair(ctx, a.ID, intent); err != nil || !reserved {
				t.Fatalf("reserve: %v, %v", reserved, err)
			}
			if _, err := e.ClaimReviewRepair(ctx, a.ID, Owner{RunID: "repair", RunAttempt: 1}, Counters{ModelCalls: 1, RuntimeSeconds: 600}); !errors.Is(err, ErrLimit) {
				t.Fatalf("exhausted claim: %v", err)
			}
			got := snapshot(t, e, a.ID)
			if got.Phase != Blocked || got.Failure != "budget" || got.Repair != nil || got.Owner != nil || got.Dispatch != "pending" || got.Counts.Repairs != 1 || got.Counts.ModelCalls != 1 || got.Counts.RuntimeSeconds != 600 || got.Publication == nil || *got.Publication != publication {
				t.Fatalf("exhausted reservation still holds WIP or lost identity: %+v", got)
			}
		})
	}
}

func TestReviewRepairClaimAndChargeSurviveTerminalRunRecovery(t *testing.T) {
	ctx := context.Background()
	e := engine()
	a := admitted(t, e)
	f := claimed(t, e, a.ID)
	if err := e.Advance(ctx, f, Validating); err != nil {
		t.Fatal(err)
	}
	p := Publication{
		Branch:          "sofa/task",
		ExpectedHead:    a.Admission.BaseSHA,
		CandidateDigest: strings.Repeat("d", 64),
	}
	if err := e.BeginPublication(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	p.HeadSHA, p.PRNumber, p.PRURL = strings.Repeat("e", 40), 7, "https://github.com/owner/consumer/pull/7"
	if err := e.MarkPublished(ctx, f, p); err != nil {
		t.Fatal(err)
	}
	intent := RepairIntent{
		FeedbackID:   "review-7-8-abcd",
		FeedbackHash: strings.Repeat("f", 64),
		PRBaseSHA:    strings.Repeat("b", 40),
		PRHeadSHA:    p.HeadSHA,
		PRNumber:     p.PRNumber,
	}
	if ok, err := e.ReserveReviewRepair(ctx, a.ID, intent); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}
	first := Owner{
		RunID:      "43",
		RunAttempt: 1,
	}
	charge := Counters{
		ModelCalls:     1,
		RuntimeSeconds: 600,
	}
	firstFence, err := e.ClaimReviewRepair(ctx, a.ID, first, charge)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ClaimReviewRepair(ctx, a.ID, first, charge); !errors.Is(err, ErrClaimed) {
		t.Fatalf("same run charged twice: %v", err)
	}
	if err := e.Recover(ctx, a.ID, RunProof{
		Owner:      first,
		Status:     "in_progress",
		ObservedAt: testNow,
	}); !errors.Is(err, ErrActive) {
		t.Fatalf("active run released: %v", err)
	}
	if err := e.Recover(ctx, a.ID, proof(Owner{
		RunID:      "different",
		RunAttempt: 1,
	})); !errors.Is(err, ErrStale) {
		t.Fatalf("other run released repair: %v", err)
	}
	if err := e.Recover(ctx, a.ID, proof(first)); err != nil {
		t.Fatal(err)
	}
	recovered := snapshot(t, e, a.ID)
	if recovered.Phase != Pending || recovered.Owner != nil || recovered.Repair == nil || *recovered.Repair != intent || recovered.Publication == nil || *recovered.Publication != p || recovered.Counts.Repairs != 1 || recovered.Counts.ModelCalls != 1 || recovered.Counts.RuntimeSeconds != 600 || recovered.Counts.InfrastructureRetries != 1 {
		t.Fatalf("terminal recovery lost durable repair identity or counters: %+v", recovered)
	}
	second := Owner{
		RunID:      "44",
		RunAttempt: 1,
	}
	secondFence, err := e.ClaimReviewRepair(ctx, a.ID, second, charge)
	if err != nil {
		t.Fatal(err)
	}
	if secondFence.Generation <= firstFence.Generation {
		t.Fatal("recovered run did not fence old owner")
	}
	final := snapshot(t, e, a.ID)
	if final.Counts.Repairs != 1 || final.Counts.ModelCalls != 2 || final.Counts.RuntimeSeconds != 1200 || final.Counts.InfrastructureRetries != 1 {
		t.Fatalf("recovery reset or double-charged budget: %+v", final.Counts)
	}
	if err := e.AssertOwner(ctx, firstFence); !errors.Is(err, ErrStale) {
		t.Fatalf("old owner retained authority: %v", err)
	}
}
