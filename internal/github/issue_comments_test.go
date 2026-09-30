package github

import (
	"context"
	"net/http"
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
