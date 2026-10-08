package version

import (
	"testing"
)

func TestSourceBuildDoesNotClaimRelease(t *testing.T) {
	got := Current()
	if got.Version != "development" || got.SourceCommit != "unknown" {
		t.Fatalf("source build claims release identity: %+v", got)
	}
}
