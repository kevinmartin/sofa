package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/state"
)

func TestAdmitReplaysOnlyExactLegacyAttempt(t *testing.T) {
	ctx := context.Background()
	engine := state.Engine{Store: &state.MemoryStore{}}
	authority := state.Admission{
		Repository:      "owner/repo",
		Issue:           1,
		SpecDigest:      strings.Repeat("a", 64),
		ConfigDigest:    strings.Repeat("b", 64),
		BaseSHA:         strings.Repeat("c", 40),
		ProjectID:       "P_1",
		ProjectItemID:   "PVTI_1",
		StatusOptionID:  "ready-option",
		StatusUpdatedAt: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
	}
	legacy := state.Limits{
		ModelCalls:            2,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        1200,
	}
	current := legacy
	current.ModelCalls = 3
	current.RuntimeSeconds = 1800
	old, created, err := engine.Admit(ctx, authority, legacy)
	if err != nil || !created {
		t.Fatalf("legacy admission: %+v, %t, %v", old, created, err)
	}
	replayed, created, err := admitWithLegacyLimits(ctx, engine, authority, current, legacy)
	if err != nil || created || replayed.ID != old.ID || replayed.Limits != legacy {
		t.Fatalf("legacy replay changed admission: %+v, %t, %v", replayed, created, err)
	}
	changed := authority
	changed.BaseSHA = strings.Repeat("d", 40)
	if _, _, err := admitWithLegacyLimits(ctx, engine, changed, current, legacy); !errors.Is(err, state.ErrAdmissionChanged) {
		t.Fatalf("changed admission borrowed legacy limits: %v", err)
	}
	fresh := authority
	fresh.Issue = 2
	fresh.ProjectItemID = "PVTI_2"
	newAttempt, created, err := admitWithLegacyLimits(ctx, engine, fresh, current, legacy)
	if err != nil || !created || newAttempt.Limits != current {
		t.Fatalf("new attempt did not use current limits: %+v, %t, %v", newAttempt, created, err)
	}
}

func TestPublicationRequiresRecordedFactoryStage(t *testing.T) {
	ctx := context.Background()
	store := &state.MemoryStore{}
	c := config.Config{
		Repository:  "owner/repo",
		ReadyStatus: "Ready",
		Lifecycle:   &config.Lifecycle{Statuses: map[string]string{"ready": "Ready", "building": "Building"}},
	}
	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ledger := state.Empty()
	ledger.Projections["I_1"] = state.BoardProjection{
		Repository:           "owner/repo",
		IssueID:              "I_1",
		ProjectID:            "P_1",
		ProjectItemID:        "PVTI_1",
		Stage:                "ready",
		OptionID:             "ready-option",
		UpdatedAt:            when,
		PendingStage:         "building",
		PendingOptionID:      "building-option",
		PendingFromOptionID:  "ready-option",
		PendingFromUpdatedAt: when,
	}
	if err := store.CompareAndSwap(ctx, "", ledger); err != nil {
		t.Fatal(err)
	}
	issue := admission.Snapshot{
		IssueID:         "I_1",
		ProjectID:       "P_1",
		ProjectItemID:   "PVTI_1",
		CurrentStatus:   "Building",
		StatusOptionID:  "building-option",
		StatusUpdatedAt: when.Add(time.Minute),
	}
	if err := publicationStageCurrent(ctx, store, c, issue); err != nil {
		t.Fatalf("factory pending stage was rejected: %v", err)
	}
	issue.StatusOptionID = "other-option"
	if err := publicationStageCurrent(ctx, store, c, issue); err == nil {
		t.Fatal("unrecorded Project status was accepted")
	}
}
