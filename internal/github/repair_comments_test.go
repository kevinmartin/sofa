package github

import (
	"context"
	"net/http"
	"testing"
)

func TestPullReviewCommentsBindsSelectedReview(t *testing.T) {
	client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/repo/pulls/7/reviews/9/comments" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("unexpected review comment request: %s %s", r.Method, r.URL)
		}
		return jsonResponse(http.StatusOK, []map[string]any{
			{
				"id":                     44,
				"pull_request_review_id": 9,
				"commit_id":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"path":                   "fixture.go",
				"line":                   12,
				"body":                   "Fix the empty case",
				"user": map[string]any{
					"node_id": "U_owner",
				},
			},
			{
				"id":                     45,
				"pull_request_review_id": 9,
				"in_reply_to_id":         44,
				"commit_id":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"path":                   "fixture.go",
				"body":                   "Unrelated later reply",
				"user": map[string]any{
					"node_id": "U_other",
				},
			},
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	comments, err := client.PullReviewComments(context.Background(), "owner/repo", 7, 9)
	if err != nil || len(comments) != 1 || comments[0].ReviewID != 9 || comments[0].Line == nil || *comments[0].Line != 12 {
		t.Fatalf("selected review comments unavailable: %+v, %v", comments, err)
	}
}

func TestPullReviewCommentsRejectsCrossReviewResponse(t *testing.T) {
	client, err := New("fixture-token", roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, []map[string]any{
			{
				"id":                     44,
				"pull_request_review_id": 10,
				"commit_id":              "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"path":                   "fixture.go",
				"body":                   "Other review",
				"user": map[string]any{
					"node_id": "U_owner",
				},
			},
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PullReviewComments(context.Background(), "owner/repo", 7, 9); err == nil {
		t.Fatal("comment from another review accepted")
	}
}
