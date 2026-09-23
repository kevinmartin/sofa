package github

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
)

func TestIssueReadsReadyRevisionWithoutCommentOrTimeline(t *testing.T) {
	f, err := os.Open("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	policy, err := config.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	const readyAt = "2026-09-23T21:55:32Z"
	requests := 0
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		var input struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(input.Query, "repository(owner"):
			return jsonResponse(200, map[string]any{"data": map[string]any{"repository": map[string]any{
				"id": policy.RepositoryID, "nameWithOwner": policy.Repository,
				"defaultBranchRef": map[string]any{"target": map[string]any{"oid": strings.Repeat("a", 40)}},
				"issue":            map[string]any{"id": "I_1", "number": 1, "title": "Fix greeting", "body": "Return hello.", "state": "OPEN", "lastEditedAt": nil},
			}}}), nil
		case strings.Contains(input.Query, "projectItems("):
			return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{"projectItems": map[string]any{
				"nodes":    []any{map[string]any{"id": "PVTI_1", "isArchived": false, "project": map[string]any{"id": policy.ProjectID, "public": false}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready-id", "updatedAt": readyAt}}},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			}}}}), nil
		}
		t.Fatalf("unexpected GraphQL query: %s", input.Query)
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := client.Issue(context.Background(), policy, 1)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || !s.ProjectPrivate || s.ProjectItemID != "PVTI_1" || s.StatusOptionID != "ready-id" || s.StatusUpdatedAt.Format(time.RFC3339) != readyAt || !s.Complete {
		t.Fatalf("unexpected Ready source: %#v; requests=%d", s, requests)
	}
	if _, _, err := admission.Authorize(policy, s); err != nil {
		t.Fatal(err)
	}
}

func TestProjectStatusRejectsPublicOrAmbiguousItems(t *testing.T) {
	for name, items := range map[string][]any{
		"public":             {map[string]any{"id": "PVTI_1", "isArchived": false, "project": map[string]any{"id": "P_1", "public": true}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready-id", "updatedAt": "2026-09-23T21:55:32Z"}}},
		"visibility missing": {map[string]any{"id": "PVTI_1", "isArchived": false, "project": map[string]any{"id": "P_1"}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready-id", "updatedAt": "2026-09-23T21:55:32Z"}}},
		"duplicate": {
			map[string]any{"id": "PVTI_1", "isArchived": false, "project": map[string]any{"id": "P_1", "public": false}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready-id", "updatedAt": "2026-09-23T21:55:32Z"}},
			map[string]any{"id": "PVTI_2", "isArchived": false, "project": map[string]any{"id": "P_1", "public": false}, "fieldValueByName": map[string]any{"name": "Ready", "optionId": "ready-id", "updatedAt": "2026-09-23T21:55:32Z"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client, err := New("fixture-token", roundTripFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(200, map[string]any{"data": map[string]any{"node": map[string]any{"projectItems": map[string]any{"nodes": items, "pageInfo": map[string]any{"hasNextPage": false}}}}}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.projectStatus(context.Background(), "I_1", "P_1"); err == nil {
				t.Fatal("accepted public or ambiguous Project item")
			}
		})
	}
}
