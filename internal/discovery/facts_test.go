package discovery

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestGatherFactsTruncatesLargeRepository(t *testing.T) {
	root := t.TempDir()
	for i := range 10001 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%05d.txt", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := GatherFacts(root, []int64{7})
	if err != nil {
		t.Fatalf("large repository blocked Discovery: %v", err)
	}
	var facts RepositoryFacts
	if err := json.Unmarshal([]byte(data), &facts); err != nil || !facts.Truncated || len(facts.RelatedIssues) != 1 || facts.RelatedIssues[0] != 7 {
		t.Fatalf("bounded facts lost truncation or issue identity: %+v, %v", facts, err)
	}
}

func TestGatherFactsBoundsManifestListWithoutBlockingDiscovery(t *testing.T) {
	root := t.TempDir()
	for i := range 101 {
		dir := filepath.Join(root, fmt.Sprintf("pkg-%03d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := GatherFacts(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var facts RepositoryFacts
	if err := json.Unmarshal([]byte(data), &facts); err != nil || !facts.Truncated || len(facts.ManifestPaths) != 100 {
		t.Fatalf("manifest list was not bounded: %+v, %v", facts, err)
	}
}
