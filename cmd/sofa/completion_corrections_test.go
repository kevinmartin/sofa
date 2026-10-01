package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/release"
	"github.com/kevinmartin/sofa/internal/state"
)

type completionFixtureReader struct {
	repository string
	defaultSHA string
	mergedSHA  string
	revertSHA  string
	comments   map[int64][]discovery.SpecComment
	commentErr map[int64]error
	commits    []github.DefaultCommit
	verified   bool
}

type pagedCompletionReader struct {
	completionFixtureReader
	pages map[int]github.DefaultCommitPage
	fail  map[int]error
	calls []int
}

func (r *pagedCompletionReader) DefaultCommitComparisonPage(_ context.Context, repository, base, head string, page int) (github.DefaultCommitPage, error) {
	if repository != r.repository || head != r.defaultSHA || base != r.mergedSHA || page < 1 {
		return github.DefaultCommitPage{}, errors.New("other PR comparison")
	}
	r.calls = append(r.calls, page)
	if err := r.fail[page]; err != nil {
		return github.DefaultCommitPage{}, err
	}
	return r.pages[page], nil
}

func (r completionFixtureReader) IssueComments(_ context.Context, repository string, number int64) ([]discovery.SpecComment, error) {
	if repository != r.repository {
		return nil, errors.New("other repository read")
	}
	if err := r.commentErr[number]; err != nil {
		return nil, err
	}
	return r.comments[number], nil
}

func (r completionFixtureReader) DefaultCommitComparisonPage(_ context.Context, repository, _, head string, _ int) (github.DefaultCommitPage, error) {
	if repository != r.repository || head != r.defaultSHA {
		return github.DefaultCommitPage{}, errors.New("other default branch read")
	}
	return github.DefaultCommitPage{Commits: r.commits, Final: true}, nil
}

func (r completionFixtureReader) IsAncestor(_ context.Context, repository, _, head string) (bool, error) {
	if repository != r.repository || head != r.defaultSHA {
		return false, errors.New("other default branch read")
	}
	return true, nil
}

func (r completionFixtureReader) VerifiedRevert(_ context.Context, repository, mergedSHA, revertSHA, defaultHead string) (bool, error) {
	if repository != r.repository || mergedSHA != r.mergedSHA || revertSHA != r.revertSHA || defaultHead != r.defaultSHA {
		return false, errors.New("other PR revert attribution")
	}
	return r.verified, nil
}

