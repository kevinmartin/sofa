package state

import (
	"context"
	"fmt"
)

// ClaimReviewRepair atomically fences a reserved repair and charges the prompt
// and runtime budget for this generation. An interrupted admission therefore
// cannot claim an uncharged prompt or charge one generation twice.
func (e Engine) ClaimReviewRepair(ctx context.Context, id string, owner Owner, charge Counters) (fence Fence, err error) {
	if !validOwner(owner) || charge.ModelCalls != 1 || charge.RuntimeSeconds <= 0 || charge.Repairs != 0 || charge.InfrastructureRetries != 0 {
		return fence, fmt.Errorf("%w: repair owner or charge", ErrInvalid)
	}
	err = e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if a.Repair == nil || a.Publication == nil || a.Repair.CandidateSHA != "" || a.Repair.CandidateDigest != "" || a.Counts.Repairs < 1 {
			return false, fmt.Errorf("%w: repair reservation unavailable", ErrInvalid)
		}
		if a.Owner != nil || a.Phase != Pending {
			return false, ErrClaimed
		}
		next := Counters{
			ModelCalls:            a.Counts.ModelCalls + charge.ModelCalls,
			Repairs:               a.Counts.Repairs,
			InfrastructureRetries: a.Counts.InfrastructureRetries,
			RuntimeSeconds:        a.Counts.RuntimeSeconds + charge.RuntimeSeconds,
		}
		if !a.Limits.permits(next) {
			return false, ErrLimit
		}
		a.Counts = next
		a.Generation++
		a.Owner = &owner
		a.Phase = Executing
		a.Dispatch = "claimed"
		a.UpdatedAt = e.now()
		s.Attempts[id] = a
		fence = Fence{AttemptID: id, Generation: a.Generation, Owner: owner}
		return true, nil
	})
	return
}

// ReserveReviewRepair converts an existing draft attempt into one bounded
// repair dispatch. The caller must first validate the current PR and the
// immutable owner review against GitHub. This CAS reserves budget before any
// model work and preserves the existing PR identity for same-PR publication.
// Replaying the same feedback is idempotent; different feedback cannot borrow
// an active or already consumed reservation.
func (e Engine) ReserveReviewRepair(ctx context.Context, id string, intent RepairIntent) (bool, error) {
	reserved := false
	err := e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if !intent.valid(a.Publication) || intent.CandidateSHA != "" || intent.CandidateDigest != "" {
			return false, fmt.Errorf("%w: review repair identity", ErrInvalid)
		}
		if a.Repair != nil {
			if *a.Repair == intent {
				return false, nil
			}
			return false, ErrAdmissionChanged
		}
		if a.Phase != Draft || a.Counts.Repairs >= a.Limits.Repairs {
			return false, ErrLimit
		}
		for _, prior := range s.Observations {
			if prior.ID == intent.FeedbackID {
				return false, ErrConflict
			}
		}
		a.Counts.Repairs++
		a.Repair = &intent
		a.Owner = nil
		a.Phase = Pending
		a.Dispatch = "pending"
		a.Checkpoint = nil
		a.Failure = ""
		a.UpdatedAt = e.now()
		s.Attempts[id] = a
		s.Observations = append(s.Observations, Observation{
			Version:    Version,
			ID:         intent.FeedbackID,
			AttemptID:  id,
			Stage:      "review-feedback",
			Outcome:    "repair-reserved",
			Revision:   intent.PRHeadSHA,
			RecordedAt: e.now(),
		})
		reserved = true
		return true, nil
	})
	if err != nil {
		return false, err
	}
	return reserved, nil
}

// BeginRepairPublication records the exact verified child commit before a
// leased branch push. A crash after the push can reconcile this intent rather
// than manufacturing another candidate or overwriting a changed branch.
func (e Engine) BeginRepairPublication(ctx context.Context, fence Fence, digest, commitSHA string) error {
	if !digestPattern.MatchString(digest) || !shaPattern.MatchString(commitSHA) {
		return fmt.Errorf("%w: repair candidate identity", ErrInvalid)
	}
	return e.mutateOwned(ctx, fence, func(a *Attempt) error {
		if a.Repair == nil || a.Publication == nil {
			return fmt.Errorf("%w: missing repair reservation", ErrInvalid)
		}
		if a.Phase == Publishing {
			if a.Repair.CandidateDigest == digest && a.Repair.CandidateSHA == commitSHA {
				return nil
			}
			return ErrConflict
		}
		if a.Phase != Validating {
			return fmt.Errorf("%w: repair publication phase", ErrInvalid)
		}
		if a.Repair.CandidateSHA != "" || a.Repair.CandidateDigest != "" {
			if a.Repair.CandidateSHA != commitSHA || a.Repair.CandidateDigest != digest {
				return ErrConflict
			}
			a.Phase = Publishing
			return nil
		}
		a.Repair.CandidateDigest = digest
		a.Repair.CandidateSHA = commitSHA
		a.Phase = Publishing
		return nil
	})
}

// MarkReviewRepairPublished acknowledges only the same existing PR at the
// committed child SHA. Caller must independently observe the remote PR and
// verify its head, base, ownership and current authority first.
func (e Engine) MarkReviewRepairPublished(ctx context.Context, fence Fence, published Publication) error {
	if !published.valid() {
		return fmt.Errorf("%w: published repair", ErrInvalid)
	}
	return e.mutateOwned(ctx, fence, func(a *Attempt) error {
		if a.Repair == nil {
			if a.Phase == Draft && a.Publication != nil && *a.Publication == published {
				return nil
			}
			return fmt.Errorf("%w: missing repair reservation", ErrInvalid)
		}
		if a.Phase != Publishing || a.Publication == nil || a.Repair.CandidateSHA != published.HeadSHA || a.Repair.CandidateDigest != published.CandidateDigest || a.Repair.PRHeadSHA != published.ExpectedHead || a.Repair.PRNumber != published.PRNumber || a.Publication.Branch != published.Branch || a.Publication.PRURL != published.PRURL || a.Publication.PRNumber != published.PRNumber {
			return fmt.Errorf("%w: repair publication identity", ErrInvalid)
		}
		a.Publication = &published
		a.Repair = nil
		a.Phase = Draft
		return nil
	})
}
