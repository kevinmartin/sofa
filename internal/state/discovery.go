package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type DiscoveryAdmission struct {
	Repository      string
	IssueID         string
	Issue           int64
	ProjectID       string
	ProjectItemID   string
	StatusOptionID  string
	StatusUpdatedAt time.Time
	SourceDigest    string
}

// AdmitDiscovery reserves WIP atomically. The caller must have checked the
// private, owner-writable Project's current Discovery status; issue creation
// alone never reaches this method. Identical admissions replay existing tasks;
// a later owner transition may reset or revise work while retaining call usage.
// created reports a new or reset task, not a budget increase on an existing task.
func (e Engine) AdmitDiscovery(ctx context.Context, admission DiscoveryAdmission, maxActive int, maxModelCalls int64) (task DiscoveryTask, created bool, err error) {
	if maxActive < 1 || maxActive > 20 || maxModelCalls < 1 || maxModelCalls > 20 {
		return task, false, fmt.Errorf("%w: discovery limits", ErrInvalid)
	}
	admission.Repository = strings.ToLower(admission.Repository)
	task = DiscoveryTask{
		Repository:      admission.Repository,
		IssueID:         admission.IssueID,
		Issue:           admission.Issue,
		ProjectID:       admission.ProjectID,
		ProjectItemID:   admission.ProjectItemID,
		StatusOptionID:  admission.StatusOptionID,
		StatusUpdatedAt: admission.StatusUpdatedAt,
		SourceDigest:    admission.SourceDigest,
		Phase:           DiscoveryPending,
		MaxModelCalls:   maxModelCalls,
		CreatedAt:       e.now(),
		UpdatedAt:       e.now(),
	}
	if !task.valid() {
		return DiscoveryTask{}, false, fmt.Errorf("%w: discovery admission", ErrInvalid)
	}
	err = e.update(ctx, func(s *State) (bool, error) {
		created = false
		if previous, ok := s.Discoveries[task.IssueID]; ok {
			if previous.Repository != task.Repository || previous.Issue != task.Issue || previous.ProjectID != task.ProjectID || previous.ProjectItemID != task.ProjectItemID {
				return false, ErrAdmissionChanged
			}
			if previous.StatusOptionID == task.StatusOptionID && previous.StatusUpdatedAt.Equal(task.StatusUpdatedAt) && previous.SourceDigest == task.SourceDigest {
				if previous.MaxModelCalls == task.MaxModelCalls {
					task = previous
					return false, nil
				}
				if task.MaxModelCalls <= previous.MaxModelCalls || previous.Phase != DiscoveryBlocked || previous.Failure != "budget" {
					return false, ErrAdmissionChanged
				}
				active := 0
				for _, other := range s.Discoveries {
					if other.Repository == task.Repository && (other.Phase == DiscoveryPending || other.Phase == DiscoveryRunning) {
						active++
					}
				}
				if active >= maxActive {
					return false, ErrLimit
				}
				previous.MaxModelCalls = task.MaxModelCalls
				previous.Phase = DiscoveryPending
				previous.Failure = ""
				previous.UpdatedAt = e.now()
				s.Discoveries[task.IssueID] = previous
				task = previous
				return true, nil
			}
			if previous.Phase != DiscoveryReview && previous.Phase != DiscoveryBlocked && (previous.Phase != DiscoveryPending || previous.Owner != nil) || !task.StatusUpdatedAt.After(previous.StatusUpdatedAt) || task.MaxModelCalls < previous.MaxModelCalls || previous.Publication != nil {
				return false, ErrAdmissionChanged
			}
			record, hasRecord := s.Specs[task.IssueID]
			completed := previous.Phase == DiscoveryReview && hasRecord && record.ApprovedDigest != "" && record.Revision == previous.Revision
			if !completed && previous.ModelCalls >= task.MaxModelCalls {
				return false, ErrLimit
			}
			if completed {
				if record.Repository != previous.Repository || record.Issue != previous.Issue || record.ProjectID != previous.ProjectID || record.ProjectItemID != previous.ProjectItemID || record.SourceDigest != previous.SourceDigest || record.SpecDigest != previous.SpecDigest || record.CommentID != previous.CommentID || record.CommentAuthorID != previous.CommentAuthorID || !record.CommentCreatedAt.Equal(previous.CommentCreatedAt) || !record.CommentUpdatedAt.Equal(previous.CommentUpdatedAt) || task.SourceDigest == previous.SourceDigest || !task.StatusUpdatedAt.After(record.BacklogUpdatedAt) {
					return false, ErrAdmissionChanged
				}
			} else if hasRecord && record.Revision == previous.Revision {
				if previous.Phase != DiscoveryReview || record.ApprovedDigest != "" || record.Repository != previous.Repository || record.Issue != previous.Issue || record.ProjectID != previous.ProjectID || record.ProjectItemID != previous.ProjectItemID || record.SourceDigest != previous.SourceDigest || record.SpecDigest != previous.SpecDigest || record.CommentID != previous.CommentID || record.CommentAuthorID != previous.CommentAuthorID || !record.CommentCreatedAt.Equal(previous.CommentCreatedAt) || !record.CommentUpdatedAt.Equal(previous.CommentUpdatedAt) {
					return false, ErrAdmissionChanged
				}
			} else if hasRecord && (record.ApprovedDigest == "" || record.Revision != previous.Revision-1) {
				return false, ErrAdmissionChanged
			}
			active := 0
			for _, other := range s.Discoveries {
				if other.IssueID != task.IssueID && other.Repository == task.Repository && (other.Phase == DiscoveryPending || other.Phase == DiscoveryRunning) {
					active++
				}
			}
			if active >= maxActive {
				return false, ErrLimit
			}
			// A completed, approved revision advances the approval sequence. A
			// rejected proposal or failed task retries the same revision, retaining
			// its model budget without treating the old proposal as approved history.
			for id, a := range s.Attempts {
				if a.Admission.Repository != task.Repository || a.Admission.Issue != task.Issue || !a.SupersededAt.IsZero() {
					continue
				}
				if !completed || a.SpecRevision != previous.Revision || a.Admission.ProjectID != task.ProjectID || a.Admission.ProjectItemID != task.ProjectItemID || a.Admission.SpecDigest != record.ApprovedDigest {
					return false, ErrAdmissionChanged
				}
				a.Owner = nil
				a.Generation++
				a.Phase = Blocked
				a.Failure = "human-change"
				a.SupersededAt = e.now()
				a.SupersededSourceDigest = task.SourceDigest
				a.UpdatedAt = e.now()
				s.Attempts[id] = a
			}
			task.Revision = previous.Revision
			if completed {
				task.Revision++
				if s.DiscoveryHistory == nil {
					s.DiscoveryHistory = map[string][]DiscoveryTask{}
				}
				s.DiscoveryHistory[task.IssueID] = append(s.DiscoveryHistory[task.IssueID], previous)
			} else {
				history := s.DiscoveryHistory[task.IssueID]
				if len(history) > 0 && history[len(history)-1].SourceDigest == task.SourceDigest {
					return false, fmt.Errorf("%w: source matches the last approved Discovery revision", ErrAdmissionChanged)
				}
				if s.DiscoveryResetHistory == nil {
					s.DiscoveryResetHistory = map[string][]DiscoveryTask{}
				}
				if len(s.DiscoveryResetHistory[task.IssueID]) >= 20 {
					return false, ErrLimit
				}
				s.DiscoveryResetHistory[task.IssueID] = append(s.DiscoveryResetHistory[task.IssueID], previous)
				if hasRecord && record.Revision == previous.Revision {
					// Remove only the unapproved proposal. Restore the previous
					// approved revision as the current record, if one exists.
					history := s.SpecHistory[task.IssueID]
					if len(history) == 0 {
						delete(s.Specs, task.IssueID)
					} else {
						s.Specs[task.IssueID] = history[len(history)-1]
						s.SpecHistory[task.IssueID] = history[:len(history)-1]
					}
				}
			}
			task.Generation = previous.Generation + 1
			task.ModelCalls = previous.ModelCalls
			task.CreatedAt = e.now()
			task.UpdatedAt = e.now()
			if task.ModelCalls >= task.MaxModelCalls {
				task.Phase = DiscoveryBlocked
				task.Failure = "budget"
			}
			if !task.valid() {
				return false, fmt.Errorf("%w: revised discovery budget", ErrInvalid)
			}
			s.Discoveries[task.IssueID] = task
			created = true
			return true, nil
		}
		active := 0
		for _, other := range s.Discoveries {
			if other.Repository == task.Repository && (other.Phase == DiscoveryPending || other.Phase == DiscoveryRunning) {
				active++
			}
		}
		if active >= maxActive {
			return false, ErrLimit
		}
		// Legacy delivery attempts have no Discovery revision to bind to this
		// first task. Do not silently coexist with one: a later Ready approval
		// could not prove an authorized supersession or carry its counters.
		for _, attempt := range s.Attempts {
			if attempt.Admission.Repository == task.Repository && attempt.Admission.Issue == task.Issue && attempt.SupersededAt.IsZero() {
				return false, fmt.Errorf("%w: legacy delivery attempt requires operator reconciliation before first Discovery", ErrAdmissionChanged)
			}
		}
		if s.Discoveries == nil {
			s.Discoveries = map[string]DiscoveryTask{}
		}
		s.Discoveries[task.IssueID] = task
		created = true
		return true, nil
	})
	if err != nil {
		created = false
	}
	return
}