func completionAttempt(t *testing.T, engine state.Engine, issue int64, head string) (state.Attempt, github.PullSnapshot) {
	t.Helper()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	publication := state.Publication{
		Branch:          fmt.Sprintf("sofa/task-%d", issue),
		ExpectedHead:    strings.Repeat("1", 40),
		CandidateDigest: strings.Repeat("2", 64),
		HeadSHA:         head,
		PRNumber:        issue,
		PRURL:           fmt.Sprintf("https://github.com/owner/repo/pull/%d", issue),
	}
	a, _, err := engine.Admit(context.Background(), state.Admission{
		Repository:      "owner/repo",
		Issue:           issue,
		SpecDigest:      strings.Repeat("3", 64),
		ConfigDigest:    strings.Repeat("4", 64),
		BaseSHA:         strings.Repeat("1", 40),
		ProjectID:       "project",
		ProjectItemID:   fmt.Sprintf("item-%d", issue),
		StatusOptionID:  "ready",
		StatusUpdatedAt: now,
	}, state.Limits{
		RuntimeSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a.Phase = state.Draft
	a.Publication = &publication
	snapshot.State.Attempts[a.ID] = a
	if err := engine.Store.CompareAndSwap(context.Background(), snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	pull := github.PullSnapshot{
		Number:         issue,
		URL:            publication.PRURL,
		State:          "closed",
		Merged:         true,
		MergedAt:       now.Add(time.Hour),
		HeadSHA:        head,
		HeadRef:        publication.Branch,
		HeadRepository: "owner/repo",
		BaseRepository: "owner/repo",
	}
	return a, pull
}

func TestCompletionCorrectionsIsolateItemsAndKeepPriorDone(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{
		Store: &state.MemoryStore{},
	}
	first, firstPull := completionAttempt(t, engine, 7, strings.Repeat("a", 40))
	second, secondPull := completionAttempt(t, engine, 8, strings.Repeat("b", 40))
	firstPull.MergeCommitSHA = strings.Repeat("c", 40)
	secondPull.MergeCommitSHA = strings.Repeat("d", 40)
	defaultHead := strings.Repeat("e", 40)
	revertSHA := strings.Repeat("f", 40)
	mergeTime := firstPull.MergedAt
	reader := completionFixtureReader{
		repository: "owner/repo",
		defaultSHA: defaultHead,
		mergedSHA:  firstPull.MergeCommitSHA,
		revertSHA:  revertSHA,
		comments: map[int64][]discovery.SpecComment{
			7: {{
				ID:        70,
				Body:      "TOP_SECRET later report",
				CreatedAt: mergeTime.Add(time.Minute),
				UpdatedAt: mergeTime.Add(time.Minute),
			}},
			8: {{
				ID:        80,
				Body:      "Second PR feedback",
				CreatedAt: mergeTime.Add(time.Minute),
				UpdatedAt: mergeTime.Add(time.Minute),
			}},
		},
		commentErr: map[int64]error{7: errors.New("temporary GitHub failure")},
		commits: []github.DefaultCommit{
			{
				SHA:     revertSHA,
				Message: "Revert fix\n\nThis reverts commit " + firstPull.MergeCommitSHA + ".",
			},
			{SHA: defaultHead, Message: "ordinary commit"},
		},
		verified: true,
	}
	// A transient read on one completed PR must not consume the other PR's
	// comments or revert identity. Nor can it remove an earlier Done event.
	if err := engine.Observe(ctx, state.Observation{
		Version:    state.Version,
		ID:         "done-first",
		AttemptID:  first.ID,
		Stage:      "release",
		Outcome:    "done",
		Revision:   firstPull.MergeCommitSHA,
		RecordedAt: mergeTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Observe(ctx, state.Observation{Version: state.Version, ID: "done-second", AttemptID: second.ID, Stage: "release", Outcome: "done", Revision: secondPull.MergeCommitSHA, RecordedAt: mergeTime}); err != nil {
		t.Fatal(err)
	}
	if err := observeCompletionCorrections(ctx, reader, engine, first, firstPull, defaultHead); err == nil {
		t.Fatal("failed comment read treated as complete")
	}
	if err := observeCompletionCorrections(ctx, reader, engine, second, secondPull, defaultHead); err != nil {
		t.Fatalf("first PR failure blocked second PR: %v", err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.Observations) != 3 || snapshot.State.Observations[0].ID != "done-first" || snapshot.State.Observations[2].AttemptID != second.ID || snapshot.State.Observations[2].Stage != "later-feedback" {
		t.Fatalf("failure crossed PR boundary or erased Done: %+v", snapshot.State.Observations)
	}
	delete(reader.commentErr, 7)
	if err := observeCompletionCorrections(ctx, reader, engine, first, firstPull, defaultHead); err != nil {
		t.Fatal(err)
	}
	if err := observeCompletionCorrections(ctx, reader, engine, first, firstPull, defaultHead); err != nil {
		t.Fatalf("redelivery appended duplicate or lost correction: %v", err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.State.Observations) != 5 || snapshot.State.Observations[3].AttemptID != first.ID || snapshot.State.Observations[3].Stage != "later-feedback" || snapshot.State.Observations[4].AttemptID != first.ID || snapshot.State.Observations[4].Outcome != "reverted" || snapshot.State.Observations[4].Revision != revertSHA {
		t.Fatalf("corrections not linked to first completed PR: %+v", snapshot.State.Observations)
	}
	encoded, err := state.Encode(snapshot.State)
	if err != nil || strings.Contains(string(encoded), "TOP_SECRET") {
		t.Fatalf("raw later feedback leaked into ledger: %v", err)
	}
	changed := firstPull
	changed.HeadSHA = strings.Repeat("9", 40)
	if err := observeCompletionCorrections(ctx, reader, engine, first, changed, defaultHead); err == nil {
		t.Fatal("changed PR head inherited completion identity")
	}
}

func TestCompletionCorrectionsRemainEligibleAfterReleaseCheckRegresses(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{
		Store: &state.MemoryStore{},
	}
	attempt, pull := completionAttempt(t, engine, 9, strings.Repeat("a", 40))
	pull.MergeCommitSHA = strings.Repeat("b", 40)
	defaultHead := strings.Repeat("c", 40)
	if err := engine.Observe(ctx, state.Observation{
		Version:    state.Version,
		ID:         "done-9",
		AttemptID:  attempt.ID,
		Stage:      "release",
		Outcome:    "done",
		Revision:   pull.MergeCommitSHA,
		RecordedAt: pull.MergedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := release.Record(ctx, engine, release.Decision{
		AttemptID: attempt.ID,
		PRNumber:  pull.Number,
		Kind:      "release-failed",
		CommitSHA: pull.MergeCommitSHA,
		EventID:   "failed-9",
	}, pull.MergedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecordedCompletion(loaded.State, attempt.ID, pull.MergeCommitSHA) || hasRecordedCompletion(loaded.State, attempt.ID, defaultHead) || hasRecordedCompletion(loaded.State, "other-attempt", pull.MergeCommitSHA) {
		t.Fatal("later release failure erased or misattributed exact completed identity")
	}
	reader := completionFixtureReader{
		repository: "owner/repo",
		defaultSHA: defaultHead,
		comments: map[int64][]discovery.SpecComment{
			9: {{
				ID:        90,
				Body:      "later report",
				CreatedAt: pull.MergedAt.Add(2 * time.Minute),
				UpdatedAt: pull.MergedAt.Add(2 * time.Minute),
			}},
		},
	}
	if err := observeCompletionCorrections(ctx, reader, engine, attempt, pull, defaultHead); err != nil {
		t.Fatal(err)
	}
	loaded, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.State.Observations) != 3 || loaded.State.Observations[2].Stage != "later-feedback" || loaded.State.Observations[2].AttemptID != attempt.ID {
		t.Fatalf("later feedback lost after release regression: %+v", loaded.State.Observations)
	}
}

func TestCompletionRevertCursorRetriesPageAndFindsOlderRevert(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{Store: &state.MemoryStore{}}
	attempt, pull := completionAttempt(t, engine, 11, strings.Repeat("a", 40))
	pull.MergeCommitSHA = strings.Repeat("b", 40)
	head := strings.Repeat("c", 40)
	revert := strings.Repeat("d", 40)
	if err := engine.Observe(ctx, state.Observation{Version: state.Version, ID: "done-11", AttemptID: attempt.ID, Stage: "release", Outcome: "done", Revision: pull.MergeCommitSHA, RecordedAt: pull.MergedAt}); err != nil {
		t.Fatal(err)
	}
	reader := &pagedCompletionReader{
		completionFixtureReader: completionFixtureReader{repository: "owner/repo", defaultSHA: head, mergedSHA: pull.MergeCommitSHA, revertSHA: revert, verified: true},
		pages: map[int]github.DefaultCommitPage{
			1: {Commits: []github.DefaultCommit{{SHA: strings.Repeat("e", 40), Message: "ordinary"}}, Final: false},
			2: {Commits: []github.DefaultCommit{{SHA: revert, Message: "This reverts commit " + pull.MergeCommitSHA}, {SHA: head, Message: "ordinary"}}, Final: true},
		},
		fail: map[int]error{1: errors.New("temporary compare outage")},
	}
	if err := observeCompletionCorrections(ctx, reader, engine, attempt, pull, head); err == nil {
		t.Fatal("comparison outage was ignored")
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cursor := snapshot.State.Attempts[attempt.ID].RevertScan; cursor == nil || cursor.NextPage != 1 || cursor.CompletedHead != "" {
		t.Fatalf("failed page advanced cursor: %+v", cursor)
	}
	delete(reader.fail, 1)
	if err := observeCompletionCorrections(ctx, reader, engine, attempt, pull, head); err != nil {
		t.Fatal(err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cursor := snapshot.State.Attempts[attempt.ID].RevertScan; cursor == nil || cursor.NextPage != 2 {
		t.Fatalf("first page did not advance exactly once: %+v", cursor)
	}
	if err := observeCompletionCorrections(ctx, reader, engine, attempt, pull, head); err != nil {
		t.Fatal(err)
	}
	if err := observeCompletionCorrections(ctx, reader, engine, attempt, pull, head); err != nil {
		t.Fatal(err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cursor := snapshot.State.Attempts[attempt.ID].RevertScan
	if cursor == nil || cursor.CompletedHead != head || cursor.ActiveHead != "" || len(snapshot.State.Observations) != 2 || snapshot.State.Observations[1].Revision != revert || fmt.Sprint(reader.calls) != "[1 1 2]" {
		t.Fatalf("older revert lost or replayed: cursor=%+v observations=%+v calls=%v", cursor, snapshot.State.Observations, reader.calls)
	}
}
