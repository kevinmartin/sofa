package state

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

type PollClaim struct {
	Claimed    bool
	Generation int64
	Missed     int64
	Next       time.Time
}

// ClaimPoll persists one due scan before Project or model work. CAS coalesces
// overlapping Actions runs and duplicate event/manual wakeups. A failed scan
// is retried by the next due tick, never by minting a new claim for the same
// event revision. All missed intervals produce one claim.
func (e Engine) ClaimPoll(ctx context.Context, now time.Time, interval time.Duration, wakeID string) (PollClaim, error) {
	if now.IsZero() || interval < time.Minute || interval > 24*time.Hour || (wakeID != "" && !reference(wakeID)) {
		return PollClaim{}, fmt.Errorf("%w: poll schedule", ErrInvalid)
	}
	now = now.UTC()
	var claim PollClaim
	err := e.update(ctx, func(s *State) (bool, error) {
		claim = PollClaim{}
		cursor := PollCursor{}
		if s.Poll != nil {
			cursor = *s.Poll
		}
		if !cursor.LastPoll.IsZero() && now.Before(cursor.LastPoll) {
			if cursor.LastPoll.Sub(now) > time.Minute {
				return false, errors.New("poll clock moved backward")
			}
			// Coalesce small runner clock skew without moving the cursor back.
			now = cursor.LastPoll
		}
		timeDue := cursor.LastPoll.IsZero() || !now.Before(cursor.LastPoll.Add(interval))
		wakeDue := wakeID != "" && wakeID != cursor.LastWakeID
		if !timeDue && !wakeDue {
			claim.Next = cursor.LastPoll.Add(interval)
			claim.Generation = cursor.Generation
			return false, nil
		}
		if cursor.Generation == math.MaxInt64 {
			return false, ErrLimit
		}
		if !cursor.LastPoll.IsZero() {
			claim.Missed = int64(now.Sub(cursor.LastPoll) / interval)
		}
		cursor.LastPoll = now
		if wakeDue {
			cursor.LastWakeID = wakeID
		}
		cursor.Generation++
		s.Poll = &cursor
		claim.Claimed = true
		claim.Generation = cursor.Generation
		claim.Next = now.Add(interval)
		return true, nil
	})
	return claim, err
}
