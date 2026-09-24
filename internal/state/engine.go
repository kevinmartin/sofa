package state

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Engine serializes deterministic state changes with optimistic retries.
// It never calls models, dispatches jobs, accesses artifacts, or publishes PRs.
type Engine struct {
	Store Store
	Now   func() time.Time
}

func (e Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e Engine) update(ctx context.Context, f func(*State) (bool, error)) error {
	if e.Store == nil {
		return fmt.Errorf("%w: missing store", ErrInvalid)
	}
	for retry := 0; retry < 64; retry++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := e.Store.Load(ctx)
		if err != nil {
			return err
		}
		if err := snapshot.State.Validate(); err != nil {
			return err
		}
		changed, err := f(&snapshot.State)
		if err != nil || !changed {
			return err
		}
		if err = e.Store.CompareAndSwap(ctx, snapshot.Revision, snapshot.State); !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

// Admit persists dispatch intent before any external dispatch. Same-authority
// replay returns the original attempt; changed snapshots cannot mint new work.
// Superseding an issue's existing authorization is intentionally not automatic.
func (e Engine) Admit(ctx context.Context, admission Admission, limits Limits) (attempt Attempt, created bool, err error) {
	if err = admission.Validate(); err != nil {
		return
	}
	if !limits.valid() {
		return attempt, false, fmt.Errorf("%w: limits", ErrInvalid)
	}
	admission.Repository = strings.ToLower(admission.Repository)
	id := AttemptID(admission)
	err = e.update(ctx, func(s *State) (bool, error) {
		created = false
		if a, ok := s.Attempts[id]; ok {
			attempt = a
			if a.Admission != admission || a.Limits != limits {
				return false, ErrAdmissionChanged
			}
			return false, nil
		}
		for _, a := range s.Attempts {
			if a.Admission.Repository == admission.Repository && a.Admission.Issue == admission.Issue {
				return false, ErrAdmissionChanged
			}
		}
		now := e.now()
		attempt = Attempt{ID: id, Admission: admission, Phase: Pending, Dispatch: "pending", Limits: limits, CreatedAt: now, UpdatedAt: now}
		s.Attempts[id] = attempt
		created = true
		return true, nil
	})
	if err != nil {
		created = false
	}
	return
}

// MarkDispatched acknowledges an external dispatch, but grants no authority.
// A crash before this acknowledgement is safe: repeated deliveries must Claim.
func (e Engine) MarkDispatched(ctx context.Context, id string) error {
	return e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if a.Dispatch != "pending" {
			return false, nil
		}
		a.Dispatch = "sent"
		a.UpdatedAt = e.now()
		s.Attempts[id] = a
		return true, nil
	})
}

// Claim is deliberately non-idempotent even for the same owner: two deliveries
// in one Actions run must not both execute. Keep the returned fence in the job.
func (e Engine) Claim(ctx context.Context, id string, owner Owner) (fence Fence, err error) {
	if !validOwner(owner) {
		return fence, fmt.Errorf("%w: owner", ErrInvalid)
	}
	err = e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if a.Owner != nil || a.Phase != Pending {
			return false, ErrClaimed
		}
		a.Generation++
		a.Owner = &owner
		a.Phase = Executing
		a.Dispatch = "claimed"
		a.UpdatedAt = e.now()
		if a.Checkpoint != nil && !a.Checkpoint.ExpiresAt.After(e.now()) {
			a.Checkpoint = nil
		}
		s.Attempts[id] = a
		fence = Fence{id, a.Generation, owner}
		return true, nil
	})
	return
}

func owned(s *State, f Fence) (Attempt, error) {
	a, ok := s.Attempts[f.AttemptID]
	if !ok {
		return a, ErrNotFound
	}
	if a.Owner == nil || *a.Owner != f.Owner || a.Generation != f.Generation {
		return a, ErrStale
	}
	return a, nil
}

// AssertOwner is an enforcement check, not a distributed transaction. Check
// again before each external write; a cancellation racing a write cannot undo it.
func (e Engine) AssertOwner(ctx context.Context, f Fence) error {
	s, err := e.Store.Load(ctx)
	if err != nil {
		return err
	}
	a, err := owned(&s.State, f)
	if err != nil {
		return err
	}
	if a.Phase == Draft || a.Phase == Blocked || a.Phase == Deferred {
		return ErrClaimed
	}
	return nil
}

