package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/lifecycle"
	"github.com/kevinmartin/sofa/internal/state"
)

type lifecycleRoundTrip func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func lifecycleJSONResponse(code int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}
}

func TestReadyForDeliveryHonorsApprovalDependenciesAndWIP(t *testing.T) {
	c := config.Config{
		Repository: "kevinmartin/sofa-disposable",
		Lifecycle: &config.Lifecycle{
			DeliveryWIP: 1,
		},
	}
	statuses := lifecycle.Statuses{lifecycle.Ready: "Ready", lifecycle.Done: "Done"}
	items := []github.ProjectWorkItem{
		{Issue: admission.Snapshot{IssueID: "done", Number: 1, CurrentStatus: "Done"}, DependenciesKnown: true, PriorityKnown: true},
		{Issue: admission.Snapshot{IssueID: "approved", Number: 2, ProjectItemID: "item-2", CurrentStatus: "Ready"}, Dependencies: []int{1}, DependenciesKnown: true, PriorityKnown: true},
		{Issue: admission.Snapshot{IssueID: "blocked", Number: 3, ProjectItemID: "item-3", CurrentStatus: "Ready"}, Dependencies: []int{9}, DependenciesKnown: true, PriorityKnown: true},
		{Issue: admission.Snapshot{IssueID: "unapproved", Number: 4, ProjectItemID: "item-4", CurrentStatus: "Ready"}, DependenciesKnown: true, PriorityKnown: true},
	}
	ledger := state.Empty()
	authority := map[string]bool{"approved": true, "blocked": true}
	got := readyForDelivery(c, items, ledger, authority, statuses, nil)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("ready issues = %v, want only approved dependency-complete #2", got)
	}
	ledger.Attempts["a"] = state.Attempt{
		Admission: state.Admission{Repository: c.Repository},
		Phase:     state.Executing,
		Owner:     &state.Owner{RunID: "1", RunAttempt: 1},
	}
	if got := readyForDelivery(c, items, ledger, authority, statuses, nil); len(got) != 0 {
		t.Fatalf("active writer did not consume WIP slot: %v", got)
	}
}

func TestReadyForDeliveryUsesOwnerPriorityThenIssueNumber(t *testing.T) {
	c := config.Config{Repository: "kevinmartin/sofa-disposable", Lifecycle: &config.Lifecycle{PriorityField: "Priority", DeliveryWIP: 2}}
	statuses := lifecycle.Statuses{lifecycle.Ready: "Ready"}
	items := []github.ProjectWorkItem{
		{Issue: admission.Snapshot{IssueID: "i1", Number: 1, ProjectItemID: "item-1", CurrentStatus: "Ready"}, Priority: "P2", PriorityRank: 2, PriorityKnown: true, DependenciesKnown: true},
		{Issue: admission.Snapshot{IssueID: "i3", Number: 3, ProjectItemID: "item-3", CurrentStatus: "Ready"}, Priority: "P0", PriorityRank: 0, PriorityKnown: true, DependenciesKnown: true},
		{Issue: admission.Snapshot{IssueID: "i2", Number: 2, ProjectItemID: "item-2", CurrentStatus: "Ready"}, Priority: "P0", PriorityRank: 0, PriorityKnown: true, DependenciesKnown: true},
		{Issue: admission.Snapshot{IssueID: "bad", Number: 4, ProjectItemID: "item-4", CurrentStatus: "Ready"}, MetadataError: "invalid priority", DependenciesKnown: true},
	}
	authority := map[string]bool{"i1": true, "i2": true, "i3": true, "bad": true}
	got := readyForDelivery(c, items, state.Empty(), authority, statuses, nil)
	if len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("priority order = %v, want P0 #2 then P0 #3", got)
	}
	got = readyForDelivery(c, items, state.Empty(), authority, statuses, []int{2})
	if len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Fatalf("held approval candidate dispatched or fallback order wrong: %v", got)
	}
}

