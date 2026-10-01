package lifecycle

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/state"
)

type EffectKind string

const (
	Observe EffectKind = "observe"
	Move    EffectKind = "move"
	Hold    EffectKind = "hold"
)

// DeliveryEvidence is gathered independently for one PR. Its identity must
// match that issue's publication before any status can advance.
type DeliveryEvidence struct {
	PRNumber        int64
	PRURL           string
	HeadSHA         string
	BaseSHA         string
	Closed          bool
	Merged          bool
	MergedSHA       string
	Gates           []GateEvidence
	RequiredGates   []string
	ReleaseRequired bool
	ReleasePassed   bool
	ReleaseSHA      string
	AllowFixture    bool // replay only; never set by a hosted controller
}

type ScanInput struct {
	Repository string
	ProjectID  string
	Statuses   Statuses
	Issues     []admission.Snapshot
	Ledger     state.State
	Authority  map[string]bool             // exact issue ID; caller verifies Discovery approval
	Evidence   map[string]DeliveryEvidence // exact issue ID, no fallback to another PR
}

type Effect struct {
	Kind          EffectKind
	IssueID       string
	IssueNumber   int
	From          Stage
	To            Stage
	PRFence       *PRMoveFence
	OptionID      string
	UpdatedAt     time.Time
	BlockedReason string
	NextAction    string
	Conflict      bool
	RetryIntent   bool
}

// PRMoveFence binds a proposed board move to the PR observed during the scan.
// The writer must re-read this identity immediately before changing Projects.
type PRMoveFence struct {
	Number         int64
	URL            string
	HeadSHA        string
	BaseSHA        string
	Closed         bool
	Merged         bool
	MergeCommitSHA string
}

