package review

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

func TestEvaluateOwnerFeedbackRequiresExactPublishedPR(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const base = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	attempt := state.Attempt{
		ID: "attempt-1",
		Admission: state.Admission{
			Repository: "owner/repo",
		},
		Phase: state.Draft,
		Limits: state.Limits{
			Repairs: 2,
		},
		Publication: &state.Publication{
			Branch:   "sofa/attempt-1",
			HeadSHA:  head,
			PRNumber: 4,
			PRURL:    "https://github.com/owner/repo/pull/4",
		},
	}
	pull := github.PullSnapshot{
		Number:         4,
		URL:            attempt.Publication.PRURL,
		State:          "open",
		HeadSHA:        head,
		HeadRef:        attempt.Publication.Branch,
		HeadRepository: "owner/repo",
		BaseSHA:        base,
		BaseRepository: "owner/repo",
	}
	feedback := github.PullReview{
		ID:          8,
		UserID:      "U_owner",
		State:       "CHANGES_REQUESTED",
		CommitSHA:   head,
		Body:        "Please fix the boundary case.",
		SubmittedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
	decision, err := Evaluate(attempt, pull, feedback, "U_owner")
	if err != nil || decision.PRHeadSHA != head || decision.PRBaseSHA != base || decision.PRNumber != 4 || decision.FeedbackHash == "" || !strings.Contains(decision.FeedbackRef, "pullrequestreview-8") {
		t.Fatalf("valid feedback was not bound to the exact PR: %+v, %v", decision, err)
	}
	cases := []struct {
		name   string
		change func(*state.Attempt, *github.PullSnapshot, *github.PullReview)
	}{
		{"bot review", func(_ *state.Attempt, _ *github.PullSnapshot, r *github.PullReview) { r.UserID = "U_bot" }},
		{"old commit", func(_ *state.Attempt, _ *github.PullSnapshot, r *github.PullReview) { r.CommitSHA = base }},
		{"comment only", func(_ *state.Attempt, _ *github.PullSnapshot, r *github.PullReview) { r.State = "COMMENTED" }},
		{"review dismissed", func(_ *state.Attempt, _ *github.PullSnapshot, r *github.PullReview) { r.State = "DISMISSED" }},
		{"head changed", func(_ *state.Attempt, p *github.PullSnapshot, _ *github.PullReview) { p.HeadSHA = base }},
		{"other PR", func(_ *state.Attempt, p *github.PullSnapshot, _ *github.PullReview) { p.Number = 5 }},
		{"fork", func(_ *state.Attempt, p *github.PullSnapshot, _ *github.PullReview) {
			p.HeadRepository = "attacker/repo"
		}},
		{"closed", func(_ *state.Attempt, p *github.PullSnapshot, _ *github.PullReview) { p.State = "closed" }},
		{"out of budget", func(a *state.Attempt, _ *github.PullSnapshot, _ *github.PullReview) {
			a.Counts.Repairs = a.Limits.Repairs
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, p, r := attempt, pull, feedback
			tc.change(&a, &p, &r)
			if _, err := Evaluate(a, p, r, "U_owner"); err == nil {
				t.Fatal("unauthorized or stale review admitted repair")
			}
		})
	}
}

func TestEvaluateReservedRejectsNewerFeedbackAndChangedIdentity(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const base = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	a := state.Attempt{
		ID: "attempt-1", Admission: state.Admission{Repository: "owner/repo"},
		Phase: state.Pending, Limits: state.Limits{Repairs: 1}, Counts: state.Counters{Repairs: 1},
		Publication: &state.Publication{Branch: "sofa/attempt-1", HeadSHA: head, PRNumber: 4, PRURL: "https://github.com/owner/repo/pull/4"},
	}
	p := github.PullSnapshot{Number: 4, URL: a.Publication.PRURL, State: "open", HeadSHA: head, HeadRef: a.Publication.Branch, HeadRepository: "owner/repo", BaseSHA: base, BaseRepository: "owner/repo"}
	r := github.PullReview{ID: 8, UserID: "U_owner", State: "CHANGES_REQUESTED", CommitSHA: head, Body: "Fix the boundary case", SubmittedAt: time.Now().UTC()}
	a.Phase = state.Draft
	a.Counts.Repairs = 0
	d, err := Evaluate(a, p, r, "U_owner")
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = state.Pending
	a.Counts.Repairs = 1
	a.Repair = &state.RepairIntent{FeedbackID: d.FeedbackID, FeedbackHash: d.FeedbackHash, PRBaseSHA: base, PRHeadSHA: head, PRNumber: 4}
	if got, err := EvaluateReserved(a, p, r, "U_owner"); err != nil || got.FeedbackID != d.FeedbackID {
		t.Fatalf("reserved feedback was not recovered: %+v, %v", got, err)
	}
	newer := r
	newer.ID = 9
	newer.SubmittedAt = newer.SubmittedAt.Add(time.Minute)
	if _, err := EvaluateReserved(a, p, newer, "U_owner"); err == nil {
		t.Fatal("newer review replaced reserved feedback")
	}
	changed := p
	changed.HeadSHA = base
	if _, err := EvaluateReserved(a, changed, r, "U_owner"); err == nil {
		t.Fatal("changed head inherited review reservation")
	}
	changed = p
	changed.BaseSHA = head
	if _, err := EvaluateReserved(a, changed, r, "U_owner"); err == nil {
		t.Fatal("changed base inherited review reservation")
	}
	r.State = "DISMISSED"
	if _, err := EvaluateReserved(a, p, r, "U_owner"); err == nil {
		t.Fatal("dismissed review inherited reservation")
	}
}

func TestLaterFeedbackAppendsOnlyLinkedMetadata(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{Store: &state.MemoryStore{}}
	a, _, err := engine.Admit(ctx, state.Admission{
		Repository: "owner/repo", Issue: 1,
		SpecDigest: strings.Repeat("a", 64), ConfigDigest: strings.Repeat("b", 64), BaseSHA: strings.Repeat("c", 40),
		ProjectID: "project", ProjectItemID: "item", StatusOptionID: "ready", StatusUpdatedAt: time.Now().UTC(),
	}, state.Limits{RuntimeSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = state.Draft
	a.Publication = &state.Publication{
		Branch: "sofa/task", ExpectedHead: strings.Repeat("c", 40), CandidateDigest: strings.Repeat("d", 64),
		HeadSHA: strings.Repeat("e", 40), PRNumber: 7, PRURL: "https://github.com/owner/repo/pull/7",
	}
	snapshot.State.Attempts[a.ID] = a
	if err := engine.Store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	mergedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	pull := github.PullSnapshot{
		Number: 7, URL: a.Publication.PRURL, HeadSHA: a.Publication.HeadSHA, HeadRef: a.Publication.Branch, Merged: true, MergedAt: mergedAt,
		MergeCommitSHA: strings.Repeat("f", 40), HeadRepository: "owner/repo", BaseRepository: "owner/repo",
	}
	comment := discovery.SpecComment{
		ID: 9, AuthorID: "U_reporter", Body: "Potential regression in private data: should not appear in ledger",
		CreatedAt: mergedAt.Add(time.Hour), UpdatedAt: mergedAt.Add(time.Hour),
	}
	if err := RecordLaterFeedback(ctx, engine, a, pull, comment); err != nil {
		t.Fatal(err)
	}
	if err := RecordLaterFeedback(ctx, engine, a, pull, comment); err != nil {
		t.Fatalf("duplicate feedback changed ledger: %v", err)
	}
	changedPull := pull
	changedPull.HeadSHA = strings.Repeat("1", 40)
	if err := RecordLaterFeedback(ctx, engine, a, changedPull, comment); err == nil {
		t.Fatal("later feedback accepted a different PR head")
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil || len(snapshot.State.Observations) != 1 || strings.Contains(snapshot.State.Observations[0].EvidenceRef, comment.Body) || snapshot.State.Observations[0].Revision != pull.MergeCommitSHA {
		t.Fatalf("feedback record leaked content or lost merge binding: %+v, %v", snapshot.State.Observations, err)
	}
	comment.CreatedAt = mergedAt.Add(-time.Minute)
	if err := RecordLaterFeedback(ctx, engine, a, pull, comment); err == nil {
		t.Fatal("pre-merge comment treated as later feedback")
	}
}