type DiscoveryFence struct {
	IssueID    string
	Generation int64
	Owner      Owner
}

// ClaimDiscovery reserves one prompt before invoking the model. Lost work
// still consumes its reservation; a retry cannot inflate the call budget.
// Recovery with a publication intent claims ownership without charging a prompt.
func (e Engine) ClaimDiscovery(ctx context.Context, issueID string, owner Owner) (fence DiscoveryFence, err error) {
	if !reference(issueID) || !validOwner(owner) {
		return fence, fmt.Errorf("%w: discovery owner", ErrInvalid)
	}
	err = e.update(ctx, func(s *State) (bool, error) {
		d, ok := s.Discoveries[issueID]
		if !ok {
			return false, ErrNotFound
		}
		if d.Phase != DiscoveryPending || d.Owner != nil {
			return false, ErrClaimed
		}
		if d.Publication == nil && d.ModelCalls >= d.MaxModelCalls {
			return false, ErrLimit
		}
		if d.Publication == nil {
			d.ModelCalls++
		}
		d.Generation++
		d.Owner = &owner
		d.Phase = DiscoveryRunning
		d.UpdatedAt = e.now()
		s.Discoveries[issueID] = d
		fence = DiscoveryFence{
			IssueID:    issueID,
			Generation: d.Generation,
			Owner:      owner,
		}
		return true, nil
	})
	return
}

