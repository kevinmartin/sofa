package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/review"
	"github.com/kevinmartin/sofa/internal/state"
)

func repairFixture(t *testing.T) (repairManifest, github.PullSnapshot, []github.PullReview) {
	t.Helper()
	c, base := testManifest(t)
	digest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	feedback := "Please handle a bounded edge case"
	h := sha256.Sum256([]byte(feedback))
	pub := state.Publication{
		Branch:          "sofa/task",
		ExpectedHead:    base.Grant.BaseSHA,
		CandidateDigest: strings.Repeat("d", 64),
		HeadSHA:         strings.Repeat("e", 40),
		PRNumber:        7,
		PRURL:           "https://github.com/kevinmartin/sofa-disposable/pull/7",
	}
	m := repairManifest{
		Version: 1,
		Admission: state.Admission{
			Repository:      c.Repository,
			Issue:           int64(base.Grant.IssueNumber),
			SpecDigest:      base.Grant.SpecDigest,
			ConfigDigest:    digest,
			BaseSHA:         base.Grant.BaseSHA,
			ProjectID:       c.ProjectID,
			ProjectItemID:   base.Grant.ProjectItemID,
			StatusOptionID:  base.Grant.StatusOptionID,
			StatusUpdatedAt: base.Grant.StatusUpdatedAt,
		},
		Publication: pub,
		Repair: state.RepairIntent{
			FeedbackID:   fmt.Sprintf("review-7-9-%s", hex.EncodeToString(h[:8])),
			FeedbackHash: hex.EncodeToString(h[:]),
			PRBaseSHA:    strings.Repeat("f", 40),
			PRHeadSHA:    pub.HeadSHA,
			PRNumber:     pub.PRNumber,
		},
		ReviewID:      9,
		CanonicalSpec: base.CanonicalSpec,
		FeedbackText:  feedback,
	}
	m.Fence = state.Fence{
		AttemptID:  state.AttemptID(m.Admission),
		Generation: 2,
		Owner: state.Owner{
			RunID:      "123",
			RunAttempt: 1,
		},
	}
	pull := github.PullSnapshot{
		Number:         7,
		URL:            pub.PRURL,
		State:          "open",
		HeadSHA:        pub.HeadSHA,
		HeadRef:        pub.Branch,
		HeadRepository: c.Repository,
		BaseSHA:        m.Repair.PRBaseSHA,
		BaseRepository: c.Repository,
	}
	reviews := []github.PullReview{{
		ID:          9,
		UserID:      c.OwnerID,
		State:       "CHANGES_REQUESTED",
		CommitSHA:   pub.HeadSHA,
		Body:        feedback,
		SubmittedAt: time.Now().UTC(),
	}}
	return m, pull, reviews
}

type repairProofReader struct {
	proof state.RunProof
	err   error
}

func (r repairProofReader) RunProof(_ context.Context, _ string, _ state.Owner) (state.RunProof, error) {
	return r.proof, r.err
}

