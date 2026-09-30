package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestIsAncestorUsesPatchFreeComparisonPage(t *testing.T) {
	ancestor := strings.Repeat("a", 40)
	descendant := strings.Repeat("b", 40)
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{status: "identical", want: true},
		{status: "ahead", want: true},
		{status: "behind", want: false},
		{status: "diverged", want: false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			client, err := New("fixture", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/repos/owner/repo/compare/"+ancestor+"..."+descendant || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "1" {
					t.Fatalf("comparison requested a changed-file page: %s", r.URL)
				}
				return jsonResponse(200, map[string]any{"status": tc.status}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.IsAncestor(context.Background(), "owner/repo", ancestor, descendant)
			if err != nil || got != tc.want {
				t.Fatalf("status %q yielded %t, %v", tc.status, got, err)
			}
		})
	}
}
