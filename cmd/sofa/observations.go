package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kevinmartin/sofa/internal/state"
)

// Execution can be retried under a later fence generation with a new digest.
// Each generation gets its own immutable execution and verification records.
func observeCandidate(ctx context.Context, engine state.Engine, fence state.Fence, digest string) error {
	scope := fmt.Sprintf("g%d", fence.Generation)
	if err := observeOnce(ctx, engine, fence.AttemptID, "execution", "candidate", digest, "", scope); err != nil {
		return err
	}
	return observeOnce(ctx, engine, fence.AttemptID, "verification", "passed", digest, "", scope)
}

func observeOnce(ctx context.Context, engine state.Engine, attemptID, stage, outcome, revision, evidenceRef, scope string) error {
	id := stage + "-" + attemptID
	if scope != "" {
		id += "-" + scope
	}
	lookup := func() (bool, error) {
		snapshot, err := engine.Store.Load(ctx)
		if err != nil {
			return false, err
		}
		for _, prior := range snapshot.State.Observations {
			if prior.ID == id {
				if prior.AttemptID == attemptID && prior.Stage == stage && prior.Outcome == outcome && prior.Revision == revision && prior.EvidenceRef == evidenceRef {
					return true, nil
				}
				return false, state.ErrConflict
			}
		}
		return false, nil
	}
	if found, err := lookup(); err != nil || found {
		return err
	}
	err := engine.Observe(ctx, state.Observation{
		Version:     state.Version,
		ID:          id,
		AttemptID:   attemptID,
		Stage:       stage,
		Outcome:     outcome,
		Revision:    revision,
		EvidenceRef: evidenceRef,
		RecordedAt:  time.Now().UTC(),
	})
	if errors.Is(err, state.ErrConflict) {
		if found, currentErr := lookup(); currentErr != nil || found {
			return currentErr
		}
	}
	return err
}
