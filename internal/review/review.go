// Package review evaluates owner-authored GitHub review feedback without
// treating its text as authority. A decision binds one existing sofa PR and
// one exact candidate revision; a separate fenced worker applies any repair.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Decision struct {
	AttemptID    string
	Repository   string
	PRNumber     int64
	PRHeadSHA    string
	PRBaseSHA    string
	FeedbackID   string
	FeedbackHash string
	SubmittedAt  time.Time
	FeedbackRef  string
	FeedbackText string // untrusted task data; never a policy or credential grant
}

// RecordLaterFeedback links a post-merge PR comment to the completed attempt
// without treating it as a new repair grant. Only bounded metadata and the
// comment reference enter the ledger; raw comment text remains external data.
func RecordLaterFeedback(ctx context.Context, engine state.Engine, attempt state.Attempt, pull github.PullSnapshot, comment discovery.SpecComment) error {
	if attempt.Publication == nil || pull.Number != attempt.Publication.PRNumber || pull.URL != attempt.Publication.PRURL || !pull.Merged || pull.MergedAt.IsZero() || pull.MergeCommitSHA == "" || comment.ID < 1 || comment.CreatedAt.Before(pull.MergedAt) || len(comment.Body) > 64<<10 {
		return errors.New("later feedback is not bound to the merged PR")
	}
	if !strings.EqualFold(pull.HeadRepository, attempt.Admission.Repository) || !strings.EqualFold(pull.BaseRepository, attempt.Admission.Repository) {
		return errors.New("later feedback repository identity changed")
	}
	h := sha256.Sum256([]byte(comment.Body))
	observation := state.Observation{
		Version:     state.Version,
		ID:          fmt.Sprintf("later-feedback-%s-%d-%s", attempt.ID, comment.ID, hex.EncodeToString(h[:8])),
		AttemptID:   attempt.ID,
		Stage:       "later-feedback",
		Outcome:     "reported",
		Revision:    pull.MergeCommitSHA,
		EvidenceRef: fmt.Sprintf("https://github.com/%s/pull/%d#issuecomment-%d", attempt.Admission.Repository, pull.Number, comment.ID),
		RecordedAt:  comment.UpdatedAt.UTC(),
	}
	for range 2 {
		snapshot, err := engine.Store.Load(ctx)
		if err != nil {
			return err
		}
		for _, prior := range snapshot.State.Observations {
			if prior.ID != observation.ID {
				continue
			}
			if prior.AttemptID == observation.AttemptID && prior.Stage == observation.Stage && prior.Outcome == observation.Outcome && prior.Revision == observation.Revision && prior.EvidenceRef == observation.EvidenceRef {
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

// Evaluate admits only a submitted owner CHANGES_REQUESTED review on the
// currently published PR revision. A comment, bot review, old commit review,
// closed PR, fork, or exhausted ledger cannot start a repair.
func Evaluate(attempt state.Attempt, pull github.PullSnapshot, submitted github.PullReview, ownerID string) (Decision, error) {
	if attempt.Phase != state.Draft || attempt.Publication == nil || attempt.Publication.PRNumber < 1 || attempt.Counts.Repairs >= attempt.Limits.Repairs {
		return Decision{}, errors.New("review repair unavailable for attempt")
	}
	p := attempt.Publication
	repo := attempt.Admission.Repository
	if pull.Number != p.PRNumber || pull.URL != p.PRURL || pull.HeadSHA != p.HeadSHA || pull.HeadRef != p.Branch || !strings.EqualFold(pull.HeadRepository, repo) || !strings.EqualFold(pull.BaseRepository, repo) || pull.State != "open" || pull.Merged || !shaPattern.MatchString(pull.BaseSHA) {
		return Decision{}, errors.New("review PR differs from published attempt")
	}
	if ownerID == "" || submitted.UserID != ownerID || submitted.ID < 1 || submitted.State != "CHANGES_REQUESTED" || submitted.CommitSHA != pull.HeadSHA || submitted.SubmittedAt.IsZero() || len(submitted.Body) > 64<<10 {
		return Decision{}, errors.New("review feedback is not current owner repair authorization")
	}
	h := sha256.Sum256([]byte(submitted.Body))
	id := fmt.Sprintf("review-%d-%d-%s", pull.Number, submitted.ID, hex.EncodeToString(h[:8]))
	return Decision{
		AttemptID:    attempt.ID,
		Repository:   repo,
		PRNumber:     pull.Number,
		PRHeadSHA:    pull.HeadSHA,
		PRBaseSHA:    pull.BaseSHA,
		FeedbackID:   id,
		FeedbackHash: hex.EncodeToString(h[:]),
		SubmittedAt:  submitted.SubmittedAt,
		FeedbackRef:  fmt.Sprintf("https://github.com/%s/pull/%d#pullrequestreview-%d", repo, pull.Number, submitted.ID),
		FeedbackText: submitted.Body,
	}, nil
}
