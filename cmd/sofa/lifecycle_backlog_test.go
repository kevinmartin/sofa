package main

import (
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/lifecycle"
	"github.com/kevinmartin/sofa/internal/state"
)

func TestBacklogSourceChangesRequiresMatchingRecordedIdentityAndChangedContent(t *testing.T) {
	const repository = "owner/repo"
	const project = "project-1"
	_, sourceDigest, err := admission.CanonicalSpec("Greeting", "Investigate greeting behavior")
	if err != nil {
		t.Fatal(err)
	}
	base := github.ProjectWorkItem{
		Issue: admission.Snapshot{
			Repository:    repository,
			IssueID:       "issue-7",
			Number:        7,
			Title:         "Greeting",
			Body:          "Investigate greeting behavior",
			ProjectID:     project,
			ProjectItemID: "item-7",
			CurrentStatus: "Backlog",
		},
	}
	record := state.SpecRecord{
		Repository:    repository,
		IssueID:       base.Issue.IssueID,
		Issue:         int64(base.Issue.Number),
		ProjectID:     project,
		ProjectItemID: base.Issue.ProjectItemID,
		SourceDigest:  sourceDigest,
	}
	statuses := lifecycle.Statuses{
		lifecycle.Backlog: "Backlog",
		lifecycle.Ready:   "Ready",
	}
	policy := config.Config{
		Repository: repository,
		ProjectID:  project,
	}
	for _, tc := range []struct {
		name   string
		item   github.ProjectWorkItem
		record *state.SpecRecord
		want   bool
	}{
		{
			name:   "unchanged",
			item:   base,
			record: &record,
		},
		{
			name: "boundary whitespace does not change approved content",
			item: func() github.ProjectWorkItem {
				item := base
				item.Issue.Body = "\n Investigate greeting behavior \n"
				return item
			}(),
			record: &record,
		},
		{
			name: "changed body",
			item: func() github.ProjectWorkItem {
				item := base
				item.Issue.Body = "Investigate a different greeting"
				return item
			}(),
			record: &record,
			want:   true,
		},
		{
			name: "invalid empty source",
			item: func() github.ProjectWorkItem {
				item := base
				item.Issue.Body = "   "
				return item
			}(),
			record: &record,
			want:   true,
		},
		{
			name: "invalid oversized source",
			item: func() github.ProjectWorkItem {
				item := base
				item.Issue.Body = strings.Repeat("x", (64<<10)+1)
				return item
			}(),
			record: &record,
			want:   true,
		},
		{
			name: "wrong repository record cannot authorize advice",
			item: base,
			record: func() *state.SpecRecord {
				changed := record
				changed.Repository = "other/repo"
				return &changed
			}(),
		},
		{
			name: "wrong issue number record cannot authorize advice",
			item: base,
			record: func() *state.SpecRecord {
				changed := record
				changed.Issue = 8
				return &changed
			}(),
		},
		{
			name: "wrong Project item record cannot authorize advice",
			item: base,
			record: func() *state.SpecRecord {
				changed := record
				changed.ProjectItemID = "item-8"
				return &changed
			}(),
		},
		{
			name: "wrong Project record cannot authorize advice",
			item: base,
			record: func() *state.SpecRecord {
				changed := record
				changed.ProjectID = "project-2"
				return &changed
			}(),
		},
		{
			name: "missing recorded proposal",
			item: base,
		},
		{
			name: "non-Backlog issue",
			item: func() github.ProjectWorkItem {
				item := base
				item.Issue.Body = "Changed after admission"
				item.Issue.CurrentStatus = "Ready"
				return item
			}(),
			record: &record,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := make(map[string]state.SpecRecord)
			if tc.record != nil {
				specs[base.Issue.IssueID] = *tc.record
			}
			got := backlogSourceChanges(policy, []github.ProjectWorkItem{tc.item}, specs, statuses)
			if got[base.Issue.IssueID] != tc.want || len(got) != boolCount(tc.want) {
				t.Fatalf("changed Backlog issues = %v, want issue changed=%t", got, tc.want)
			}
		})
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestBacklogOwnerFieldAdviceReportsOnlyActionableMetadata(t *testing.T) {
	base := github.ProjectWorkItem{
		Issue: admission.Snapshot{
			Number: 7,
		},
		DependenciesKnown: true,
		PriorityKnown:     true,
	}
	for _, tc := range []struct {
		name   string
		item   github.ProjectWorkItem
		stages map[int]lifecycle.Stage
		reason string
	}{
		{
			name:   "no dependencies and valid priority",
			item:   base,
			stages: map[int]lifecycle.Stage{},
		},
		{
			name: "missing dependencies field",
			item: func() github.ProjectWorkItem {
				item := base
				item.DependenciesKnown = false
				return item
			}(),
			stages: map[int]lifecycle.Stage{},
			reason: "owner-managed dependencies unavailable",
		},
		{
			name: "missing priority field",
			item: func() github.ProjectWorkItem {
				item := base
				item.PriorityKnown = false
				return item
			}(),
			stages: map[int]lifecycle.Stage{},
			reason: "owner-managed priority unavailable",
		},
		{
			name: "malformed configured field",
			item: func() github.ProjectWorkItem {
				item := base
				item.MetadataError = "untrusted field content"
				return item
			}(),
			stages: map[int]lifecycle.Stage{},
			reason: "owner-managed Project field invalid",
		},
		{
			name: "duplicate dependency references",
			item: func() github.ProjectWorkItem {
				item := base
				item.Dependencies = []int{3, 3}
				return item
			}(),
			stages: map[int]lifecycle.Stage{3: lifecycle.Done},
			reason: "owner-managed dependencies invalid",
		},
		{
			name: "dependency still under review",
			item: func() github.ProjectWorkItem {
				item := base
				item.Dependencies = []int{3}
				return item
			}(),
			stages: map[int]lifecycle.Stage{3: lifecycle.Review},
			reason: "dependencies have not reached Done",
		},
		{
			name: "dependency missing from Project",
			item: func() github.ProjectWorkItem {
				item := base
				item.Dependencies = []int{3}
				return item
			}(),
			stages: map[int]lifecycle.Stage{},
			reason: "dependencies have not reached Done",
		},
		{
			name: "dependency reached Done",
			item: func() github.ProjectWorkItem {
				item := base
				item.Dependencies = []int{3}
				return item
			}(),
			stages: map[int]lifecycle.Stage{3: lifecycle.Done},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			advice, needed := backlogOwnerFieldAdvice(tc.item, tc.stages)
			if needed != (tc.reason != "") || advice.IssueNumber != tc.item.Issue.Number || advice.BlockedReason != tc.reason {
				t.Fatalf("advice = %+v, needed=%t; want reason %q", advice, needed, tc.reason)
			}
			if needed && (advice.NextAction == "" || strings.Contains(advice.BlockedReason+advice.NextAction, "untrusted field content")) {
				t.Fatalf("advice missing action or echoed untrusted metadata: %+v", advice)
			}
			if !needed && advice.NextAction != "" {
				t.Fatalf("unchanged owner fields produced action: %+v", advice)
			}
		})
	}
}
