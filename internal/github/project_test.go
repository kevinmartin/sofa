package github

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/lifecycle"
)

func projectTestConfig(t *testing.T) config.Config {
	t.Helper()
	f, err := os.Open("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	policy, err := config.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func projectIssueNode(policy config.Config, number int) map[string]any {
	return map[string]any{
		"id":         "PVTI_" + string(rune('0'+number)),
		"isArchived": false,
		"content": map[string]any{
			"id":           "I_" + string(rune('0'+number)),
			"number":       number,
			"title":        "Issue",
			"body":         "Specification",
			"state":        "OPEN",
			"lastEditedAt": nil,
			"repository": map[string]any{
				"id":               policy.RepositoryID,
				"nameWithOwner":    policy.Repository,
				"defaultBranchRef": map[string]any{"target": map[string]any{"oid": strings.Repeat("a", 40)}},
			},
		},
		"fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready", "updatedAt": "2026-09-23T21:55:32Z"},
	}
}

func TestProjectIssuesPaginatesAndRejectsPartialOrAmbiguousScans(t *testing.T) {
	policy := projectTestConfig(t)
	for _, tc := range []struct {
		name         string
		secondStatus int
		duplicate    bool
		wantError    bool
	}{
		{
			name:         "complete",
			secondStatus: 200,
		},
		{
			name:         "later API failure",
			secondStatus: 503,
			wantError:    true,
		},
		{
			name:         "duplicate issue across pages",
			secondStatus: 200,
			duplicate:    true,
			wantError:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var request struct {
					Variables map[string]json.RawMessage `json:"variables"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				if calls == 2 && tc.secondStatus != 200 {
					return jsonResponse(tc.secondStatus, map[string]any{}), nil
				}
				if calls > 2 {
					t.Fatal("unexpected extra Project page")
				}
				page := map[string]any{"hasNextPage": calls == 1, "endCursor": nil}
				if calls == 1 {
					page["endCursor"] = "next"
				} else if string(request.Variables["after"]) != `"next"` {
					t.Fatalf("wrong page cursor: %s", request.Variables["after"])
				}
				number := calls
				if tc.duplicate {
					number = 1
				}
				return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
					"id": policy.ProjectID, "public": false,
					"items": map[string]any{"nodes": []any{projectIssueNode(policy, number)}, "pageInfo": page},
				}}}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			items, err := client.ProjectIssues(context.Background(), policy)
			if (err != nil) != tc.wantError {
				t.Fatalf("items=%v error=%v", items, err)
			}
			if tc.wantError && len(items) != 0 {
				t.Fatalf("partial authorization returned: %+v", items)
			}
			if !tc.wantError && (calls != 2 || len(items) != 2 || items[0].Number != 1 || items[1].Number != 2 || !items[0].Complete) {
				t.Fatalf("unexpected Project scan: calls=%d items=%+v", calls, items)
			}
		})
	}
}

func TestSetProjectStatusRejectsChangedItemBeforeMutation(t *testing.T) {
	updatedAt := time.Date(2026, 9, 23, 21, 55, 32, 0, time.UTC)
	statuses := lifecycle.Statuses{
		lifecycle.Inbox: "Inbox", lifecycle.Discovery: "Discovery", lifecycle.SpecReview: "Spec Review", lifecycle.Backlog: "Backlog", lifecycle.Ready: "Ready", lifecycle.Building: "Building", lifecycle.Verification: "Verification", lifecycle.Review: "Review", lifecycle.Release: "Release", lifecycle.Done: "Done",
	}
	for _, tc := range []struct {
		name      string
		item      string
		option    string
		updated   time.Time
		wantWrite bool
	}{
		{
			name:      "exact",
			item:      "PVTI_1",
			option:    "ready",
			updated:   updatedAt,
			wantWrite: true,
		},
		{
			name:    "other item",
			item:    "PVTI_2",
			option:  "ready",
			updated: updatedAt,
		},
		{
			name:    "manual move",
			item:    "PVTI_1",
			option:  "review",
			updated: updatedAt,
		},
		{
			name:    "late edit",
			item:    "PVTI_1",
			option:  "ready",
			updated: updatedAt.Add(time.Minute),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var input struct{ Query string }
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(input.Query, "mutation") {
					writes++
					return jsonResponse(200, map[string]any{"data": map[string]any{"updateProjectV2ItemFieldValue": map[string]any{"projectV2Item": map[string]any{"id": "PVTI_1"}}}}), nil
				}
				if strings.Contains(input.Query, "fields(first:") {
					return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
						"id": "P_1", "public": false,
						"fields": map[string]any{"nodes": []any{map[string]any{"id": "field", "name": "Status", "options": []any{map[string]any{"id": "ready", "name": "Ready"}, map[string]any{"id": "building", "name": "Building"}}}}, "pageInfo": map[string]any{"hasNextPage": false}},
					}}}), nil
				}
				return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{"projectItems": map[string]any{
					"nodes":    []any{map[string]any{"id": tc.item, "isArchived": false, "project": map[string]any{"id": "P_1", "public": false}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": tc.option, "updatedAt": tc.updated.Format(time.RFC3339)}}},
					"pageInfo": map[string]any{"hasNextPage": false},
				}}}}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			err = client.SetProjectStatusIfCurrent(context.Background(), "I_1", "P_1", "PVTI_1", "ready", updatedAt, statuses, lifecycle.Building)
			if tc.wantWrite != (err == nil) || writes != btoi(tc.wantWrite) {
				t.Fatalf("err=%v writes=%d", err, writes)
			}
			if err := client.SetProjectStatusIfCurrent(context.Background(), "I_1", "P_1", "PVTI_1", "ready", updatedAt, statuses, lifecycle.Backlog); err == nil || writes != btoi(tc.wantWrite) {
				t.Fatalf("automation could set owner approval status: err=%v writes=%d", err, writes)
			}
		})
	}
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func TestProjectStatusFieldRejectsPublicAndDuplicateOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		public    bool
		options   []any
		wantError bool
	}{
		{
			name:    "valid",
			options: []any{map[string]any{"id": "a", "name": "Ready"}},
		},
		{
			name:      "public",
			public:    true,
			options:   []any{map[string]any{"id": "a", "name": "Ready"}},
			wantError: true,
		},
		{
			name:      "duplicate",
			options:   []any{map[string]any{"id": "a", "name": "Ready"}, map[string]any{"id": "b", "name": "Ready"}},
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("fixture-token", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
					"id": "P_1", "public": tc.public,
					"fields": map[string]any{"nodes": []any{map[string]any{"id": "field", "name": "Status", "options": tc.options}}, "pageInfo": map[string]any{"hasNextPage": false}},
				}}}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			id, options, err := client.ProjectStatusField(context.Background(), "P_1")
			if (err != nil) != tc.wantError {
				t.Fatalf("field=%q options=%v error=%v", id, options, err)
			}
			if !tc.wantError && (id != "field" || !reflect.DeepEqual(options, map[string]string{"Ready": "a"})) {
				t.Fatalf("field=%q options=%v", id, options)
			}
		})
	}
}

func TestParseDependenciesRequiresOwnerFieldIntent(t *testing.T) {
	for _, tc := range []struct {
		text    string
		want    []int
		invalid bool
	}{
		{
			text: "none",
			want: []int{},
		},
		{
			text: "#12, #3",
			want: []int{3, 12},
		},
		{
			text:    "",
			invalid: true,
		},
		{
			text:    "#2, #2",
			invalid: true,
		},
		{
			text:    "#4",
			invalid: true,
		},
		{
			text:    "other/repo#2",
			invalid: true,
		},
		{
			text:    "#1,#2",
			invalid: true,
		},
		{
			text:    "#0",
			invalid: true,
		},
	} {
		got, err := ParseDependencies(tc.text, 4)
		if (err != nil) != tc.invalid || !tc.invalid && !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("parse %q = %v, %v", tc.text, got, err)
		}
	}
}

func TestProjectWorkItemsKeepsMissingOwnerFieldsPerItem(t *testing.T) {
	policy := projectTestConfig(t)
	policy.Lifecycle.DependenciesField = "Dependencies"
	policy.Lifecycle.PriorityField = "Priority"
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	client, err := New("fixture-token", roundTripFunc(func(*http.Request) (*http.Response, error) {
		first := projectIssueNode(policy, 1)
		first["dependencies"] = map[string]any{"text": "#3"}
		first["priority"] = map[string]any{"name": "P1"}
		second := projectIssueNode(policy, 2)
		third := projectIssueNode(policy, 3)
		third["dependencies"] = map[string]any{"text": "other/repo#4"}
		third["priority"] = map[string]any{"name": "Critical"}
		fourth := projectIssueNode(policy, 4)
		fourth["dependencies"] = map[string]any{"text": "none"}
		fourth["priority"] = map[string]any{"name": "P0"}
		return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
			"id": policy.ProjectID, "public": false,
			"items": map[string]any{"nodes": []any{first, second, third, fourth}, "pageInfo": map[string]any{"hasNextPage": false}},
		}}}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.ProjectWorkItems(context.Background(), policy)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 || !items[0].DependenciesKnown || !reflect.DeepEqual(items[0].Dependencies, []int{3}) || !items[0].PriorityKnown || items[0].Priority != "P1" || items[0].PriorityRank != 1 || items[1].DependenciesKnown || items[1].PriorityKnown || items[2].DependenciesKnown || items[2].MetadataError == "" || items[2].PriorityKnown || !items[3].DependenciesKnown || len(items[3].Dependencies) != 0 || !items[3].PriorityKnown || items[3].PriorityRank != 0 {
		t.Fatalf("owner field boundaries lost: %+v", items)
	}
}
