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
// alone never reaches this method. Existing tasks are replayed, not reset.
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
			if previous.Repository != task.Repository || previous.Issue != task.Issue || previous.ProjectID != task.ProjectID || previous.ProjectItemID != task.ProjectItemID || previous.StatusOptionID != task.StatusOptionID || !previous.StatusUpdatedAt.Equal(task.StatusUpdatedAt) || previous.SourceDigest != task.SourceDigest || previous.MaxModelCalls != task.MaxModelCalls {
				return false, ErrAdmissionChanged
			}
			task = previous
			return false, nil
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
		fence = DiscoveryFence{IssueID: issueID, Generation: d.Generation, Owner: owner}
		return true, nil
	})
	return
}

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
	intent := DiscoveryPublication{Digest: digest, Key: key, Producer: fence.Owner}
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
