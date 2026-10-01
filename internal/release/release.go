// Package release reconciles an admitted pull request with exact-commit,
// configured post-merge evidence. It only observes existing release activity.
package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

var ErrUnverifiedRevert = errors.New("revert evidence does not prove exact inverse")

type RequiredCheck struct {
	Name  string
	AppID int64
}

type Decision struct {
	AttemptID   string
	PRNumber    int64
	Kind        string // review, release-blocked, release-failed, done, closed-unmerged
	CommitSHA   string
	EvidenceRef string
	EventID     string
}

type RevertVerifier interface {
	VerifiedRevert(context.Context, string, string, string, string) (bool, error)
}

// ObserveRevert appends a linked correction only after a trusted source proves
// the new default-branch commit exactly reverses the merged file changes.
// Partial or ambiguous reversions remain unclassified for human review.
// A negative verification returns ErrUnverifiedRevert; verifier and ledger
// errors propagate, and invalid PR or commit identity returns an error.
func ObserveRevert(ctx context.Context, verifier RevertVerifier, engine state.Engine, attempt state.Attempt, pull github.PullSnapshot, revertSHA, defaultHead string, observedAt time.Time) error {
	if verifier == nil || attempt.Publication == nil || pull.Number != attempt.Publication.PRNumber || pull.URL != attempt.Publication.PRURL || !pull.Merged || pull.HeadSHA != attempt.Publication.HeadSHA || !shaPattern.MatchString(pull.MergeCommitSHA) || !shaPattern.MatchString(revertSHA) || !shaPattern.MatchString(defaultHead) {
		return errors.New("revert is not bound to completed PR")
	}
	verified, err := verifier.VerifiedRevert(ctx, attempt.Admission.Repository, pull.MergeCommitSHA, revertSHA, defaultHead)
	if err != nil {
		return err
	}
	if !verified {
		return ErrUnverifiedRevert
	}
	h := sha256.Sum256([]byte(attempt.ID))
	return Record(ctx, engine, Decision{
		AttemptID:   attempt.ID,
		PRNumber:    pull.Number,
		Kind:        "reverted",
		CommitSHA:   revertSHA,
		EvidenceRef: fmt.Sprintf("https://github.com/%s/commit/%s", attempt.Admission.Repository, revertSHA),
		EventID:     fmt.Sprintf("revert-%s-%s", hex.EncodeToString(h[:8]), revertSHA),
	}, observedAt)
}

// EvaluateDesignated observes an already-merged test PR without fabricating a
// sofa attempt or writing to a Project. The expected merge SHA is supplied by
// the operator and compared with GitHub's current immutable PR result.
func EvaluateDesignated(repository string, pull github.PullSnapshot, expectedMergeSHA, defaultHead string, mergedCommitOnDefault bool, required []RequiredCheck, checks []github.CommitCheck) (Decision, error) {
	if repository == "" || !strings.EqualFold(pull.HeadRepository, repository) || !strings.EqualFold(pull.BaseRepository, repository) || !shaPattern.MatchString(defaultHead) || pull.Number < 1 {
		return Decision{}, errors.New("designated release repository identity unavailable")
	}
	d := Decision{
		PRNumber:    pull.Number,
		Kind:        "review",
		CommitSHA:   pull.HeadSHA,
		EvidenceRef: pull.URL,
	}
	if pull.State == "open" && !pull.Merged {
		return d, nil
	}
	if pull.State == "closed" && !pull.Merged {
		d.Kind = "closed-unmerged"
		return d, nil
	}
	if pull.State != "closed" || !pull.Merged || !shaPattern.MatchString(expectedMergeSHA) || pull.MergeCommitSHA != expectedMergeSHA {
		return Decision{}, errors.New("designated merge commit differs from GitHub PR")
	}
	d.CommitSHA = expectedMergeSHA
	if !mergedCommitOnDefault {
		d.Kind = "release-blocked"
		return d, nil
	}
	outcome, err := evaluateChecks(required, checks)
	if err != nil {
		return Decision{}, err
	}
	d.Kind = outcome
	return d, nil
}