// Scan computes bounded effects from one complete Project poll. It never
// performs a write. Unknown statuses, mismatched publication identities, or
// changed manual board positions become holds scoped to that issue.
func Scan(input ScanInput) ([]Effect, error) {
	if input.Repository == "" || input.ProjectID == "" || input.Statuses.Validate() != nil || input.Ledger.Validate() != nil || len(input.Issues) > 2000 {
		return nil, errors.New("invalid lifecycle scan inputs")
	}
	attempts := make(map[string]state.Attempt)
	ambiguousItems := make(map[string]bool)
	for _, attempt := range input.Ledger.Attempts {
		if !attempt.SupersededAt.IsZero() {
			continue
		}
		key := attempt.Admission.ProjectItemID
		if _, exists := attempts[key]; exists {
			ambiguousItems[key] = true
			continue
		}
		attempts[key] = attempt
	}
	effects := make([]Effect, 0, len(input.Issues))
	seen := make(map[string]bool, len(input.Issues))
	for _, issue := range input.Issues {
		if !issue.Complete || !issue.ProjectPrivate || !strings.EqualFold(issue.Repository, input.Repository) || issue.ProjectID != input.ProjectID || issue.IssueID == "" || issue.ProjectItemID == "" || issue.Number < 1 || issue.StatusOptionID == "" || issue.StatusUpdatedAt.IsZero() || seen[issue.IssueID] {
			return nil, errors.New("incomplete or duplicate Project issue in scan")
		}
		seen[issue.IssueID] = true
		stage, known := input.Statuses.StageFor(issue.CurrentStatus)
		effect := Effect{
			Kind:        Hold,
			IssueID:     issue.IssueID,
			IssueNumber: issue.Number,
			From:        stage,
			OptionID:    issue.StatusOptionID,
			UpdatedAt:   issue.StatusUpdatedAt,
		}
		if !known {
			effect.BlockedReason = "unknown Project status"
			effect.NextAction = "update trusted status mapping"
			effects = append(effects, effect)
			continue
		}
		prior, exists := input.Ledger.Projections[issue.IssueID]
		if !exists {
			effect.Kind = Observe
			effects = append(effects, effect)
			continue
		}
		if !strings.EqualFold(prior.Repository, input.Repository) || prior.ProjectID != input.ProjectID || prior.ProjectItemID != issue.ProjectItemID {
			effect.BlockedReason = "Project item identity changed"
			effect.Conflict = true
			effects = append(effects, effect)
			continue
		}
		pendingRetry := false
		if prior.PendingStage != "" {
			if string(stage) == prior.PendingStage && issue.StatusOptionID == prior.PendingOptionID && issue.StatusUpdatedAt.After(prior.PendingFromUpdatedAt) {
				effect.Kind = Observe
			} else if string(stage) == prior.Stage && issue.StatusOptionID == prior.OptionID && issue.StatusUpdatedAt.Equal(prior.UpdatedAt) {
				pendingRetry = true
			} else {
				effect.BlockedReason = "Project changed during pending transition"
				effect.Conflict = true
			}
			if !pendingRetry {
				effects = append(effects, effect)
				continue
			}
		}
		if prior.Stage != string(stage) || prior.OptionID != issue.StatusOptionID || !prior.UpdatedAt.Equal(issue.StatusUpdatedAt) {
			effect.Kind = Observe // Engine accepts owner moves; rejects other edits.
			effects = append(effects, effect)
			continue
		}
		if stage != Ready && stage != Building && stage != Verification && stage != Review && stage != Release {
			effects = append(effects, effect)
			continue
		}
		if ambiguousItems[issue.ProjectItemID] {
			effect.BlockedReason = "ambiguous attempts for Project item"
			effect.NextAction = "inspect superseded task history"
			effects = append(effects, effect)
			continue
		}
		attempt, exists := attempts[issue.ProjectItemID]
		if !exists || attempt.Admission.Issue != int64(issue.Number) || attempt.Admission.ProjectID != issue.ProjectID || !strings.EqualFold(attempt.Admission.Repository, input.Repository) {
			effect.BlockedReason = "delivery attempt unavailable"
			effects = append(effects, effect)
			continue
		}
		record, recordExists := input.Ledger.Specs[issue.IssueID]
		if !input.Authority[issue.IssueID] || !recordExists || record.ApprovedDigest == "" || record.Issue != int64(issue.Number) || record.ProjectItemID != issue.ProjectItemID || record.ProjectID != issue.ProjectID || !strings.EqualFold(record.Repository, input.Repository) || attempt.Admission.SpecDigest != record.ApprovedDigest || attempt.SpecRevision != record.Revision || !attempt.Admission.StatusUpdatedAt.After(record.BacklogUpdatedAt) || attempt.Admission.ProjectItemID != issue.ProjectItemID || attempt.Admission.ProjectID != issue.ProjectID {
			effect.BlockedReason = "approved scope unavailable or changed"
			effects = append(effects, effect)
			continue
		}
		_, sourceDigest, sourceErr := admission.CanonicalSpec(issue.Title, issue.Body)
		if sourceErr != nil || sourceDigest != record.SourceDigest || !issue.IssueLastEditedAt.IsZero() && !record.BacklogUpdatedAt.After(issue.IssueLastEditedAt) {
			effect.BlockedReason = "source idea changed after approval"
			effect.NextAction = "return to Spec Review for revised scope"
			effects = append(effects, effect)
			continue
		}
		// A terminal attempt remains in its observed board stage. Only the
		// validated phase, never the untrusted failure detail, reaches advice.
		if attempt.Phase == state.Blocked || attempt.Phase == state.Deferred {
			effect.BlockedReason = string(attempt.Phase)
			effect.NextAction = "inspect bounded delivery outcome"
			effects = append(effects, effect)
			continue
		}
		if attempt.Phase != state.Draft || attempt.Publication == nil || attempt.Publication.PRNumber < 1 {
			effect.BlockedReason = "candidate publication pending or blocked"
			effects = append(effects, effect)
			continue
		}
		observed, ok := input.Evidence[issue.IssueID]
		if !ok || observed.PRNumber != attempt.Publication.PRNumber || observed.PRURL != attempt.Publication.PRURL || observed.HeadSHA == "" || observed.BaseSHA == "" {
			effect.BlockedReason = "current PR identity unavailable"
			effects = append(effects, effect)
			continue
		}
		if !issue.Open && !observed.Merged {
			effect.BlockedReason = "source issue closed before merge"
			effect.NextAction = "inspect issue closure or restore owner authority"
			effects = append(effects, effect)
			continue
		}
		projected := Projection{
			Current:         stage,
			LastProjected:   stage,
			Authorized:      true,
			Published:       true,
			CandidateSHA:    attempt.Publication.HeadSHA,
			PRHeadSHA:       observed.HeadSHA,
			PRBaseSHA:       observed.BaseSHA,
			PRExists:        true,
			PRClosed:        observed.Closed,
			PRMerged:        observed.Merged,
			RequiredGates:   observed.RequiredGates,
			GateEvidence:    observed.Gates,
			ReleaseRequired: observed.ReleaseRequired,
			ReleasePassed:   observed.ReleasePassed,
			ReleaseSHA:      observed.ReleaseSHA,
			MergedSHA:       observed.MergedSHA,
			AllowFixture:    observed.AllowFixture,
		}
		decision := ProjectDecision(projected)
		effect.BlockedReason = decision.BlockedReason
		effect.NextAction = decision.NextAction
		effect.Conflict = decision.Conflict
		if pendingRetry && decision.MoveTo == "" {
			effect.BlockedReason = "pending transition no longer justified"
			effect.NextAction = "inspect changed PR and pending move"
			effect.Conflict = true
		}
		if decision.MoveTo != "" {
			if pendingRetry && string(decision.MoveTo) != prior.PendingStage {
				effect.BlockedReason = "pending transition no longer justified"
				effect.Conflict = true
			} else {
				effect.Kind = Move
				effect.To = decision.MoveTo
				effect.RetryIntent = pendingRetry
				effect.PRFence = &PRMoveFence{
					Number:         observed.PRNumber,
					URL:            observed.PRURL,
					HeadSHA:        observed.HeadSHA,
					BaseSHA:        observed.BaseSHA,
					Closed:         observed.Closed,
					Merged:         observed.Merged,
					MergeCommitSHA: observed.MergedSHA,
				}
			}
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

// String summarizes the issue number, effect kind, and proposed stage transition.
func (e Effect) String() string {
	return fmt.Sprintf("issue #%d: %s %s→%s", e.IssueNumber, e.Kind, e.From, e.To)
}
