package github

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestVerifiedRevertRequiresExactInverseOnDefault(t *testing.T) {
	merge := strings.Repeat("a", 40)
	revert := strings.Repeat("b", 40)
	defaultHead := strings.Repeat("c", 40)
	mergeParent := strings.Repeat("d", 40)
	revertParent := strings.Repeat("e", 40)
	treeBefore := strings.Repeat("1", 40)
	treeAfter := strings.Repeat("2", 40)
	treeRevertParent := strings.Repeat("3", 40)
	treeRevertAfter := strings.Repeat("4", 40)
	oldBlob := strings.Repeat("5", 40)
	newBlob := strings.Repeat("6", 40)
	partial := false
	multiParent := false
	client, err := New("fixture", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		path := r.URL.Path
		if strings.Contains(path, "/compare/") {
			return jsonResponse(200, map[string]any{"status": "ahead"}), nil
		}
		if strings.Contains(path, "/git/commits/") {
			sha := path[strings.LastIndex(path, "/")+1:]
			tree := ""
			var parents []map[string]string
			switch sha {
			case merge:
				tree, parents = treeAfter, []map[string]string{{"sha": mergeParent}}
			case revert:
				tree, parents = treeRevertAfter, []map[string]string{{"sha": revertParent}}
				if multiParent {
					parents = append(parents, map[string]string{"sha": merge})
				}
			case mergeParent:
				tree = treeBefore
			case revertParent:
				tree = treeRevertParent
			default:
				return jsonResponse(404, map[string]any{}), nil
			}
			return jsonResponse(200, map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}, "parents": parents}), nil
		}
		if strings.Contains(path, "/git/trees/") {
			sha := path[strings.LastIndex(path, "/")+1:]
			blob := ""
			switch sha {
			case treeBefore, treeRevertAfter:
				blob = oldBlob
			case treeAfter, treeRevertParent:
				blob = newBlob
			default:
				return jsonResponse(404, map[string]any{}), nil
			}
			if partial && sha == treeRevertAfter {
				blob = newBlob
			}
			return jsonResponse(200, map[string]any{"tree": []map[string]string{{"path": "fixture/a.txt", "mode": "100644", "type": "blob", "sha": blob}}, "truncated": false}), nil
		}
		return jsonResponse(404, map[string]any{}), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	verified, err := client.VerifiedRevert(context.Background(), "owner/repo", merge, revert, defaultHead)
	if err != nil || !verified {
		t.Fatalf("exact inverse rejected: %t, %v", verified, err)
	}
	partial = true
	verified, err = client.VerifiedRevert(context.Background(), "owner/repo", merge, revert, defaultHead)
	if err != nil || verified {
		t.Fatalf("partial revert accepted: %t, %v", verified, err)
	}
	multiParent = true
	verified, err = client.VerifiedRevert(context.Background(), "owner/repo", merge, revert, defaultHead)
	if err != nil || verified {
		t.Fatalf("multi-parent revert accepted or aborted scan: %t, %v", verified, err)
	}
}