func TestReadyForDiscoveryRespectsOwnerStatusWIPAndExistingPrompt(t *testing.T) {
	c, err := readConfig("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	statuses := lifecycleStatuses(c)
	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	items := make([]github.ProjectWorkItem, 0, 4)
	for number := 1; number <= 4; number++ {
		status := statuses[lifecycle.Discovery]
		if number == 4 {
			status = statuses[lifecycle.Inbox]
		}
		items = append(items, github.ProjectWorkItem{Issue: admission.Snapshot{
			Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_" + string(rune('0'+number)), Number: number,
			Title: "Idea", Body: "Investigate greeting behavior", Open: true, ProjectID: c.ProjectID, ProjectPrivate: true,
			ProjectItemID: "PVTI_" + string(rune('0'+number)), CurrentStatus: status, StatusOptionID: "option", StatusUpdatedAt: when, Complete: true,
		}})
	}
	ledger := state.Empty()
	if got := readyForDiscovery(c, items, ledger, policy, statuses, nil); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("owner Discovery admission and WIP selection = %v", got)
	}
	first, _, err := discovery.AuthorizeDiscovery(policy, items[0].Issue)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := discovery.AuthorizeDiscovery(policy, items[1].Issue)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Discoveries[first.IssueID] = state.DiscoveryTask{Repository: first.Repository, IssueID: first.IssueID, Issue: first.Issue, ProjectID: first.ProjectID, ProjectItemID: first.ProjectItemID, StatusOptionID: first.StatusOptionID, StatusUpdatedAt: first.StatusUpdatedAt, SourceDigest: first.SourceDigest, Phase: state.DiscoveryRunning, Owner: &state.Owner{RunID: "1", RunAttempt: 1}, ModelCalls: 1, MaxModelCalls: 1}
	ledger.Discoveries[second.IssueID] = state.DiscoveryTask{Repository: second.Repository, IssueID: second.IssueID, Issue: second.Issue, ProjectID: second.ProjectID, ProjectItemID: second.ProjectItemID, StatusOptionID: second.StatusOptionID, StatusUpdatedAt: second.StatusUpdatedAt, SourceDigest: second.SourceDigest, Phase: state.DiscoveryPending, MaxModelCalls: 1}
	if got := readyForDiscovery(c, items, ledger, policy, statuses, nil); len(got) != 1 || got[0] != 2 {
		t.Fatalf("pending reservation should retry without dispatching a new prompt: %v", got)
	}
	if got := readyForDiscovery(c, items, ledger, policy, statuses, []int{2}); len(got) != 0 {
		t.Fatalf("held pending issue was dispatched: %v", got)
	}
}

func TestReadyForDeliveryPreservesPendingWIPBeforeNewPriority(t *testing.T) {
	c := config.Config{Repository: "owner/repo", ProjectID: "P_1", Lifecycle: &config.Lifecycle{PriorityField: "Priority", DeliveryWIP: 1}}
	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	statuses := lifecycle.Statuses{lifecycle.Ready: "Ready"}
	items := []github.ProjectWorkItem{
		{Issue: admission.Snapshot{IssueID: "pending", Number: 9, ProjectID: "P_1", ProjectItemID: "item-9", CurrentStatus: "Ready"}, Priority: "P4", PriorityRank: 4, PriorityKnown: true, DependenciesKnown: true},
		{Issue: admission.Snapshot{IssueID: "new", Number: 2, ProjectID: "P_1", ProjectItemID: "item-2", CurrentStatus: "Ready"}, Priority: "P0", PriorityRank: 0, PriorityKnown: true, DependenciesKnown: true},
	}
	ledger := state.Empty()
	ledger.Specs["pending"] = state.SpecRecord{Repository: c.Repository, IssueID: "pending", Issue: 9, ProjectID: c.ProjectID, ProjectItemID: "item-9", Revision: 1, ApprovedDigest: digest, BacklogUpdatedAt: when}
	admissionRecord := state.Admission{Repository: c.Repository, ProjectID: c.ProjectID, ProjectItemID: "item-9", Issue: 9, SpecDigest: digest, StatusUpdatedAt: when.Add(time.Minute)}
	ledger.Attempts["pending"] = state.Attempt{Admission: admissionRecord, SpecRevision: 1, Phase: state.Pending}
	authority := map[string]bool{"pending": true, "new": true}
	got := readyForDelivery(c, items, ledger, authority, statuses, nil)
	if len(got) != 1 || got[0] != 9 {
		t.Fatalf("new high-priority item displaced occupied WIP slot: %v", got)
	}
	ledger.Attempts["pending"] = state.Attempt{Admission: admissionRecord, SpecRevision: 1, Phase: state.Executing, Owner: &state.Owner{RunID: "10", RunAttempt: 1}}
	got = readyForDelivery(c, items, ledger, authority, statuses, nil)
	if len(got) != 0 {
		t.Fatalf("active worker did not hold WIP: %v", got)
	}
}