func (e Engine) mutateOwned(ctx context.Context, f Fence, mutation func(*Attempt) error) error {
	return e.update(ctx, func(s *State) (bool, error) {
		a, err := owned(s, f)
		if err != nil {
			return false, err
		}
		before := a
		if err := mutation(&a); err != nil {
			return false, err
		}
		if reflect.DeepEqual(before, a) {
			return false, nil
		}
		a.UpdatedAt = e.now()
		s.Attempts[a.ID] = a
		return true, nil
	})
}

// Charge reserves cumulative budget before the associated external work. Lost
// work still consumes its reservation; a rerun never resets these counters.
func (e Engine) Charge(ctx context.Context, f Fence, delta Counters) error {
	if !delta.valid() {
		return fmt.Errorf("%w: negative counter", ErrInvalid)
	}
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if a.Phase == Draft || a.Phase == Blocked || a.Phase == Deferred {
			return ErrClaimed
		}
		next := Counters{a.Counts.ModelCalls + delta.ModelCalls, a.Counts.Repairs + delta.Repairs, a.Counts.InfrastructureRetries + delta.InfrastructureRetries, a.Counts.RuntimeSeconds + delta.RuntimeSeconds}
		if !a.Limits.permits(next) {
			return ErrLimit
		}
		a.Counts = next
		return nil
	})
}

func (e Engine) Advance(ctx context.Context, f Fence, phase Phase) error {
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if a.Phase == phase {
			return nil
		}
		if a.Phase != Executing || phase != Validating {
			return fmt.Errorf("%w: phase transition", ErrInvalid)
		}
		a.Phase = phase
		return nil
	})
}

// SaveCheckpoint records only provenance already validated by the trusted
// caller. Recording or recovering a checkpoint is never itself a passing check.
func (e Engine) SaveCheckpoint(ctx context.Context, f Fence, checkpoint Checkpoint) error {
	if !checkpoint.valid() || checkpoint.Producer != f.Owner || checkpoint.Generation != f.Generation || !checkpoint.ExpiresAt.After(e.now()) || checkpoint.AcceptedAt.After(e.now()) {
		return fmt.Errorf("%w: checkpoint provenance or expiry", ErrInvalid)
	}
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if checkpoint.Phase != a.Phase {
			return fmt.Errorf("%w: checkpoint phase", ErrInvalid)
		}
		a.Checkpoint = &checkpoint
		return nil
	})
}

// DiscardUnavailableCheckpoint permits a fresh execution only after the
// controller has proved the retained artifact is absent or expired. The
// checkpoint identity is compared under CAS, and a publication intent can
// never be discarded here: its branch or PR may already exist.
func (e Engine) DiscardUnavailableCheckpoint(ctx context.Context, id string, expected Checkpoint) error {
	if !expected.valid() {
		return fmt.Errorf("%w: checkpoint", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if a.Phase != Pending || a.Owner != nil || a.Publication != nil {
			return false, ErrClaimed
		}
		if a.Checkpoint == nil || *a.Checkpoint != expected {
			return false, ErrConflict
		}
		a.Checkpoint = nil
		a.UpdatedAt = e.now()
		s.Attempts[id] = a
		return true, nil
	})
}

func (e Engine) BeginPublication(ctx context.Context, f Fence, publication Publication) error {
	if !publication.valid() || publication.PRNumber != 0 {
		return fmt.Errorf("%w: publication intent", ErrInvalid)
	}
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if a.Publication != nil {
			p := *a.Publication
			p.PRNumber = 0
			p.PRURL = ""
			p.HeadSHA = ""
			if p != publication {
				return fmt.Errorf("%w: changed publication", ErrInvalid)
			}
			if a.Phase == Draft {
				return nil
			}
		}
		if a.Phase != Validating && a.Phase != Publishing {
			return fmt.Errorf("%w: publication phase", ErrInvalid)
		}
		a.Publication = &publication
		a.Phase = Publishing
		return nil
	})
}

