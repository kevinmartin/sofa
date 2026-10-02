package state

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRevertScanCursorRequiresExactCompletedAttemptAndCAS(t *testing.T) {
	ctx := context.Background()
	engine := Engine{Store: &MemoryStore{}}
	admission := Admission{Repository: "owner/repo", Issue: 7, SpecDigest: strings.Repeat("a", 64), ConfigDigest: strings.Repeat("b", 64), BaseSHA: strings.Repeat("c", 40), ProjectID: "project", ProjectItemID: "item", StatusOptionID: "ready", StatusUpdatedAt: time.Now().UTC()}
	attempt, _, err := engine.Admit(ctx, admission, Limits{RuntimeSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Publication = &Publication{Branch: "sofa/task", ExpectedHead: admission.BaseSHA, CandidateDigest: strings.Repeat("d", 64), HeadSHA: strings.Repeat("e", 40), PRNumber: 7, PRURL: "https://github.com/owner/repo/pull/7"}
	snapshot.State.Attempts[attempt.ID] = attempt
	if err := engine.Store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); err != nil {
		t.Fatal(err)
	}
	merge := strings.Repeat("f", 40)
	head := strings.Repeat("1", 40)
	start := RevertScanCursor{MergeSHA: merge, ActiveBase: merge, ActiveHead: head, NextPage: 1}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, nil, start); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uncompleted PR gained cursor: %v", err)
	}
	if err := engine.Observe(ctx, Observation{Version: Version, ID: "done-7", AttemptID: attempt.ID, Stage: "release", Outcome: "done", Revision: merge, RecordedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, nil, start); err != nil {
		t.Fatal(err)
	}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, nil, start); err != nil {
		t.Fatalf("same boundary replay: %v", err)
	}
	pageTwo := start
	pageTwo.NextPage = 2
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, nil, pageTwo); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale page skipped ahead: %v", err)
	}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, &start, pageTwo); err != nil {
		t.Fatal(err)
	}
	completed := RevertScanCursor{MergeSHA: merge, CompletedHead: head}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, &pageTwo, completed); err != nil {
		t.Fatal(err)
	}
	if err := engine.AdvanceRevertScan(ctx, attempt.ID, &pageTwo, completed); err != nil {
		t.Fatalf("completed boundary replay: %v", err)
	}
	snapshot, err = engine.Store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.State.Attempts[attempt.ID].RevertScan; got == nil || *got != completed {
		t.Fatalf("wrong completed cursor: %+v", got)
	}
}