func TestBoardSnapshotPreservesProjectIdentity(t *testing.T) {
	c := config.Config{Repository: "kevinmartin/sofa-disposable", ProjectID: "project"}
	now := time.Now().UTC()
	issue := admission.Snapshot{
		IssueID: "issue", ProjectItemID: "item", StatusOptionID: "option", StatusUpdatedAt: now,
	}
	got := boardFromSnapshot(c, lifecycle.Verification, issue)
	if got.Repository != c.Repository || got.ProjectID != c.ProjectID || got.ProjectItemID != issue.ProjectItemID || got.IssueID != issue.IssueID || got.Stage != "verification" || !got.UpdatedAt.Equal(now) {
		t.Fatalf("board identity changed: %+v", got)
	}
}

func TestAttemptForItemFailsClosedOnAmbiguousOrWrongIdentity(t *testing.T) {
	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	digest := strings.Repeat("a", 64)
	issue := admission.Snapshot{IssueID: "I_7", ProjectID: "P_1", ProjectItemID: "item-1", Number: 7}
	ledger := state.Empty()
	ledger.Specs[issue.IssueID] = state.SpecRecord{Repository: "owner/repo", IssueID: issue.IssueID, Issue: 7, ProjectID: issue.ProjectID, ProjectItemID: issue.ProjectItemID, Revision: 1, ApprovedDigest: digest, BacklogUpdatedAt: when}
	first := state.Attempt{Admission: state.Admission{Repository: "owner/repo", ProjectID: "P_1", ProjectItemID: "item-1", Issue: 7, SpecDigest: digest, StatusUpdatedAt: when.Add(time.Minute)}, SpecRevision: 1}
	ledger.Attempts["first"] = first
	if _, found, err := attemptForItem(ledger, "owner/repo", issue); err != nil || !found {
		t.Fatalf("exact attempt unavailable: found=%t err=%v", found, err)
	}
	ledger.Attempts["second"] = first
	if _, found, err := attemptForItem(ledger, "owner/repo", issue); err == nil || found {
		t.Fatalf("ambiguous attempt selected: found=%t err=%v", found, err)
	}
	prior := first
	prior.SupersededAt = when.Add(2 * time.Minute)
	ledger.Attempts["first"] = prior
	if _, found, err := attemptForItem(ledger, "owner/repo", issue); err != nil || !found {
		t.Fatalf("superseded historical attempt hid current one: found=%t err=%v", found, err)
	}
	delete(ledger.Attempts, "second")
	ledger.Attempts["first"] = first
	issue.Number = 8
	if _, found, err := attemptForItem(ledger, "owner/repo", issue); err == nil || found {
		t.Fatalf("wrong issue attempt selected: found=%t err=%v", found, err)
	}
}

