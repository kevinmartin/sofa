package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestIssueCommentBindsRepositoryIssueAndID(t *testing.T) {
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/kevinmartin/sofa-disposable/issues/comments/42" {
			t.Fatalf("unexpected comment request: %s %s", r.Method, r.URL)
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"id":         42,
			"issue_url":  "https://api.github.com/repos/kevinmartin/sofa-disposable/issues/99",
			"body":       "copied specification",
			"user":       map[string]any{"node_id": "BOT_node"},
			"created_at": "2026-09-29T10:00:00Z",
			"updated_at": "2026-09-29T10:00:00Z",
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.IssueComment(context.Background(), "kevinmartin/sofa-disposable", 12, 42); err == nil {
		t.Fatal("accepted a comment belonging to another issue")
	}
}

func TestIssueCommentsSkipsUnrelatedOversizedAndGhostAuthor(t *testing.T) {
	large := strings.Repeat("界", 22000)
	comment := func(id int, body, author string) map[string]any {
		return map[string]any{
			"id":         id,
			"issue_url":  "https://api.github.com/repos/kevinmartin/sofa-disposable/issues/12",
			"body":       body,
			"user":       map[string]any{"node_id": author},
			"created_at": "2026-09-29T10:00:00Z",
			"updated_at": "2026-09-29T10:00:00Z",
		}
	}
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/kevinmartin/sofa-disposable/issues/12/comments":
			return jsonResponse(http.StatusOK, []map[string]any{
				comment(1, large, "U_other"),
				comment(2, "ghost", ""),
				comment(3, "approved specification", "BOT_node"),
			}), nil
		case "/repos/kevinmartin/sofa-disposable/issues/comments/1":
			return jsonResponse(http.StatusOK, comment(1, large, "U_other")), nil
		default:
			t.Fatalf("unexpected comment request: %s", r.URL)
			return nil, nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	comments, err := client.IssueComments(context.Background(), "kevinmartin/sofa-disposable", 12)
	if err != nil || len(comments) != 1 || comments[0].ID != 3 {
		t.Fatalf("unrelated comment blocked recovery: %+v, %v", comments, err)
	}
	if _, err := client.IssueComment(context.Background(), "kevinmartin/sofa-disposable", 12, 1); err == nil {
		t.Fatal("exact oversized comment was accepted")
	}
}

func TestCreateIssueCommentRequiresExactEcho(t *testing.T) {
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/kevinmartin/sofa-disposable/issues/12/comments" {
			t.Fatalf("unexpected comment request: %s %s", r.Method, r.URL)
		}
		return jsonResponse(http.StatusCreated, map[string]any{
			"id":         42,
			"issue_url":  "https://api.github.com/repos/kevinmartin/sofa-disposable/issues/12",
			"body":       "unexpected body",
			"user":       map[string]any{"node_id": "BOT_node"},
			"created_at": "2026-09-29T10:00:00Z",
			"updated_at": "2026-09-29T10:00:00Z",
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateIssueComment(context.Background(), "kevinmartin/sofa-disposable", 12, "approved content"); err == nil {
		t.Fatal("accepted a comment response with unexpected body")
	}
}