// MarkPublished must be given independently observed matching branch/PR state.
// It also acknowledges an existing PR after publication-before-ledger crashes.
func (e Engine) MarkPublished(ctx context.Context, f Fence, publication Publication) error {
	if !publication.valid() || publication.PRNumber <= 0 {
		return fmt.Errorf("%w: published PR", ErrInvalid)
	}
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if a.Publication == nil {
			return fmt.Errorf("%w: no publication intent", ErrInvalid)
		}
		p := publication
		p.PRNumber = a.Publication.PRNumber
		p.PRURL = a.Publication.PRURL
		p.HeadSHA = a.Publication.HeadSHA
		if p != *a.Publication {
			return fmt.Errorf("%w: publication identity", ErrInvalid)
		}
		if a.Phase == Draft {
			if *a.Publication == publication {
				return nil
			}
			return ErrConflict
		}
		if a.Phase != Publishing {
			return fmt.Errorf("%w: publication phase", ErrInvalid)
		}
		a.Publication = &publication
		a.Phase = Draft
		return nil
	})
}

func (e Engine) Fail(ctx context.Context, f Fence, kind string) error {
	if kind != "authentication" && kind != "quota" && kind != "infrastructure" && kind != "validation" && kind != "authority" && kind != "human-change" {
		return fmt.Errorf("%w: failure class", ErrInvalid)
	}
	return e.mutateOwned(ctx, f, func(a *Attempt) error {
		if a.Phase == Draft {
			return ErrClaimed
		}
		a.Failure = kind
		a.Phase = Blocked
		if kind == "quota" || kind == "infrastructure" {
			a.Phase = Deferred
		}
		return nil
	})
}

// RunProof is an authoritative runtime observation supplied by reconciliation.
// A local timeout, stale heartbeat, or missing API response is not proof.
type RunProof struct {
	Owner      Owner
	Status     string
	Conclusion string
	ObservedAt time.Time
}

func (p RunProof) terminal(now time.Time) bool {
	if p.Status != "completed" || p.ObservedAt.IsZero() || p.ObservedAt.After(now) || now.Sub(p.ObservedAt) > 5*time.Minute {
		return false
	}
	switch p.Conclusion {
	case "success", "failure", "cancelled", "timed_out", "action_required", "neutral", "skipped", "stale":
		return true
	}
	return false
}

// Recover releases a stopped owner, retaining counts and publication intent.
// Blocked authentication, authority, validation and human-change cases require
// operator resolution; they cannot enter an automatic retry loop.
func (e Engine) Recover(ctx context.Context, id string, proof RunProof) error {
	if !proof.terminal(e.now()) {
		return ErrActive
	}
	return e.update(ctx, func(s *State) (bool, error) {
		a, ok := s.Attempts[id]
		if !ok {
			return false, ErrNotFound
		}
		if a.Owner == nil || *a.Owner != proof.Owner {
			return false, ErrStale
		}
		if a.Phase == Draft || a.Phase == Blocked {
			return false, ErrClaimed
		}
		if a.Counts.InfrastructureRetries >= a.Limits.InfrastructureRetries {
			return false, ErrLimit
		}
		a.Counts.InfrastructureRetries++
		a.Owner = nil
		a.Generation++
		a.Phase = Pending
		a.Dispatch = "pending"
		a.Failure = ""
		a.UpdatedAt = e.now()
		if a.Checkpoint != nil && !a.Checkpoint.ExpiresAt.After(e.now()) {
			a.Checkpoint = nil
		}
		s.Attempts[id] = a
		return true, nil
	})
}

// Observe is idempotent by record ID. The same ID with different content fails
// instead of rewriting a historical observation.
func (e Engine) Observe(ctx context.Context, observation Observation) error {
	return e.update(ctx, func(s *State) (bool, error) {
		for _, previous := range s.Observations {
			if previous.ID == observation.ID {
				if previous == observation {
					return false, nil
				}
				return false, ErrConflict
			}
		}
		s.Observations = append(s.Observations, observation)
		if err := s.Validate(); err != nil {
			return false, err
		}
		return true, nil
	})
}
