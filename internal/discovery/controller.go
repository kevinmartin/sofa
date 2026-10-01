package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/state"
)

type CommentReader interface {
	IssueComment(context.Context, string, int64, int64) (SpecComment, error)
}

type SpecStore interface {
	state.Store
	SaveSpec(context.Context, string, string, []byte) error
}

// ApprovedSnapshot replaces the idea body with the exact approved comment
// body for the existing milestone-01 admission/revalidation contract. It must
// be called on every trusted gate before delivery or publication.
func ApprovedSnapshot(ctx context.Context, reader CommentReader, store state.Store, policy Policy, source admission.Snapshot) (admission.Snapshot, error) {
	approved, err := VerifyApprovedRevision(ctx, reader, store, policy, source)
	if err != nil {
		return admission.Snapshot{}, err
	}
	ledger, err := store.Load(ctx)
	if err != nil {
		return admission.Snapshot{}, err
	}
	record := ledger.State.Specs[source.IssueID]
	comment, err := reader.IssueComment(ctx, policy.Repository, int64(source.Number), record.CommentID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	if err := ReadyApproved(policy, source, comment, record); err != nil {
		return admission.Snapshot{}, err
	}
	approved.Body = comment.Body
	return approved, nil
}

// VerifyApprovedRevision checks the source idea and exact approved comment
// after the board has advanced beyond Ready. It does not itself authorize a
// new delivery attempt or claim that the current lifecycle stage is Ready.
func VerifyApprovedRevision(ctx context.Context, reader CommentReader, store state.Store, policy Policy, source admission.Snapshot) (admission.Snapshot, error) {
	if reader == nil || store == nil {
		return admission.Snapshot{}, errors.New("approved specification source unavailable")
	}
	ledger, err := store.Load(ctx)
	if err != nil {
		return admission.Snapshot{}, err
	}
	record, ok := ledger.State.Specs[source.IssueID]
	if !ok {
		return admission.Snapshot{}, ErrAuthority
	}
	if !policy.valid() || !source.Complete || !strings.EqualFold(source.Repository, policy.Repository) || source.RepositoryID != policy.RepositoryID || source.ProjectID != policy.ProjectID || !source.ProjectPrivate || !policy.matches(source, record) || record.ApprovedDigest == "" || record.ApprovedDigest != record.SpecDigest {
		return admission.Snapshot{}, ErrAuthority
	}
	comment, err := reader.IssueComment(ctx, policy.Repository, int64(source.Number), record.CommentID)
	if err != nil {
		return admission.Snapshot{}, err
	}
	if !currentRevision(source, comment, record) || (!source.IssueLastEditedAt.IsZero() && !record.BacklogUpdatedAt.After(source.IssueLastEditedAt)) {
		return admission.Snapshot{}, ErrRevision
	}
	source.Body = comment.Body
	return source, nil
}

// ObserveSpecReview persists the exact comment snapshot before ledgering it.
// A failed snapshot write cannot leave an apparently reviewable approval.
// The boolean reports a committed ledger change. Comment, validation, and store
// errors propagate; 32 conflicting write attempts return state.ErrConflict.
func ObserveSpecReview(ctx context.Context, reader CommentReader, store SpecStore, policy Policy, source admission.Snapshot) (state.SpecRecord, bool, error) {
	if reader == nil || store == nil {
		return state.SpecRecord{}, false, ErrAuthority
	}
	for range 32 {
		ledger, err := store.Load(ctx)
		if err != nil {
			return state.SpecRecord{}, false, err
		}
		task, ok := ledger.State.Discoveries[source.IssueID]
		if !ok {
			return state.SpecRecord{}, false, ErrAuthority
		}
		comment, err := reader.IssueComment(ctx, policy.Repository, int64(source.Number), task.CommentID)
		if err != nil {
			return state.SpecRecord{}, false, err
		}
		var previous *state.SpecRecord
		if value, ok := ledger.State.Specs[source.IssueID]; ok {
			previous = &value
		}
		record, canonical, changed, err := CaptureReview(policy, source, comment, task, previous)
		if err != nil || !changed {
			return record, false, err
		}
		if err := store.SaveSpec(ctx, record.IssueID, record.SpecDigest, canonical); err != nil {
			return state.SpecRecord{}, false, fmt.Errorf("store immutable specification: %w", err)
		}
		if ledger.State.Specs == nil {
			ledger.State.Specs = map[string]state.SpecRecord{}
		}
		if previous != nil {
			if ledger.State.SpecHistory == nil {
				ledger.State.SpecHistory = map[string][]state.SpecRecord{}
			}
			ledger.State.SpecHistory[source.IssueID] = append(ledger.State.SpecHistory[source.IssueID], *previous)
		}
		ledger.State.Specs[source.IssueID] = record
		err = store.CompareAndSwap(ctx, ledger.Revision, ledger.State)
		if errors.Is(err, state.ErrConflict) {
			continue
		}
		return record, err == nil, err
	}
	return state.SpecRecord{}, false, state.ErrConflict
}

// ObserveBacklog records an owner approval only after observing the trusted
// Project's Backlog transition and re-reading the same unedited comment.
// The boolean reports a committed approval. Comment, validation, and store
// errors propagate; 32 conflicting write attempts return state.ErrConflict.
func ObserveBacklog(ctx context.Context, reader CommentReader, store state.Store, policy Policy, source admission.Snapshot) (state.SpecRecord, bool, error) {
	if reader == nil || store == nil {
		return state.SpecRecord{}, false, ErrAuthority
	}
	for range 32 {
		ledger, err := store.Load(ctx)
		if err != nil {
			return state.SpecRecord{}, false, err
		}
		previous, ok := ledger.State.Specs[source.IssueID]
		if !ok {
			return state.SpecRecord{}, false, ErrAuthority
		}
		comment, err := reader.IssueComment(ctx, policy.Repository, int64(source.Number), previous.CommentID)
		if err != nil {
			return state.SpecRecord{}, false, err
		}
		record, changed, err := ApproveBacklog(policy, source, comment, previous)
		if err != nil || !changed {
			return record, false, err
		}
		ledger.State.Specs[source.IssueID] = record
		err = store.CompareAndSwap(ctx, ledger.Revision, ledger.State)
		if errors.Is(err, state.ErrConflict) {
			continue
		}
		return record, err == nil, err
	}
	return state.SpecRecord{}, false, state.ErrConflict
}
