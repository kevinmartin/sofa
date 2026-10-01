package state

import (
	"context"
	"fmt"
)

// AdvanceRevertScan persists one comparison-page boundary with optimistic
// concurrency. Replaying an already committed boundary is harmless; a
// competing scan of the same attempt cannot silently skip a page.
func (e Engine) AdvanceRevertScan(ctx context.Context, attemptID string, expected *RevertScanCursor, next RevertScanCursor) error {
	if !next.valid() || expected != nil && !expected.valid() {
		return fmt.Errorf("%w: revert scan cursor", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		attempt, ok := s.Attempts[attemptID]
		if !ok {
			return false, ErrNotFound
		}
		if attempt.Publication == nil || !recordedDoneForMerge(*s, attemptID, next.MergeSHA) {
			return false, fmt.Errorf("%w: revert scan has no completed PR", ErrInvalid)
		}
		if sameRevertCursor(attempt.RevertScan, &next) {
			return false, nil
		}
		if !sameRevertCursor(attempt.RevertScan, expected) || !validRevertScanStep(expected, next) {
			return false, ErrConflict
		}
		attempt.RevertScan = &next
		s.Attempts[attemptID] = attempt
		return true, nil
	})
}

func recordedDoneForMerge(s State, attemptID, mergeSHA string) bool {
	for _, observation := range s.Observations {
		if observation.AttemptID == attemptID && observation.Stage == "release" && observation.Outcome == "done" && observation.Revision == mergeSHA {
			return true
		}
	}
	return false
}

func sameRevertCursor(a, b *RevertScanCursor) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func validRevertScanStep(previous *RevertScanCursor, next RevertScanCursor) bool {
	if previous == nil {
		return next.CompletedHead == next.MergeSHA && next.ActiveHead == "" || next.CompletedHead == "" && next.ActiveBase == next.MergeSHA && next.NextPage == 1
	}
	if previous.MergeSHA != next.MergeSHA {
		return false
	}
	if previous.ActiveHead == "" {
		return next.CompletedHead == previous.CompletedHead && next.ActiveBase == previous.CompletedHead && next.ActiveHead != "" && next.NextPage == 1
	}
	if next.ActiveHead == previous.ActiveHead && next.ActiveBase == previous.ActiveBase && next.CompletedHead == previous.CompletedHead {
		return next.NextPage == previous.NextPage+1
	}
	return next.ActiveBase == "" && next.ActiveHead == "" && next.NextPage == 0 && next.CompletedHead == previous.ActiveHead
}
