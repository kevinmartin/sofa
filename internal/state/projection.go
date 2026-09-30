package state

import (
	"context"
	"errors"
	"fmt"
)

// ObserveBoard remembers an exact Project status revision. The only unplanned
// transitions accepted automatically are the owner-controlled admission moves
// in a restricted Project. Other human edits are reported as conflicts and
// never overwritten on the next poll.
func (e Engine) ObserveBoard(ctx context.Context, observed BoardProjection) error {
	if !observed.valid() || observed.PendingStage != "" {
		return fmt.Errorf("%w: board observation", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		if s.Projections == nil {
			s.Projections = make(map[string]BoardProjection)
		}
		prior, ok := s.Projections[observed.IssueID]
		if !ok {
			s.Projections[observed.IssueID] = observed
			return true, nil
		}
		if prior.Repository != observed.Repository || prior.ProjectID != observed.ProjectID || prior.ProjectItemID != observed.ProjectItemID {
			return false, ErrConflict
		}
		if observed.UpdatedAt.Before(prior.UpdatedAt) {
			return false, ErrConflict
		}
		if prior.PendingStage != "" {
			if observed.Stage == prior.PendingStage && observed.OptionID == prior.PendingOptionID && observed.UpdatedAt.After(prior.PendingFromUpdatedAt) {
				s.Projections[observed.IssueID] = observed
				return true, nil
			}
			if sameBoardRevision(prior, observed) {
				return false, nil // Write intent remains recoverable.
			}
			return false, ErrConflict
		}
		if sameBoardRevision(prior, observed) {
			return false, nil
		}
		if !ownerTransition(prior.Stage, observed.Stage) || !observed.UpdatedAt.After(prior.UpdatedAt) {
			return false, ErrConflict
		}
		s.Projections[observed.IssueID] = observed
		return true, nil
	})
}

func sameBoardRevision(a, b BoardProjection) bool {
	return a.Stage == b.Stage && a.OptionID == b.OptionID && a.UpdatedAt.Equal(b.UpdatedAt)
}

func ownerTransition(from, to string) bool {
	switch {
	case from == "inbox" && to == "discovery":
		return true
	case from == "spec_review" && to == "backlog":
		return true
	case from == "backlog" && to == "ready":
		return true
	}
	return false
}

// BeginBoardMove persists one exact write intent before the external Project
// mutation. A crash can safely retry the same intent after re-reading status.
func (e Engine) BeginBoardMove(ctx context.Context, expected BoardProjection, targetStage, targetOptionID string) error {
	if !expected.valid() || expected.PendingStage != "" || !reference(targetOptionID) || targetOptionID == expected.OptionID || !factoryTransition(expected.Stage, targetStage) {
		return fmt.Errorf("%w: board transition", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		prior, ok := s.Projections[expected.IssueID]
		if !ok {
			return false, ErrNotFound
		}
		if prior.PendingStage != "" {
			if prior.PendingStage == targetStage && prior.PendingOptionID == targetOptionID && prior.PendingFromOptionID == expected.OptionID && prior.PendingFromUpdatedAt.Equal(expected.UpdatedAt) {
				return false, nil
			}
			return false, ErrConflict
		}
		if prior != expected {
			return false, ErrConflict
		}
		prior.PendingStage = targetStage
		prior.PendingOptionID = targetOptionID
		prior.PendingFromOptionID = expected.OptionID
		prior.PendingFromUpdatedAt = expected.UpdatedAt
		s.Projections[expected.IssueID] = prior
		return true, nil
	})
}

func factoryTransition(from, to string) bool {
	if to == "spec_review" {
		return from == "discovery" || from == "backlog"
	}
	switch from + ">" + to {
	case "ready>building", "building>verification", "verification>review", "review>release", "release>done", "review>building", "verification>building":
		return true
	}
	return false
}

// PendingBoardMove exposes recoverable intent without allowing arbitrary
// callers to clear it after a lost API response.
func (e Engine) PendingBoardMove(ctx context.Context, issueID string) (BoardProjection, bool, error) {
	if !reference(issueID) {
		return BoardProjection{}, false, fmt.Errorf("%w: issue identity", ErrInvalid)
	}
	snapshot, err := e.Store.Load(ctx)
	if err != nil {
		return BoardProjection{}, false, err
	}
	projection, ok := snapshot.State.Projections[issueID]
	if !ok || projection.PendingStage == "" {
		return projection, false, nil
	}
	if !projection.valid() || !factoryTransition(projection.Stage, projection.PendingStage) {
		return BoardProjection{}, false, errors.New("invalid pending Project transition")
	}
	return projection, true, nil
}
