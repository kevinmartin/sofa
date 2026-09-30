package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/github"
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
		Branch: "sofa/task", ExpectedHead: base.Grant.BaseSHA, CandidateDigest: strings.Repeat("d", 64),
		HeadSHA: strings.Repeat("e", 40), PRNumber: 7, PRURL: "https://github.com/kevinmartin/sofa-disposable/pull/7",
	}
	m := repairManifest{
		Version: 1,
		Admission: state.Admission{
			Repository: c.Repository, Issue: int64(base.Grant.IssueNumber), SpecDigest: base.Grant.SpecDigest,
			ConfigDigest: digest, BaseSHA: base.Grant.BaseSHA, ProjectID: c.ProjectID,
			ProjectItemID: base.Grant.ProjectItemID, StatusOptionID: base.Grant.StatusOptionID, StatusUpdatedAt: base.Grant.StatusUpdatedAt,
		},
		Publication: pub,
		Repair: state.RepairIntent{
			FeedbackID: "review-7-9-abcd", FeedbackHash: hex.EncodeToString(h[:]),
			PRBaseSHA: strings.Repeat("f", 40), PRHeadSHA: pub.HeadSHA, PRNumber: pub.PRNumber,
		},
		ReviewID: 9, CanonicalSpec: base.CanonicalSpec, FeedbackText: feedback,
	}
	m.Fence = state.Fence{AttemptID: state.AttemptID(m.Admission), Generation: 2, Owner: state.Owner{RunID: "123", RunAttempt: 1}}
	pull := github.PullSnapshot{
		Number: 7, URL: pub.PRURL, State: "open", HeadSHA: pub.HeadSHA, HeadRef: pub.Branch,
		HeadRepository: c.Repository, BaseSHA: m.Repair.PRBaseSHA, BaseRepository: c.Repository,
	}
	reviews := []github.PullReview{{
		ID: 9, UserID: c.OwnerID, State: "CHANGES_REQUESTED", CommitSHA: pub.HeadSHA,
		Body: feedback, SubmittedAt: time.Now().UTC(),
	}}
	return m, pull, reviews
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

func TestRepairSourceRequiresSameApprovedSpecAndDeliveryPosition(t *testing.T) {
	c, _ := testManifest(t)
	m, _, _ := repairFixture(t)
	s := admission.Snapshot{
		Repository: c.Repository, RepositoryID: c.RepositoryID, Number: int(m.Admission.Issue),
		Title: "Fixture", Body: "Change the fixture", Open: true,
		ProjectID: c.ProjectID, ProjectPrivate: true, ProjectItemID: m.Admission.ProjectItemID,
		CurrentStatus: c.Lifecycle.Statuses["review"], Complete: true,
	}
	a := state.Attempt{Admission: m.Admission}
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
