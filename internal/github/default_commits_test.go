package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDefaultCommitComparisonPageBindsIdentityAndSkipsBadMessages(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	middle := strings.Repeat("c", 40)
	responseHead := head
	client, err := New("fixture", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/owner/repo/compare/"+base+"..."+head || r.URL.Query().Get("per_page") != "100" || r.URL.Query().Get("page") != "1" {
			t.Fatalf("unbounded or wrong comparison request: %s %s", r.Method, r.URL)
		}
		return jsonResponse(200, map[string]any{
			"status": "ahead", "total_commits": 4,
			"base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": base},
			"commits": []map[string]any{
				{"sha": middle, "commit": map[string]string{"message": "This reverts commit " + base}},
				{"sha": strings.Repeat("d", 40), "commit": map[string]string{"message": strings.Repeat("x", 64<<10+1)}},
				{"sha": strings.Repeat("e", 40), "commit": map[string]string{"message": "binary\x00hint"}},
				{"sha": responseHead, "commit": map[string]string{"message": "ordinary change"}},
			},
		}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.DefaultCommitComparisonPage(context.Background(), "owner/repo", base, head, 1)
	if err != nil || !page.Final || len(page.Commits) != 2 || page.Commits[0].SHA != middle || page.Commits[1].SHA != head {
		t.Fatalf("bounded comparison lost valid hint: %+v, %v", page, err)
	}
	responseHead = middle
	if _, err := client.DefaultCommitComparisonPage(context.Background(), "owner/repo", base, head, 1); err == nil {
		t.Fatal("wrong comparison head accepted")
	}
	if _, err := client.DefaultCommitComparisonPage(context.Background(), "owner/repo", base, "invalid", 1); err == nil {
		t.Fatal("invalid SHA reached GitHub")
	}
}

func TestDefaultCommitComparisonPageTraversesPastFirstHundred(t *testing.T) {
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	first := make([]map[string]any, 100)
	for i := range first {
		sha := strings.Repeat("0", 38) + string("0123456789abcdef"[i/16]) + string("0123456789abcdef"[i%16])
		first[i] = map[string]any{"sha": sha, "commit": map[string]string{"message": "ordinary"}}
	}
	client, err := New("fixture", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/owner/repo/compare/"+base+"..."+head || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("wrong comparison endpoint: %s", r.URL)
		}
		var commits []map[string]any
		switch r.URL.Query().Get("page") {
		case "1":
			commits = first
		case "2":
			commits = []map[string]any{{"sha": head, "commit": map[string]string{"message": "This reverts commit " + base}}}
		default:
			t.Fatalf("unexpected page: %s", r.URL)
		}
		return jsonResponse(200, map[string]any{"status": "ahead", "total_commits": 101, "base_commit": map[string]string{"sha": base}, "merge_base_commit": map[string]string{"sha": base}, "commits": commits}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	page1, err := client.DefaultCommitComparisonPage(context.Background(), "owner/repo", base, head, 1)
	if err != nil || page1.Final || len(page1.Commits) != 100 {
		t.Fatalf("first page incomplete: %+v, %v", page1, err)
	}
	page2, err := client.DefaultCommitComparisonPage(context.Background(), "owner/repo", base, head, 2)
	if err != nil || !page2.Final || len(page2.Commits) != 1 || page2.Commits[0].SHA != head {
		t.Fatalf("older comparison range ended early: %+v, %v", page2, err)
	}
}