// ownedDiscovery returns the running task matching fence, or ErrNotFound if
// absent and ErrStale if its phase, owner, or generation differs.
func ownedDiscovery(s *State, fence DiscoveryFence) (DiscoveryTask, error) {
	d, ok := s.Discoveries[fence.IssueID]
	if !ok {
		return d, ErrNotFound
	}
	if d.Generation != fence.Generation || d.Owner == nil || *d.Owner != fence.Owner || d.Phase != DiscoveryRunning {
		return d, ErrStale
	}
	return d, nil
}

// AssertDiscoveryOwner checks the stored task's running ownership fence without
// changing it. Store errors, ErrNotFound, and ErrStale are returned to the caller.
func (e Engine) AssertDiscoveryOwner(ctx context.Context, fence DiscoveryFence) error {
	snapshot, err := e.Store.Load(ctx)
	if err != nil {
		return err
	}
	_, err = ownedDiscovery(&snapshot.State, fence)
	return err
}

// PrepareDiscoveryPublication records one unapproved comment intent before
// the external POST. The key is a deterministic retry locator; neither it nor
// the pending digest can satisfy Spec Review or Ready approval.
func (e Engine) PrepareDiscoveryPublication(ctx context.Context, fence DiscoveryFence, digest, key string) error {
	intent := DiscoveryPublication{
		Digest:   digest,
		Key:      key,
		Producer: fence.Owner,
	}
	if !intent.valid() {
		return fmt.Errorf("%w: Discovery publication intent", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		d, err := ownedDiscovery(s, fence)
		if err != nil {
			return false, err
		}
		if d.Publication != nil {
			if d.Publication.Digest == digest && d.Publication.Key == key {
				return false, nil
			}
			return false, ErrConflict
		}
		d.Publication = &intent
		d.UpdatedAt = e.now()
		s.Discoveries[d.IssueID] = d
		return true, nil
	})
}

// MarkDiscoveryPostAttempt is persisted before the first POST. On an ambiguous
// transport error, a retry may adopt a verified matching bot comment; it must
// not issue a second POST when the first outcome is unknown.
func (e Engine) MarkDiscoveryPostAttempt(ctx context.Context, fence DiscoveryFence, digest, key string) (first bool, err error) {
	err = e.update(ctx, func(s *State) (bool, error) {
		first = false
		d, err := ownedDiscovery(s, fence)
		if err != nil {
			return false, err
		}
		if d.Publication == nil || d.Publication.Digest != digest || d.Publication.Key != key {
			return false, ErrConflict
		}
		if d.Publication.PostAttempted {
			return false, nil
		}
		d.Publication.PostAttempted = true
		first = true
		d.UpdatedAt = e.now()
		s.Discoveries[d.IssueID] = d
		return true, nil
	})
	if err != nil {
		first = false
	}
	return first, err
}