// Evaluate binds the current PR to the ledger before reading check results.
// The caller must obtain defaultHead and ancestor evidence from the trusted
// repository, then pass checks fetched for pull.MergeCommitSHA only. A merge
// with missing required checks remains in Release, not Done.
func Evaluate(attempt state.Attempt, pull github.PullSnapshot, defaultHead string, mergedCommitOnDefault bool, required []RequiredCheck, checks []github.CommitCheck) (Decision, error) {
	if attempt.Publication == nil || attempt.Publication.PRNumber < 1 {
		return Decision{}, errors.New("attempt has no published PR")
	}
	p := attempt.Publication
	repo := attempt.Admission.Repository
	if attempt.ID == "" || pull.Number != p.PRNumber || pull.URL != p.PRURL || !shaPattern.MatchString(pull.HeadSHA) || pull.HeadSHA != p.HeadSHA || pull.HeadRef != p.Branch || !strings.EqualFold(pull.HeadRepository, repo) || !strings.EqualFold(pull.BaseRepository, repo) {
		return Decision{}, errors.New("release PR differs from published attempt")
	}
	if !shaPattern.MatchString(defaultHead) {
		return Decision{}, errors.New("default branch commit unavailable")
	}
	d := Decision{
		AttemptID:   attempt.ID,
		PRNumber:    pull.Number,
		Kind:        "review",
		CommitSHA:   pull.HeadSHA,
		EvidenceRef: pull.URL,
	}
	switch {
	case pull.State == "open" && !pull.Merged:
		// No release event until a terminal PR outcome is observed.
		return d, nil
	case pull.State == "closed" && !pull.Merged:
		d.Kind = "closed-unmerged"
	case pull.State == "closed" && pull.Merged:
		if !shaPattern.MatchString(pull.MergeCommitSHA) {
			return Decision{}, errors.New("merged commit unavailable")
		}
		d.CommitSHA = pull.MergeCommitSHA
		if !mergedCommitOnDefault {
			d.Kind = "release-blocked"
			break
		}
		outcome, err := evaluateChecks(required, checks)
		if err != nil {
			return Decision{}, err
		}
		d.Kind = outcome
	default:
		return Decision{}, errors.New("pull request state unavailable")
	}
	// The event identity is based on the exact observed outcome. Later changes to
	// required checks append a correction rather than rewriting earlier events.
	var parts []string
	for _, check := range checks {
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%s:%s", check.Name, check.AppID, check.SourceID, check.State, check.UpdatedAt.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(parts)
	h := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	attemptHash := sha256.Sum256([]byte(attempt.ID))
	d.EventID = fmt.Sprintf("release-%s-%s-%s-%s", hex.EncodeToString(attemptHash[:8]), d.CommitSHA[:16], d.Kind, hex.EncodeToString(h[:8]))
	return d, nil
}

// evaluateChecks classifies the latest check for each required name and App ID,
// breaking timestamp ties by larger source ID. Failure takes precedence over
// missing or pending evidence. An empty plan returns done; an invalid or
// duplicate requirement or more than 20 requirements returns an error.
func evaluateChecks(required []RequiredCheck, checks []github.CommitCheck) (string, error) {
	if len(required) > 20 {
		return "", errors.New("too many required release checks")
	}
	seen := map[RequiredCheck]bool{}
	blocked := false
	failed := false
	for _, requirement := range required {
		if requirement.Name == "" || len(requirement.Name) > 200 || strings.ContainsAny(requirement.Name, "\r\n\x00") || requirement.AppID < 1 || seen[requirement] {
			return "", errors.New("invalid required release check")
		}
		seen[requirement] = true
		var latest *github.CommitCheck
		for i := range checks {
			check := &checks[i]
			if check.Name != requirement.Name || check.AppID != requirement.AppID || check.SourceID < 1 || check.UpdatedAt.IsZero() {
				continue
			}
			if latest == nil || check.UpdatedAt.After(latest.UpdatedAt) || (check.UpdatedAt.Equal(latest.UpdatedAt) && check.SourceID > latest.SourceID) {
				latest = check
			}
		}
		if latest == nil {
			blocked = true
			continue
		}
		switch latest.State {
		case "success":
		case "queued", "in_progress", "requested", "waiting", "pending":
			blocked = true
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
			failed = true
		default:
			blocked = true
		}
	}
	if failed {
		return "release-failed", nil
	}
	if blocked {
		return "release-blocked", nil
	}
	return "done", nil
}

// Record appends an independently observed outcome to the existing attempt.
// It never changes a completed event, moves a board item, or deploys software.
func Record(ctx context.Context, engine state.Engine, decision Decision, observedAt time.Time) error {
	if decision.Kind == "review" {
		return nil
	}
	if decision.AttemptID == "" || decision.EventID == "" || !shaPattern.MatchString(decision.CommitSHA) || observedAt.IsZero() {
		return errors.New("invalid release observation")
	}
	observation := state.Observation{
		Version:     state.Version,
		ID:          decision.EventID,
		AttemptID:   decision.AttemptID,
		Stage:       "release",
		Outcome:     decision.Kind,
		Revision:    decision.CommitSHA,
		EvidenceRef: decision.EvidenceRef,
		RecordedAt:  observedAt.UTC(),
	}
	// Preserve the first observation time across duplicate scans. A changed
	// outcome has a new event ID and is appended as a correction.
	for range 2 {
		snapshot, err := engine.Store.Load(ctx)
		if err != nil {
			return err
		}
		for _, previous := range snapshot.State.Observations {
			if previous.ID != observation.ID {
				continue
			}
			if previous.AttemptID == observation.AttemptID && previous.Stage == observation.Stage && previous.Outcome == observation.Outcome && previous.Revision == observation.Revision && previous.EvidenceRef == observation.EvidenceRef {
				return nil
			}
			return state.ErrConflict
		}
		if err := engine.Observe(ctx, observation); !errors.Is(err, state.ErrConflict) {
			return err
		}
	}
	return state.ErrConflict
}
