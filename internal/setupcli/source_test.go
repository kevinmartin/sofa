package setupcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceSelectionBuildsBothCLIsOncePerJobWithoutTokens(t *testing.T) {
	f := newSourceFixture(t)
	f.state.SourceSHA = shaA
	f.save()
	first := f.run("source", "v0", shaA)
	if first.err != nil || first.version != shaA {
		t.Fatalf("source install: version=%q err=%v output=%s", first.version, first.err, first.output)
	}
	first.assertBinaries(t, "development")
	again := f.run("source", "v0", shaA)
	if again.err != nil || again.version != shaA {
		t.Fatalf("reuse source selection: %+v", again)
	}
	trace, err := os.ReadFile(filepath.Join(f.jobDir("source"), "tool-trace"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(trace), "go:build") != 2 || strings.Contains(string(trace), "gh:") {
		t.Fatalf("source mode rebuilt within the job or contacted releases: %s", trace)
	}
	for _, selector := range []string{"", "v0", shaB, "v0.1.0"} {
		if got := f.run("source", "v0", selector); got.err == nil {
			t.Fatalf("source selection switched to %q inside one job", selector)
		}
	}
	next := f.run("fresh", "v0", shaA)
	if next.err != nil || next.version != shaA {
		t.Fatalf("fresh-job source install: %+v", next)
	}
	next.assertBinaries(t, "development")
}

func TestSourceSelectionRejectsChangedCheckoutAndFailedBuild(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sha   string
		dirty bool
		fail  bool
	}{
		{name: "wrong revision", sha: shaB},
		{name: "dirty checkout", sha: shaA, dirty: true},
		{name: "second binary fails", sha: shaA, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceFixture(t)
			f.state.SourceSHA = tc.sha
			f.state.SourceDirty = tc.dirty
			f.state.BuildFailure = tc.fail
			f.save()
			got := f.run("job", "v0", shaA)
			if got.err == nil {
				t.Fatal("invalid source installation succeeded")
			}
			for _, path := range []string{"sofa-cli", "sofa-setup-cli.selection"} {
				if _, err := os.Stat(filepath.Join(f.jobDir("job"), path)); !os.IsNotExist(err) {
					t.Fatalf("failed build published %s: %v", path, err)
				}
			}
		})
	}
}

func newSourceFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, ".sofa-cli-source", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"git", "go"} {
		if err := os.Symlink(self, filepath.Join(f.path, name)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