// CompleteDiscovery records the published comment, enters the ledger's Spec Review
// phase, and releases the worker and intent. The caller must verify the remote comment.
// Invalid metadata yields ErrInvalid; a missing or different publication digest
// yields ErrConflict. Ownership and store errors are propagated.
func (e Engine) CompleteDiscovery(ctx context.Context, fence DiscoveryFence, specDigest string, commentID int64, authorID string, commentCreatedAt, commentUpdatedAt time.Time) error {
	if !digestPattern.MatchString(specDigest) || commentID < 1 || !reference(authorID) || commentCreatedAt.IsZero() || commentUpdatedAt.Before(commentCreatedAt) {
		return fmt.Errorf("%w: specification comment identity", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		d, err := ownedDiscovery(s, fence)
		if err != nil {
			return false, err
		}
		if d.Publication == nil || d.Publication.Digest != specDigest {
			return false, ErrConflict
		}
		d.Phase = DiscoveryReview
		d.Publication = nil
		d.SpecDigest = specDigest
		d.CommentID = commentID
		d.CommentAuthorID = authorID
		d.CommentCreatedAt = commentCreatedAt
		d.CommentUpdatedAt = commentUpdatedAt
		d.Owner = nil
		d.UpdatedAt = e.now()
		s.Discoveries[d.IssueID] = d
		return true, nil
	})
}

// FailDiscovery blocks owned work and releases its worker while preserving call
// usage. kind must be authority, validation, authentication, quota, or infrastructure.
// An existing publication intent must be reconciled first and causes an error;
// invalid kinds, ownership failures, and store errors are returned.
func (e Engine) FailDiscovery(ctx context.Context, fence DiscoveryFence, kind string) error {
	if kind != "authority" && kind != "validation" && kind != "authentication" && kind != "quota" && kind != "infrastructure" {
		return fmt.Errorf("%w: discovery failure", ErrInvalid)
	}
	return e.update(ctx, func(s *State) (bool, error) {
		d, err := ownedDiscovery(s, fence)
		if err != nil {
			return false, err
		}
		if d.Publication != nil {
			return false, errors.New("discovery publication intent requires reconciliation")
		}
		d.Phase = DiscoveryBlocked
		d.Failure = kind
		d.Owner = nil
		d.UpdatedAt = e.now()
		s.Discoveries[d.IssueID] = d
		return true, nil
	})
}

// RecoverDiscovery fences a proven stopped worker and retains consumed calls and
// publication intent. Work becomes pending unless its budget is exhausted without
// an intent, in which case it is blocked. Unproven termination returns ErrActive;
// missing tasks, changed ownership, and store failures are returned as errors.
func (e Engine) RecoverDiscovery(ctx context.Context, issueID string, proof RunProof) error {
	if !proof.terminal(e.now()) {
		return ErrActive
	}
	return e.update(ctx, func(s *State) (bool, error) {
		d, ok := s.Discoveries[issueID]
		if !ok {
			return false, ErrNotFound
		}
		if d.Owner == nil || *d.Owner != proof.Owner || d.Phase != DiscoveryRunning {
			return false, ErrStale
		}
		d.Owner = nil
		d.Generation++
		d.UpdatedAt = e.now()
		if d.Publication == nil && d.ModelCalls >= d.MaxModelCalls {
			d.Phase = DiscoveryBlocked
			d.Failure = "budget"
		} else {
			d.Phase = DiscoveryPending
		}
		s.Discoveries[issueID] = d
		return true, nil
	})
}

// CancelDiscovery cancels unfinished work and invalidates its ownership fence.
// Repeated cancellation is a no-op. Completed work or a publication intent causes
// an error; a missing task yields ErrNotFound, and store errors are propagated.
func (e Engine) CancelDiscovery(ctx context.Context, issueID string) error {
	return e.update(ctx, func(s *State) (bool, error) {
		d, ok := s.Discoveries[issueID]
		if !ok {
			return false, ErrNotFound
		}
		if d.Phase == DiscoveryCanceled {
			return false, nil
		}
		if d.Phase == DiscoveryReview {
			return false, errors.New("completed Discovery cannot be cancelled")
		}
		if d.Publication != nil {
			return false, errors.New("discovery publication intent requires reconciliation before cancellation")
		}
		d.Owner = nil
		d.Generation++
		d.Phase = DiscoveryCanceled
		d.UpdatedAt = e.now()
		s.Discoveries[issueID] = d
		return true, nil
	})
}
