package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
)

func TestProjectWorkItemsReadsSeparateAdviceValues(t *testing.T) {
	policy := projectTestConfig(t)
	node := projectIssueNode(policy, 1)
	node["blockedReason"] = map[string]any{"text": "required gate evidence unavailable"}
	node["nextAction"] = map[string]any{"text": "inspect current gates"}
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Variables map[string]json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		var blockedName, nextName string
		if err := json.Unmarshal(request.Variables["blockedName"], &blockedName); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(request.Variables["nextName"], &nextName); err != nil {
			t.Fatal(err)
		}
		if blockedName != "Blocked reason" || nextName != "Next action" {
			t.Fatalf("wrong configured advice names: %q, %q", blockedName, nextName)
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
			"id": policy.ProjectID, "public": false,
			"items": map[string]any{"nodes": []any{node}, "pageInfo": map[string]any{"hasNextPage": false}},
		}}}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	items, err := client.ProjectWorkItems(context.Background(), policy)
	if err != nil || len(items) != 1 || items[0].BlockedReason != "required gate evidence unavailable" || items[0].NextAction != "inspect current gates" {
		t.Fatalf("advice fields missing from work item: items=%+v err=%v", items, err)
	}
}

func TestProjectAdviceFieldsRequireTwoPrivateTextFields(t *testing.T) {
	for _, tc := range []struct {
		name     string
		public   bool
		fields   []any
		wantErr  bool
		optional bool
	}{
		{
			name: "valid",
			fields: []any{
				map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "TEXT"},
				map[string]any{"id": "F_next", "name": "Next action", "dataType": "TEXT"},
			},
		},
		{
			name:    "missing next field",
			fields:  []any{map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "TEXT"}},
			wantErr: true,
		},
		{
			name:     "optional default pair absent",
			optional: true,
		},
		{
			name:     "optional default pair partial",
			fields:   []any{map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "TEXT"}},
			optional: true,
			wantErr:  true,
		},
		{
			name: "wrong type",
			fields: []any{
				map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "TEXT"},
				map[string]any{"id": "F_next", "name": "Next action", "dataType": "SINGLE_SELECT"},
			},
			wantErr: true,
		},
		{
			name: "optional defaults with wrong field types",
			fields: []any{
				map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "SINGLE_SELECT"},
				map[string]any{"id": "F_next", "name": "Next action", "dataType": "ITERATION"},
			},
			optional: true,
			wantErr:  true,
		},
		{
			name:   "public Project",
			public: true,
			fields: []any{
				map[string]any{"id": "F_blocked", "name": "Blocked reason", "dataType": "TEXT"},
				map[string]any{"id": "F_next", "name": "Next action", "dataType": "TEXT"},
			},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var request struct{ Query string }
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatal(err)
				}
				responseFields := tc.fields
				if strings.Contains(request.Query, "... on ProjectV2Field{") {
					// GitHub returns empty nodes for single-select and iteration
					// fields when a query selects only the concrete text-field type.
					responseFields = make([]any, len(tc.fields))
					for i, field := range tc.fields {
						value := field.(map[string]any)
						if value["dataType"] == "TEXT" {
							responseFields[i] = value
						} else {
							responseFields[i] = map[string]any{}
						}
					}
				}
				return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
					"id": "P_1", "public": tc.public,
					"fields": map[string]any{"nodes": responseFields, "pageInfo": map[string]any{"hasNextPage": false}},
				}}}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			fields, err := client.ProjectAdviceFields(context.Background(), "P_1", "Blocked reason", "Next action", !tc.optional)
			if (err != nil) != tc.wantErr {
				t.Fatalf("fields=%+v err=%v", fields, err)
			}
			if !tc.wantErr && !tc.optional && (fields.BlockedReasonID != "F_blocked" || fields.NextActionID != "F_next") {
				t.Fatalf("wrong advice field IDs: %+v", fields)
			}
			if tc.optional && !tc.wantErr && (fields.BlockedReasonID != "" || fields.NextActionID != "") {
				t.Fatalf("absent optional fields should disable writes: %+v", fields)
			}
		})
	}
}

