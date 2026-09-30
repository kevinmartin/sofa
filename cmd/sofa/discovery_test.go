package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/state"
)

func discoveryCommandFixture(t *testing.T) (config.Config, discoveryManifest) {
	t.Helper()
	c, _ := testManifest(t)
	c.Lifecycle = &config.Lifecycle{Statuses: map[string]string{
		"inbox": "Inbox", "discovery": "Discovery", "spec_review": "Spec Review", "backlog": "Backlog",
		"ready": c.ReadyStatus, "building": "Building", "verification": "Verification", "review": "Review", "release": "Release", "done": "Done",
	}, SpecAuthorID: "BOT_1"}
	digest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	source := admission.Snapshot{
		Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_7", Number: 7,
		Title: "Improve behavior", Body: "Investigate the existing behavior and acceptance cases", Open: true,
		BaseSHA: strings.Repeat("a", 40), ProjectID: c.ProjectID, ProjectPrivate: true,
		ProjectItemID: "PVTI_7", CurrentStatus: "Discovery", StatusOptionID: "discovery-option",
		StatusUpdatedAt: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC), Complete: true,
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	admitted, _, err := discovery.AuthorizeDiscovery(policy, source)
	if err != nil {
		t.Fatal(err)
	}
	m := discoveryManifest{
		Version: 1, Repository: c.Repository, ConfigDigest: digest, Source: source, Admission: admitted,
		Fence:       state.DiscoveryFence{IssueID: source.IssueID, Generation: 1, Owner: state.Owner{RunID: "123", RunAttempt: 1}},
		Facts:       `{"manifest_paths":["go.mod"],"workflow_paths":[],"test_files":1,"source_files":2,"related_issue_numbers":[]}`,
		TimeoutSecs: min(c.Limits.AttemptSeconds, 900),
	}
	return c, m
}

func TestDiscoveryManifestRejectsIdentityTampering(t *testing.T) {
	c, original := discoveryCommandFixture(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeJSON(path, original); err != nil {
		t.Fatal(err)
	}
	if _, err := readDiscoveryManifest(path, &c); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*discoveryManifest)
	}{
		{"different issue", func(m *discoveryManifest) { m.Source.IssueID = "I_other" }},
		{"different Project revision", func(m *discoveryManifest) { m.Source.StatusUpdatedAt = m.Source.StatusUpdatedAt.Add(time.Minute) }},
		{"different Project item", func(m *discoveryManifest) { m.Source.ProjectItemID = "PVTI_other" }},
		{"different config digest", func(m *discoveryManifest) { m.ConfigDigest = strings.Repeat("b", 64) }},
		{"different repository", func(m *discoveryManifest) { m.Repository = "other/repo" }},
		{"different fence", func(m *discoveryManifest) { m.Fence.IssueID = "I_other" }},
		{"longer model timeout", func(m *discoveryManifest) { m.TimeoutSecs++ }},
		{"invalid recovery identity", func(m *discoveryManifest) { m.Recovery = &state.Owner{RunID: "", RunAttempt: 1} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := original
			tc.mutate(&m)
			if err := writeJSON(path, m); err != nil {
				t.Fatal(err)
			}
			if _, err := readDiscoveryManifest(path, &c); err == nil {
				t.Fatal("tampered Discovery manifest accepted")
			}
		})
	}
	if err := writeJSON(path, original); err != nil {
		t.Fatal(err)
	}
	c.Lifecycle.SpecAuthorID = "BOT_other"
	if _, err := readDiscoveryManifest(path, &c); err == nil {
		t.Fatal("changed trusted publisher configuration accepted")
	}
}

func TestDiscoveryWorkerRefusesPrivilegedCredentialsBeforeModel(t *testing.T) {
	_, manifest := discoveryCommandFixture(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeJSON(path, manifest); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFA_MODEL_TOKEN", "inert-model-token")
	t.Setenv("SOFA_PROJECTS_TOKEN", "inert-project-token")
	out := filepath.Join(t.TempDir(), "candidate.json")
	if err := runDiscoveryGenerate(context.Background(), path, out); err == nil {
		t.Fatal("worker accepted Project credential")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("worker wrote candidate despite privilege: %v", err)
	}
}

func TestRelatedDiscoveryIssuesAreExactAndScoped(t *testing.T) {
	source := admission.Snapshot{Repository: "owner/repo", IssueID: "I_1", Number: 1, Title: " Fix crash ", Body: "When the worker stops", Open: true}
	items := []github.ProjectWorkItem{
		{Issue: admission.Snapshot{Repository: "owner/repo", IssueID: "I_1", Number: 1, Title: " Fix crash ", Open: true}},
		{Issue: admission.Snapshot{Repository: "owner/repo", IssueID: "I_2", Number: 2, Title: "fix crash", Open: true}},
		{Issue: admission.Snapshot{Repository: "OWNER/REPO", IssueID: "I_3", Number: 3, Title: "Different", Body: "When the worker stops", Open: true}},
		{Issue: admission.Snapshot{Repository: "owner/repo", IssueID: "I_4", Number: 4, Title: "Fix crash", Open: false}},
		{Issue: admission.Snapshot{Repository: "other/repo", IssueID: "I_5", Number: 5, Title: "Fix crash", Open: true}},
		{Issue: admission.Snapshot{Repository: "owner/repo", IssueID: "I_6", Number: 6, Title: "Crashing worker", Open: true}},
	}
	related, err := relatedDiscoveryIssues(source, items)
	if err != nil || len(related) != 2 || related[0] != 2 || related[1] != 3 {
		t.Fatalf("exact duplicate identifiers = %v, err=%v", related, err)
	}
}
