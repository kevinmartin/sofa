package main

import (
	"context"
	"errors"
	"time"

	"github.com/kevinmartin/sofa/internal/state"
)

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
