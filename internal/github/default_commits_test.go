package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRecentDefaultCommitsBindsExactHeadAndBoundsPage(t *testing.T) {
	head := strings.Repeat("a", 40)
	second := strings.Repeat("b", 40)
	responseHead := head
	client, err := New("fixture", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/repo/commits" || r.URL.Query().Get("sha") != head || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("unbounded or wrong default-commit request: %s %s", r.Method, r.URL)
		}
		return jsonResponse(200, []map[string]any{
			{"sha": responseHead, "commit": map[string]any{"message": "normal change"}},
			{"sha": second, "commit": map[string]any{"message": "This reverts commit " + head + "."}},
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	commits, err := client.RecentDefaultCommits(context.Background(), "owner/repo", head)
	if err != nil || len(commits) != 2 || commits[0].SHA != head || commits[1].SHA != second {
		t.Fatalf("default commit page not bound: %+v, %v", commits, err)
	}
	responseHead = second
	if _, err := client.RecentDefaultCommits(context.Background(), "owner/repo", head); err == nil {
		t.Fatal("stale default head accepted")
	}
	if _, err := client.RecentDefaultCommits(context.Background(), "owner/repo", "invalid"); err == nil {
		t.Fatal("invalid SHA reached GitHub")
	}
}
