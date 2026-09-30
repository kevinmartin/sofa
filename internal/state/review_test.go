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
	repairFence, err := e.Claim(ctx, a.ID, Owner{RunID: "43", RunAttempt: 1})
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