func TestRecoverDiscoveryReviewValidatesCommentAndRetriesPendingProjectMove(t *testing.T) {
	c, err := readConfig("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		t.Fatal(err)
	}
	statuses := lifecycleStatuses(c)
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	spec, err := (discovery.Specification{
		Version: discovery.SpecificationVersion, Problem: "Observed defect", Evidence: "Reproduced locally", Goals: "Repair greeting", NonGoals: "No API redesign", Constraints: "Keep public behavior", Dependencies: "none", Acceptance: "Greeting is correct", Validation: "Run Go tests", Risks: "Low", Questions: "None", DeliverySlices: "One patch",
	}).Render()
	if err != nil {
		t.Fatal(err)
	}
	issue := admission.Snapshot{Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_7", Number: 7, Title: "Fix greeting", Body: "Investigate greeting", Open: true, ProjectID: c.ProjectID, ProjectPrivate: true, ProjectItemID: "PVTI_7", CurrentStatus: statuses[lifecycle.Discovery], StatusOptionID: "discovery-option", StatusUpdatedAt: base, BaseSHA: strings.Repeat("a", 40), Complete: true}
	_, sourceDigest, err := admission.CanonicalSpec(issue.Title, issue.Body)
	if err != nil {
		t.Fatal(err)
	}
	_, specDigest, err := admission.CanonicalSpec(issue.Title, spec)
	if err != nil {
		t.Fatal(err)
	}
	task := state.DiscoveryTask{Repository: strings.ToLower(c.Repository), IssueID: issue.IssueID, Issue: 7, ProjectID: c.ProjectID, ProjectItemID: issue.ProjectItemID, StatusOptionID: issue.StatusOptionID, StatusUpdatedAt: base, SourceDigest: sourceDigest, Phase: state.DiscoveryReview, SpecDigest: specDigest, CommentID: 77, CommentAuthorID: "U_bot", CommentCreatedAt: base.Add(time.Minute), CommentUpdatedAt: base.Add(time.Minute)}
	store := &state.MemoryStore{}
	engine := state.Engine{Store: store}
	mutationCalls := 0
	failFirstMutation := true
	corruptComment := false
	projectMoved := false
	client, err := github.New("fixture-token", lifecycleRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/comments/77") {
			body := spec
			if corruptComment {
				body = "changed after publication"
			}
			return lifecycleJSONResponse(200, map[string]any{"id": 77, "issue_url": "https://api.github.com/repos/" + c.Repository + "/issues/7", "body": body, "created_at": base.Add(time.Minute), "updated_at": base.Add(time.Minute), "user": map[string]any{"node_id": "U_bot"}}), nil
		}
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(request.Query, "fields(first:"):
			return lifecycleJSONResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{"id": c.ProjectID, "public": false, "fields": map[string]any{"nodes": []any{map[string]any{"id": "status-field", "name": "Status", "options": []any{map[string]any{"id": "discovery-option", "name": statuses[lifecycle.Discovery]}, map[string]any{"id": "review-option", "name": statuses[lifecycle.SpecReview]}}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
		case strings.Contains(request.Query, "projectItems(first:"):
			status := statuses[lifecycle.Discovery]
			option := "discovery-option"
			updated := base
			if projectMoved {
				status = statuses[lifecycle.SpecReview]
				option = "review-option"
				updated = base.Add(2 * time.Minute)
			}
			return lifecycleJSONResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{"projectItems": map[string]any{"nodes": []any{map[string]any{"id": issue.ProjectItemID, "isArchived": false, "project": map[string]any{"id": c.ProjectID, "public": false}, "fieldValueByName": map[string]any{"name": status, "optionId": option, "updatedAt": updated}}}, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
		case strings.HasPrefix(request.Query, "mutation"):
			mutationCalls++
			if failFirstMutation {
				failFirstMutation = false
				return lifecycleJSONResponse(503, map[string]any{}), nil
			}
			projectMoved = true
			return lifecycleJSONResponse(200, map[string]any{"data": map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]any{"id": issue.ProjectItemID}}}}), nil
		case strings.Contains(request.Query, "repository(owner:"):
			return lifecycleJSONResponse(200, map[string]any{"data": map[string]any{"repository": map[string]any{"id": c.RepositoryID, "nameWithOwner": c.Repository, "defaultBranchRef": map[string]any{"target": map[string]any{"oid": issue.BaseSHA}}, "issue": map[string]any{"id": issue.IssueID, "number": 7, "title": issue.Title, "body": issue.Body, "state": "OPEN", "lastEditedAt": nil}}}}), nil
		}
		t.Fatalf("unexpected GitHub request: %s", request.Query)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	corruptComment = true
	if err := recoverDiscoveryReview(context.Background(), client, engine, c, policy, statuses, issue, task); err == nil || mutationCalls != 0 {
		t.Fatalf("changed comment could reach Project mutation: err=%v calls=%d", err, mutationCalls)
	}
	corruptComment = false
	if err := recoverDiscoveryReview(context.Background(), client, engine, c, policy, statuses, issue, task); err == nil || mutationCalls != 1 {
		t.Fatalf("transient mutation failure not retained: err=%v calls=%d", err, mutationCalls)
	}
	if _, pending, err := engine.PendingBoardMove(context.Background(), issue.IssueID); err != nil || !pending {
		t.Fatalf("lost Project response erased write intent: pending=%t err=%v", pending, err)
	}
	if err := recoverDiscoveryReview(context.Background(), client, engine, c, policy, statuses, issue, task); err != nil || mutationCalls != 2 {
		t.Fatalf("pending Discovery move not recovered: err=%v calls=%d", err, mutationCalls)
	}
	snapshot, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	projection := snapshot.State.Projections[issue.IssueID]
	if projection.Stage != string(lifecycle.SpecReview) || projection.PendingStage != "" || projection.OptionID != "review-option" {
		t.Fatalf("recovered board projection = %+v", projection)
	}
}
