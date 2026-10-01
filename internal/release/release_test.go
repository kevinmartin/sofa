package release

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

type fixtureRevertVerifier struct{ verified bool }

func (f fixtureRevertVerifier) VerifiedRevert(_ context.Context, _, _, _, _ string) (bool, error) {
	return f.verified, nil
}

func TestEvaluateMergeAndReleaseExactCommit(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const merge = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const defaultHead = "cccccccccccccccccccccccccccccccccccccccc"
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	attempt := state.Attempt{
		ID: "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		Admission: state.Admission{
			Repository: "owner/repo",
		},
		Publication: &state.Publication{
			Branch:   "sofa/task",
			HeadSHA:  head,
			PRNumber: 7,
			PRURL:    "https://github.com/owner/repo/pull/7",
		},
	}
	pull := github.PullSnapshot{
		Number:         7,
		URL:            attempt.Publication.PRURL,
		State:          "closed",
		Merged:         true,
		MergeCommitSHA: merge,
		HeadSHA:        head,
		HeadRef:        "sofa/task",
		HeadRepository: "owner/repo",
		BaseRepository: "owner/repo",
	}
	required := []RequiredCheck{{
		Name:  "release / smoke",
		AppID: 77,
	}}
	pass := github.CommitCheck{
		Name:      "release / smoke",
		AppID:     77,
		SourceID:  10,
		State:     "success",
		UpdatedAt: now,
	}
	cases := []struct {
		name      string
		onDefault bool
		checks    []github.CommitCheck
		want      string
	}{
		{"merge not on default", false, []github.CommitCheck{pass}, "release-blocked"},
		{"missing check", true, nil, "release-blocked"},
		{"wrong app", true, []github.CommitCheck{{
			Name:      pass.Name,
			AppID:     88,
			SourceID:  11,
			State:     "success",
			UpdatedAt: now,
		}}, "release-blocked"},
		{"pending", true, []github.CommitCheck{{
			Name:      pass.Name,
			AppID:     77,
			SourceID:  11,
			State:     "in_progress",
			UpdatedAt: now,
		}}, "release-blocked"},
		{"failed", true, []github.CommitCheck{{
			Name:      pass.Name,
			AppID:     77,
			SourceID:  11,
			State:     "failure",
			UpdatedAt: now,
		}}, "release-failed"},
		{"passed", true, []github.CommitCheck{pass}, "done"},
		{"latest failure overrides old pass", true, []github.CommitCheck{pass, {
			Name:      pass.Name,
			AppID:     77,
			SourceID:  11,
			State:     "failure",
			UpdatedAt: now.Add(time.Minute),
		}}, "release-failed"},
		{"latest success corrects failure", true, []github.CommitCheck{{
			Name:      pass.Name,
			AppID:     77,
			SourceID:  9,
			State:     "failure",
			UpdatedAt: now.Add(-time.Minute),
		}, pass}, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Evaluate(attempt, pull, defaultHead, tc.onDefault, required, tc.checks)
			if err != nil || d.Kind != tc.want || d.CommitSHA != merge || d.EventID == "" {
				t.Fatalf("got %+v, %v; want %s", d, err, tc.want)
			}
		})
	}
	baseline, err := Evaluate(attempt, pull, defaultHead, true, required, []github.CommitCheck{pass})
	if err != nil {
		t.Fatal(err)
	}
	withUnrelated, err := Evaluate(attempt, pull, defaultHead, true, required, []github.CommitCheck{pass, {
		Name:      "optional / advisory",
		AppID:     88,
		SourceID:  12,
		State:     "failure",
		UpdatedAt: now.Add(time.Minute),
	}})
	if err != nil || withUnrelated.Kind != baseline.Kind || withUnrelated.EventID != baseline.EventID {
		t.Fatalf("unrelated check changed release event: baseline=%+v unrelated=%+v err=%v", baseline, withUnrelated, err)
	}
	updatedRequired := pass
	updatedRequired.UpdatedAt = now.Add(time.Minute)
	withUpdatedRequired, err := Evaluate(attempt, pull, defaultHead, true, required, []github.CommitCheck{updatedRequired})
	if err != nil || withUpdatedRequired.Kind != baseline.Kind || withUpdatedRequired.EventID == baseline.EventID {
		t.Fatalf("required check revision did not change release event: baseline=%+v updated=%+v err=%v", baseline, withUpdatedRequired, err)
	}
	closed := pull
	closed.Merged = false
	closed.MergeCommitSHA = ""
	d, err := Evaluate(attempt, closed, defaultHead, false, required, nil)
	if err != nil || d.Kind != "closed-unmerged" || d.CommitSHA != head {
		t.Fatalf("closed-unmerged outcome: %+v, %v", d, err)
	}
	stale := pull
	stale.HeadSHA = defaultHead
	if _, err := Evaluate(attempt, stale, defaultHead, true, required, []github.CommitCheck{pass}); err == nil {
		t.Fatal("different PR head accepted release result")
	}
}