func TestSetProjectAdviceIfCurrentSkipsUnchangedWritesAndClearsResolvedHold(t *testing.T) {
	updatedAt := time.Date(2026, 9, 23, 21, 55, 32, 0, time.UTC)
	issue := admission.Snapshot{
		IssueID:         "I_1",
		ProjectID:       "P_1",
		ProjectPrivate:  true,
		ProjectItemID:   "PVTI_1",
		StatusOptionID:  "verification",
		StatusUpdatedAt: updatedAt,
		Complete:        true,
	}
	fields := ProjectAdviceFields{
		BlockedReasonID: "F_blocked",
		NextActionID:    "F_next",
		BlockedName:     "Blocked reason",
		NextName:        "Next action",
	}
	blocked := "required gate evidence unavailable or failed"
	next := "inspect current required gate evidence"
	writes := 0
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			Query     string
			Variables map[string]json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(request.Query, "mutation") {
			writes++
			var fieldID string
			if err := json.Unmarshal(request.Variables["field"], &fieldID); err != nil {
				t.Fatal(err)
			}
			responseField := "updateProjectV2ItemFieldValue"
			if strings.Contains(request.Query, "clearProjectV2ItemFieldValue") {
				responseField = "clearProjectV2ItemFieldValue"
				if fieldID == "F_blocked" {
					blocked = ""
				} else {
					next = ""
				}
			} else {
				var value string
				if err := json.Unmarshal(request.Variables["text"], &value); err != nil {
					t.Fatal(err)
				}
				if fieldID == "F_blocked" {
					blocked = value
				} else {
					next = value
				}
			}
			return jsonResponse(200, map[string]any{"data": map[string]any{responseField: map[string]any{"projectV2Item": map[string]any{"id": issue.ProjectItemID}}}}), nil
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
			"projectItems": map[string]any{
				"nodes": []any{map[string]any{
					"id": issue.ProjectItemID, "isArchived": false,
					"project":          map[string]any{"id": issue.ProjectID, "public": false},
					"fieldValueByName": map[string]any{"optionId": issue.StatusOptionID, "updatedAt": updatedAt.Format(time.RFC3339)},
					"blockedReason":    map[string]any{"text": blocked},
					"nextAction":       map[string]any{"text": next},
				}},
				"pageInfo": map[string]any{"hasNextPage": false},
			},
		}}}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetProjectAdviceIfCurrent(context.Background(), issue, fields, blocked, next); err != nil || writes != 0 {
		t.Fatalf("unchanged advice wrote fields: err=%v writes=%d", err, writes)
	}
	if err := client.SetProjectAdviceIfCurrent(context.Background(), issue, fields, "", ""); err != nil || writes != 2 || blocked != "" || next != "" {
		t.Fatalf("resolved hold was not cleared: err=%v writes=%d blocked=%q next=%q", err, writes, blocked, next)
	}
	if err := client.SetProjectAdviceIfCurrent(context.Background(), issue, fields, "new hold", "inspect proof"); err != nil || writes != 4 || blocked != "new hold" || next != "inspect proof" {
		t.Fatalf("new hold was not written: err=%v writes=%d blocked=%q next=%q", err, writes, blocked, next)
	}
}

func TestSetProjectAdviceIfCurrentRejectsMovedStatusAndUnsafeText(t *testing.T) {
	updatedAt := time.Date(2026, 9, 23, 21, 55, 32, 0, time.UTC)
	issue := admission.Snapshot{
		IssueID: "I_1", ProjectID: "P_1", ProjectPrivate: true,
		ProjectItemID: "PVTI_1", StatusOptionID: "verification",
		StatusUpdatedAt: updatedAt, Complete: true,
	}
	fields := ProjectAdviceFields{BlockedReasonID: "F_blocked", NextActionID: "F_next", BlockedName: "Blocked reason", NextName: "Next action"}
	writes := 0
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(request.Query, "mutation") {
			writes++
			t.Fatal("mutation after status changed")
		}
		return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{
			"projectItems": map[string]any{"nodes": []any{map[string]any{
				"id": "PVTI_1", "isArchived": false,
				"project":          map[string]any{"id": "P_1", "public": false},
				"fieldValueByName": map[string]any{"optionId": "review", "updatedAt": updatedAt.Add(time.Minute).Format(time.RFC3339)},
			}}, "pageInfo": map[string]any{"hasNextPage": false}},
		}}}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SetProjectAdviceIfCurrent(context.Background(), issue, fields, "blocked", "inspect proof"); err == nil || writes != 0 {
		t.Fatalf("moved status accepted: err=%v writes=%d", err, writes)
	}
	if err := client.SetProjectAdviceIfCurrent(context.Background(), issue, fields, "untrusted\ntext", "inspect proof"); err == nil || writes != 0 {
		t.Fatalf("unsafe advice accepted: err=%v writes=%d", err, writes)
	}
}