func TestReservedRepairAdmissionKeepsOriginalReviewAndTerminalFence(t *testing.T) {
	ctx := context.Background()
	m, pull, reviews := repairFixture(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	engine := state.Engine{
		Store: &state.MemoryStore{},
		Now:   func() time.Time { return now },
	}
	a, _, err := engine.Admit(ctx, m.Admission, state.Limits{
		ModelCalls:            2,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = state.Draft
	a.Publication = &m.Publication
	snapshot.State.Attempts[a.ID] = a
	if err := engine.Store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	if ok, err := engine.ReserveReviewRepair(ctx, a.ID, m.Repair); err != nil || !ok {
		t.Fatalf("reserve: %v, %v", ok, err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pending := snapshot.State.Attempts[a.ID]
	if got, err := recoverReservedReviewRepair(ctx, engine, repairProofReader{
		err: errors.New("proof should not be read"),
	}, m.Admission.Repository, pending); err != nil || got.Phase != state.Pending || got.Counts.Repairs != 1 || got.Counts.ModelCalls != 0 {
		t.Fatalf("reservation-only crash was not recoverable without a run proof: %+v, %v", got, err)
	}
	firstOwner := state.Owner{
		RunID:      "77",
		RunAttempt: 1,
	}
	if _, err := engine.ClaimReviewRepair(ctx, a.ID, firstOwner, state.Counters{
		ModelCalls:     1,
		RuntimeSeconds: 600,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a = snapshot.State.Attempts[a.ID]
	newer := reviews[0]
	newer.ID = 10
	newer.SubmittedAt = newer.SubmittedAt.Add(time.Minute)
	if _, err := review.EvaluateReserved(a, pull, reviews[0], reviews[0].UserID); err != nil {
		t.Fatalf("reserved fixture feedback rejected: %v; attempt=%+v pull=%+v", err, a, pull)
	}
	selected, selectedReview := selectRepairReview(a, pull, []github.PullReview{reviews[0], newer}, reviews[0].UserID)
	if selected.FeedbackID != "" || selectedReview.ID != 0 {
		t.Fatalf("superseded review borrowed reservation: %+v, %+v", selected, selectedReview)
	}
	if _, err := recoverReservedReviewRepair(ctx, engine, repairProofReader{
		err: errors.New("transient API error"),
	}, m.Admission.Repository, a); err == nil {
		t.Fatal("API failure released owner")
	}
	active := state.RunProof{
		Owner:      firstOwner,
		Status:     "in_progress",
		ObservedAt: now,
	}
	if _, err := recoverReservedReviewRepair(ctx, engine, repairProofReader{
		proof: active,
	}, m.Admission.Repository, a); !errors.Is(err, state.ErrActive) {
		t.Fatalf("active owner released: %v", err)
	}
	terminal := state.RunProof{
		Owner:      firstOwner,
		Status:     "completed",
		Conclusion: "cancelled",
		ObservedAt: now,
	}
	recovered, err := recoverReservedReviewRepair(ctx, engine, repairProofReader{
		proof: terminal,
	}, m.Admission.Repository, a)
	if err != nil || recovered.Repair == nil || *recovered.Repair != m.Repair || recovered.Publication == nil || *recovered.Publication != m.Publication || recovered.Owner != nil || recovered.Phase != state.Pending || recovered.Counts.Repairs != 1 || recovered.Counts.ModelCalls != 1 || recovered.Counts.InfrastructureRetries != 1 {
		t.Fatalf("reserved repair not safely recovered: %+v, %v", recovered, err)
	}
	if selected, _ := selectRepairReview(recovered, pull, []github.PullReview{newer}, reviews[0].UserID); selected.FeedbackID != "" {
		t.Fatal("new review authorized recovered repair")
	}
}

type repairCommentReader struct{ comment discovery.SpecComment }

func (r repairCommentReader) IssueComment(_ context.Context, _ string, _, _ int64) (discovery.SpecComment, error) {
	return r.comment, nil
}

func TestRepairReadsExactApprovedCommentInsteadOfIssueIdea(t *testing.T) {
	c, m := testManifest(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	issue := admission.Snapshot{
		Repository:        c.Repository,
		RepositoryID:      c.RepositoryID,
		IssueID:           m.Grant.IssueID,
		Number:            m.Grant.IssueNumber,
		Title:             "Fixture",
		Body:              "Investigate the fixture before specifying the change.",
		Open:              true,
		ProjectID:         c.ProjectID,
		ProjectPrivate:    true,
		ProjectItemID:     m.Grant.ProjectItemID,
		CurrentStatus:     c.Lifecycle.Statuses["review"],
		StatusOptionID:    "review-option",
		StatusUpdatedAt:   now.Add(4 * time.Minute),
		IssueLastEditedAt: now,
		Complete:          true,
	}
	proposal := discovery.Specification{
		Version:        discovery.SpecificationVersion,
		Problem:        "Fixture behavior is incomplete.",
		Evidence:       "The current fixture reproduces the missing behavior.",
		Goals:          "Complete the fixture behavior.",
		NonGoals:       "No unrelated changes.",
		Constraints:    "Keep the existing API.",
		Dependencies:   "None.",
		Acceptance:     "Given the fixture input, the expected output is produced.",
		Validation:     "Run the fixture and Go tests.",
		Risks:          "Check existing callers.",
		Questions:      "None.",
		DeliverySlices: "One bounded change.",
	}
	body, err := proposal.Render()
	if err != nil {
		t.Fatal(err)
	}
	comment := discovery.SpecComment{
		ID:        42,
		AuthorID:  "BOT_node",
		Body:      body,
		CreatedAt: now.Add(time.Minute),
		UpdatedAt: now.Add(time.Minute),
	}
	_, sourceDigest, err := admission.CanonicalSpec(issue.Title, issue.Body)
	if err != nil {
		t.Fatal(err)
	}
	canonical, specDigest, err := admission.CanonicalSpec(issue.Title, comment.Body)
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	attempt := state.Attempt{
		Admission: state.Admission{
			Repository:    c.Repository,
			Issue:         int64(issue.Number),
			SpecDigest:    specDigest,
			ConfigDigest:  configDigest,
			ProjectItemID: issue.ProjectItemID,
		},
	}
	if _, err := validateRepairSource(c, issue, attempt); err == nil {
		t.Fatal("raw issue idea incorrectly stood in for the approved specification")
	}
	ledger := state.Empty()
	ledger.Specs[issue.IssueID] = state.SpecRecord{
		Repository:       c.Repository,
		IssueID:          issue.IssueID,
		Issue:            int64(issue.Number),
		ProjectID:        c.ProjectID,
		ProjectItemID:    issue.ProjectItemID,
		SourceDigest:     sourceDigest,
		SpecDigest:       specDigest,
		IssueEditedAt:    now,
		CommentID:        comment.ID,
		CommentAuthorID:  comment.AuthorID,
		CommentCreatedAt: comment.CreatedAt,
		CommentUpdatedAt: comment.UpdatedAt,
		ReviewOptionID:   "spec-review-option",
		ReviewUpdatedAt:  now.Add(2 * time.Minute),
		ApprovedDigest:   specDigest,
		BacklogOptionID:  "backlog-option",
		BacklogUpdatedAt: now.Add(3 * time.Minute),
	}
	store := &state.MemoryStore{}
	if err := store.CompareAndSwap(context.Background(), "", ledger); err != nil {
		t.Fatal(err)
	}
	reader := repairCommentReader{
		comment: comment,
	}
	got, err := approvedRepairSource(context.Background(), c, reader, store, issue, attempt)
	if err != nil || string(got) != string(canonical) {
		t.Fatalf("exact approved comment was not used: %v", err)
	}
	reader.comment.Body = strings.Replace(reader.comment.Body, "expected output", "different output", 1)
	if _, err := approvedRepairSource(context.Background(), c, reader, store, issue, attempt); err == nil {
		t.Fatal("changed acceptance criteria inherited approval")
	}
	reader.comment = comment
	issue.Body = "A different source idea"
	if _, err := approvedRepairSource(context.Background(), c, reader, store, issue, attempt); err == nil {
		t.Fatal("changed issue idea inherited approval")
	}
}

func TestRepairManifestAndCurrentFeedbackBinding(t *testing.T) {
	c, _ := testManifest(t)
	m, pull, reviews := repairFixture(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeJSON(path, m); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepairManifest(path, c); err != nil {
		t.Fatalf("valid repair manifest denied: %v", err)
	}
	if err := revalidateRepairReview(c, m, pull, reviews, ""); err != nil {
		t.Fatalf("current review denied: %v", err)
	}
	changed := m
	changed.FeedbackText = "different instruction"
	if err := writeJSON(path, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := readRepairManifest(path, c); err == nil {
		t.Fatal("modified feedback inherited reservation")
	}
	changes := []struct {
		name   string
		mutate func(*github.PullSnapshot, *[]github.PullReview)
	}{
		{"changed head", func(p *github.PullSnapshot, _ *[]github.PullReview) { p.HeadSHA = strings.Repeat("1", 40) }},
		{"changed base", func(p *github.PullSnapshot, _ *[]github.PullReview) { p.BaseSHA = strings.Repeat("2", 40) }},
		{"other PR", func(p *github.PullSnapshot, _ *[]github.PullReview) { p.Number = 8 }},
		{"fork", func(p *github.PullSnapshot, _ *[]github.PullReview) { p.HeadRepository = "attacker/repo" }},
		{"other actor", func(_ *github.PullSnapshot, r *[]github.PullReview) { (*r)[0].UserID = "U_other" }},
		{"dismissed review", func(_ *github.PullSnapshot, r *[]github.PullReview) { (*r)[0].State = "DISMISSED" }},
		{"edited review", func(_ *github.PullSnapshot, r *[]github.PullReview) { (*r)[0].Body = "new scope" }},
		{"later approval", func(_ *github.PullSnapshot, r *[]github.PullReview) {
			approved := (*r)[0]
			approved.ID++
			approved.State = "APPROVED"
			approved.SubmittedAt = approved.SubmittedAt.Add(time.Minute)
			*r = append(*r, approved)
		}},
	}
	for _, tc := range changes {
		t.Run(tc.name, func(t *testing.T) {
			p := pull
			r := append([]github.PullReview(nil), reviews...)
			tc.mutate(&p, &r)
			if err := revalidateRepairReview(c, m, p, r, ""); err == nil {
				t.Fatal("changed PR or feedback accepted")
			}
		})
	}
}

func TestLaterOwnerReviewSupersedesRepairAdmission(t *testing.T) {
	c, _ := testManifest(t)
	m, pull, reviews := repairFixture(t)
	attempt := state.Attempt{
		ID:        m.Fence.AttemptID,
		Admission: m.Admission,
		Phase:     state.Draft,
		Limits: state.Limits{
			Repairs: 1,
		},
		Publication: &m.Publication,
	}
	if selected, _ := selectRepairReview(attempt, pull, reviews, reviews[0].UserID); selected.FeedbackID == "" {
		t.Fatal("current owner change request was not selected")
	}
	for _, stateName := range []string{"APPROVED", "DISMISSED"} {
		t.Run(stateName, func(t *testing.T) {
			later := reviews[0]
			later.ID++
			later.State = stateName
			later.SubmittedAt = later.SubmittedAt.Add(time.Minute)
			selected, _ := selectRepairReview(attempt, pull, []github.PullReview{reviews[0], later}, reviews[0].UserID)
			if selected.FeedbackID != "" {
				t.Fatal("older change request survived later owner review")
			}
		})
	}
	commented := reviews[0]
	commented.ID++
	commented.State = "COMMENTED"
	commented.SubmittedAt = commented.SubmittedAt.Add(time.Minute)
	withComment := []github.PullReview{reviews[0], commented}
	if selected, _ := selectRepairReview(attempt, pull, withComment, reviews[0].UserID); selected.FeedbackID == "" {
		t.Fatal("informational owner comment withdrew active change request")
	}
	if err := revalidateRepairReview(c, m, pull, withComment, ""); err != nil {
		t.Fatalf("informational owner comment invalidated reserved review: %v", err)
	}
}

func TestRepairInlineFeedbackCannotChangeAfterAdmission(t *testing.T) {
	c, _ := testManifest(t)
	m, pull, reviews := repairFixture(t)
	reviews[0].Comments = []github.PullReviewComment{
		{
			ID:        44,
			ReviewID:  reviews[0].ID,
			UserID:    reviews[0].UserID,
			CommitSHA: reviews[0].CommitSHA,
			Path:      "fixture.go",
			Body:      "Fix the empty input case",
		},
	}
	feedback, err := review.FeedbackText(reviews[0])
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(feedback))
	m.FeedbackText = feedback
	m.Repair.FeedbackHash = hex.EncodeToString(h[:])
	m.Repair.FeedbackID = fmt.Sprintf("review-7-9-%s", hex.EncodeToString(h[:8]))
	if err := revalidateRepairReview(c, m, pull, reviews, ""); err != nil {
		t.Fatalf("exact inline review denied: %v", err)
	}
	reviews[0].Comments[0].Body = "Rewritten finding"
	if err := revalidateRepairReview(c, m, pull, reviews, ""); err == nil {
		t.Fatal("edited inline review inherited repair authority")
	}
}

func TestRepairSourceRequiresSameApprovedSpecAndDeliveryPosition(t *testing.T) {
	c, _ := testManifest(t)
	m, _, _ := repairFixture(t)
	s := admission.Snapshot{
		Repository:     c.Repository,
		RepositoryID:   c.RepositoryID,
		Number:         int(m.Admission.Issue),
		Title:          "Fixture",
		Body:           "Change the fixture",
		Open:           true,
		ProjectID:      c.ProjectID,
		ProjectPrivate: true,
		ProjectItemID:  m.Admission.ProjectItemID,
		CurrentStatus:  c.Lifecycle.Statuses["review"],
		Complete:       true,
	}
	a := state.Attempt{
		Admission: m.Admission,
	}
	if _, err := validateRepairSource(c, s, a); err != nil {
		t.Fatalf("approved source denied: %v", err)
	}
	s.Body = "Expanded scope"
	if _, err := validateRepairSource(c, s, a); err == nil {
		t.Fatal("materially edited specification inherited approval")
	}
	s.Body = "Change the fixture"
	s.CurrentStatus = c.Lifecycle.Statuses["done"]
	if _, err := validateRepairSource(c, s, a); err == nil {
		t.Fatal("completed item admitted a repair")
	}
}