func TestRecordAppendsLaterCorrectionWithoutRewritingOutcome(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{
		Store: &state.MemoryStore{},
	}
	attempt, _, err := engine.Admit(ctx, state.Admission{
		Repository:      "owner/repo",
		Issue:           1,
		SpecDigest:      strings.Repeat("a", 64),
		ConfigDigest:    strings.Repeat("b", 64),
		BaseSHA:         strings.Repeat("c", 40),
		ProjectID:       "project",
		ProjectItemID:   "item",
		StatusOptionID:  "ready",
		StatusUpdatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}, state.Limits{
		RuntimeSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := Decision{
		AttemptID:   attempt.ID,
		Kind:        "release-failed",
		CommitSHA:   strings.Repeat("d", 40),
		EvidenceRef: "https://github.com/owner/repo/pull/7",
		EventID:     "release-failed-7",
	}
	if err := Record(ctx, engine, failed, time.Date(2026, 9, 29, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, engine, failed, time.Date(2026, 9, 29, 14, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("duplicate observation rewrote original: %v", err)
	}
	corrected := failed
	corrected.Kind = "done"
	corrected.EventID = "release-done-7"
	if err := Record(ctx, engine, corrected, time.Date(2026, 9, 29, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil || len(snapshot.State.Observations) != 2 || snapshot.State.Observations[0].Outcome != "release-failed" || snapshot.State.Observations[1].Outcome != "done" {
		t.Fatalf("release correction history lost: %+v, %v", snapshot.State.Observations, err)
	}
}

func TestRevertCorrectionRequiresVerifiedExactInverse(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{
		Store: &state.MemoryStore{},
	}
	a, _, err := engine.Admit(ctx, state.Admission{
		Repository:      "owner/repo",
		Issue:           1,
		SpecDigest:      strings.Repeat("a", 64),
		ConfigDigest:    strings.Repeat("b", 64),
		BaseSHA:         strings.Repeat("c", 40),
		ProjectID:       "project",
		ProjectItemID:   "item",
		StatusOptionID:  "ready",
		StatusUpdatedAt: time.Now().UTC(),
	}, state.Limits{
		RuntimeSeconds: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	fence, err := engine.Claim(ctx, a.ID, state.Owner{
		RunID:      "1",
		RunAttempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Advance(ctx, fence, state.Validating); err != nil {
		t.Fatal(err)
	}
	p := state.Publication{
		Branch:          "sofa/task",
		ExpectedHead:    strings.Repeat("c", 40),
		CandidateDigest: strings.Repeat("d", 64),
	}
	if err := engine.BeginPublication(ctx, fence, p); err != nil {
		t.Fatal(err)
	}
	p.HeadSHA = strings.Repeat("e", 40)
	p.PRNumber = 7
	p.PRURL = "https://github.com/owner/repo/pull/7"
	if err := engine.MarkPublished(ctx, fence, p); err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a = snapshot.State.Attempts[a.ID]
	pull := github.PullSnapshot{
		Number:         7,
		URL:            p.PRURL,
		Merged:         true,
		HeadSHA:        p.HeadSHA,
		MergeCommitSHA: strings.Repeat("f", 40),
	}
	revert := strings.Repeat("1", 40)
	if err := ObserveRevert(ctx, fixtureRevertVerifier{false}, engine, a, pull, revert, revert, time.Now().UTC()); !errors.Is(err, ErrUnverifiedRevert) {
		t.Fatal("unverified inverse recorded as revert")
	}
	if err := ObserveRevert(ctx, fixtureRevertVerifier{true}, engine, a, pull, revert, revert, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := ObserveRevert(ctx, fixtureRevertVerifier{true}, engine, a, pull, revert, revert, time.Now().UTC()); err != nil {
		t.Fatalf("revert redelivery failed: %v", err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil || len(snapshot.State.Observations) != 1 || snapshot.State.Observations[0].Outcome != "reverted" {
		t.Fatalf("revert correction missing: %+v, %v", snapshot.State.Observations, err)
	}
}

func TestDesignatedCanaryCannotInventMergeOrGate(t *testing.T) {
	const merge = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pull := github.PullSnapshot{
		Number:         178,
		URL:            "https://github.com/owner/repo/pull/178",
		State:          "closed",
		Merged:         true,
		MergeCommitSHA: merge,
		HeadSHA:        head,
		HeadRepository: "owner/repo",
		BaseRepository: "owner/repo",
	}
	required := []RequiredCheck{{
		Name:  "release / smoke",
		AppID: 77,
	}}
	if _, err := EvaluateDesignated("owner/repo", pull, head, merge, true, required, []github.CommitCheck{{
		Name:      required[0].Name,
		AppID:     77,
		SourceID:  1,
		State:     "success",
		UpdatedAt: time.Now(),
	}}); err == nil {
		t.Fatal("accepted a fabricated merge SHA")
	}
	d, err := EvaluateDesignated("owner/repo", pull, merge, merge, true, required, nil)
	if err != nil || d.Kind != "release-blocked" {
		t.Fatalf("missing gate marked complete: %+v, %v", d, err)
	}
	pull.Merged = false
	pull.MergeCommitSHA = ""
	d, err = EvaluateDesignated("owner/repo", pull, "", merge, false, required, nil)
	if err != nil || d.Kind != "closed-unmerged" {
		t.Fatalf("closed PR mislabeled: %+v, %v", d, err)
	}
}
