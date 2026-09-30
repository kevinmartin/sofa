package state

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPollClaimCoalescesOverlappingAndMissedRuns(t *testing.T) {
	engine := Engine{
		Store: &MemoryStore{},
	}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	results := make(chan PollClaim, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claim, err := engine.ClaimPoll(context.Background(), base, 10*time.Minute, "manual-1")
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			results <- claim
		}()
	}
	wg.Wait()
	close(results)
	claimed := 0
	for result := range results {
		if result.Claimed {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("same wake revision claimed %d times", claimed)
	}
	duplicate, err := engine.ClaimPoll(context.Background(), base.Add(time.Minute), 10*time.Minute, "manual-1")
	if err != nil || duplicate.Claimed {
		t.Fatalf("duplicate wake caused scan: %+v, %v", duplicate, err)
	}
	event, err := engine.ClaimPoll(context.Background(), base.Add(2*time.Minute), 10*time.Minute, "manual-2")
	if err != nil || !event.Claimed || event.Generation != 2 {
		t.Fatalf("fresh wake not claimed: %+v, %v", event, err)
	}
	missed, err := engine.ClaimPoll(context.Background(), base.Add(4*time.Hour), 10*time.Minute, "")
	if err != nil || !missed.Claimed || missed.Missed < 20 || missed.Generation != 3 || missed.Next != base.Add(4*time.Hour+10*time.Minute) {
		t.Fatalf("missed windows did not coalesce: %+v, %v", missed, err)
	}
}

func TestPollClaimClampsSmallRunnerSkewButRejectsLargeJump(t *testing.T) {
	engine := Engine{Store: &MemoryStore{}}
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if _, err := engine.ClaimPoll(context.Background(), base, 10*time.Minute, ""); err != nil {
		t.Fatal(err)
	}
	claim, err := engine.ClaimPoll(context.Background(), base.Add(-10*time.Second), 10*time.Minute, "")
	if err != nil || claim.Claimed || claim.Generation != 1 || claim.Next != base.Add(10*time.Minute) {
		t.Fatalf("small skew did not coalesce: %+v, %v", claim, err)
	}
	if _, err := engine.ClaimPoll(context.Background(), base.Add(-2*time.Minute), 10*time.Minute, ""); err == nil {
		t.Fatalf("large backward jump accepted: %v", err)
	}
}
