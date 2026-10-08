package setupcli

import (
	"archive/tar"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPromotedReleaseAndVersionHandoff(t *testing.T) {
	f := newFixture(t)
	f.addRelease("v0.1.0", shaA, nil)
	f.addRelease("v0.1.1", shaB, nil)
	f.addRelease("v0.1.2", shaD, nil) // Published, but not promoted.
	f.addRelease("v1.0.0", shaC, nil)
	f.addRelease("v2.0.0", shaC, nil) // Same source cannot cross a major boundary.
	f.state.Promoted["v0"] = shaB
	f.state.Promoted["v1"] = shaC
	f.save()

	first := f.run("first", "v0", "")
	if first.version != "v0.1.1" || first.err != nil {
		t.Fatalf("promoted v0 install: version=%q, err=%v, output=%s", first.version, first.err, first.output)
	}
	first.assertBinaries(t, "v0.1.1")

	// The channel advances while the first job is alive. Its second setup call
	// retains the exact selection; a later job takes the newly promoted release.
	f.state.Promoted["v0"] = shaD
	f.save()
	again := f.run("first", "v0", "")
	if again.err != nil || again.version != "v0.1.1" {
		t.Fatalf("same-job selection changed: version=%q, err=%v, output=%s", again.version, again.err, again.output)
	}
	conflict := f.run("first", "v0", "v0.1.2")
	if conflict.err == nil {
		t.Fatalf("same-job conflicting version was accepted: %s", conflict.output)
	}
	next := f.run("next", "v0", "")
	if next.err != nil || next.version != "v0.1.2" {
		t.Fatalf("later job missed promotion: version=%q, err=%v, output=%s", next.version, next.err, next.output)
	}
	next.assertBinaries(t, "v0.1.2")
	handoff := f.run("handoff", "v0", first.version)
	if handoff.err != nil || handoff.version != "v0.1.1" {
		t.Fatalf("exact handoff failed: version=%q, err=%v, output=%s", handoff.version, handoff.err, handoff.output)
	}
	handoff.assertBinaries(t, "v0.1.1")
	v1 := f.run("v1", "v1", "")
	if v1.err != nil || v1.version != "v1.0.0" {
		t.Fatalf("v1 selected wrong release: version=%q, err=%v, output=%s", v1.version, v1.err, v1.output)
	}
	v1.assertBinaries(t, "v1.0.0")
}

func TestRetainedReleaseAwareInstallerSelectsCurrentPromotionOnRerun(t *testing.T) {
	f := newFixture(t)
	f.addRelease("v0.1.0", shaA, nil)
	f.addRelease("v0.1.1", shaB, nil)
	f.state.Promoted["v0"] = shaA
	f.save()
	initial := f.run("original workflow revision", "v0", "")
	if initial.err != nil || initial.version != "v0.1.0" {
		t.Fatalf("initial release-aware workflow: version=%q, err=%v, output=%s", initial.version, initial.err, initial.output)
	}

	// The same installer script stands for an earlier release-aware workflow
	// revision. GitHub's full rerun resolves its reusable workflow ref again;
	// failed-job and specific-job reruns retain that revision. Every rerun still
	// starts a fresh job, so runtime major-tag resolution must see promotion B.
	f.state.Promoted["v0"] = shaB
	f.save()
	for _, mode := range []string{"full rerun", "failed-job rerun", "specific-job rerun"} {
		got := f.run(mode, "v0", "")
		if got.err != nil || got.version != "v0.1.1" {
			t.Fatalf("%s failed current runtime selection: version=%q, err=%v, output=%s", mode, got.version, got.err, got.output)
		}
		got.assertBinaries(t, "v0.1.1")
	}
}

func TestPromotedMajorRequiresOnePublishedRelease(t *testing.T) {
	for _, tc := range []struct {
		name     string
		releases []string
		promoted string
	}{
		{name: "unpromoted published release", releases: []string{"v0.1.0"}, promoted: shaB},
		{name: "ambiguous exact releases", releases: []string{"v0.1.0", "v0.1.1"}, promoted: shaA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			for _, tag := range tc.releases {
				f.addRelease(tag, shaA, nil)
			}
			f.state.Promoted["v0"] = tc.promoted
			f.save()
			got := f.run("job", "v0", "")
			if got.err == nil || got.version != "" {
				t.Fatalf("promoted ref without unique release installed: version=%q, output=%s", got.version, got.output)
			}
		})
	}
}

func TestInstalledRealCLIsSurviveUpgradeHandoffAndRollback(t *testing.T) {
	f := newFixture(t)
	f.addBuiltRelease("v0.1.0", shaA)
	f.addBuiltRelease("v0.1.1", shaB)
	f.state.Promoted["v0"] = shaA
	f.save()

	before := f.run("before", "v0", "")
	if before.err != nil || before.version != "v0.1.0" {
		t.Fatalf("install initial CLI: version=%q, err=%v, output=%s", before.version, before.err, before.output)
	}
	before.assertRealVersions(t, "v0.1.0", shaA)
	first := filepath.Join(f.root, "first-state.json")
	before.roundTripState(t, "", first)

	f.state.Promoted["v0"] = shaB
	f.save()
	after := f.run("after", "v0", "")
	if after.err != nil || after.version != "v0.1.1" {
		t.Fatalf("install upgraded CLI: version=%q, err=%v, output=%s", after.version, after.err, after.output)
	}
	after.assertRealVersions(t, "v0.1.1", shaB)
	second := filepath.Join(f.root, "second-state.json")
	after.roundTripState(t, first, second)
	assertSameFile(t, first, second)

	// A dependent job may pin A even though the channel now promotes B.
	pinned := f.run("explicit handoff", "v0", before.version)
	if pinned.err != nil || pinned.version != before.version {
		t.Fatalf("exact handoff: version=%q, err=%v, output=%s", pinned.version, pinned.err, pinned.output)
	}
	pinned.assertRealVersions(t, "v0.1.0", shaA)

	// Rolling the channel back selects the prior immutable release; it must
	// still consume the state last written by the newer labeled binaries.
	f.state.Promoted["v0"] = shaA
	f.save()
	rolled := f.run("rollback", "v0", "")
	if rolled.err != nil || rolled.version != "v0.1.0" {
		t.Fatalf("rollback install: version=%q, err=%v, output=%s", rolled.version, rolled.err, rolled.output)
	}
	rolled.assertRealVersions(t, "v0.1.0", shaA)
	third := filepath.Join(f.root, "third-state.json")
	rolled.roundTripState(t, second, third)
	assertSameFile(t, first, third)
}

func TestSelectionAndVerificationFailClosed(t *testing.T) {
	f := newFixture(t)
	f.addRelease("v0.1.0", shaA, nil)
	f.addRelease("v1.0.0", shaC, nil)
	f.addRelease("v2.0.0", shaD, nil)
	f.state.Promoted["v0"] = shaA
	f.state.Promoted["v1"] = shaC
	f.save()
	for _, tc := range []struct {
		name    string
		major   string
		version string
	}{
		{name: "malformed major", major: "v0;echo bad"},
		{name: "malformed exact", major: "v0", version: "v0.01.0"},
		{name: "wrong major", major: "v0", version: "v1.0.0"},
		{name: "future major", major: "v1", version: "v2.0.0"},
		{name: "unknown exact", major: "v0", version: "v0.2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.run(tc.name, tc.major, tc.version)
			if got.err == nil || got.version != "" {
				t.Fatalf("invalid request installed a release: version=%q, output=%s", got.version, got.output)
			}
		})
	}

	// Signed asset verification runs before extraction. A changed archive must
	// not put either executable on PATH.
	f.writeBundle("v0.1.0", shaA, []tarMember{
		regular("sofa", "#!/bin/sh\necho changed\n"),
		regular("sofa-test", "#!/bin/sh\necho changed\n"),
		regular("release.json", metadata("v0.1.0", shaA)),
	})
	failed := f.run("tamper", "v0", "")
	if failed.err == nil || failed.version != "" || !strings.Contains(failed.output, "verification failed") {
		t.Fatalf("tampered asset passed: err=%v, output=%s", failed.err, failed.output)
	}
	if _, err := os.Stat(filepath.Join(f.jobDir("tamper"), "sofa-cli", "sofa")); !os.IsNotExist(err) {
		t.Fatalf("tamper installed executable: %v", err)
	}
	trace, err := os.ReadFile(filepath.Join(f.jobDir("tamper"), "tool-trace"))
	if err != nil || !strings.Contains(string(trace), "gh:release:verify-asset") || strings.Contains(string(trace), "tar:") {
		t.Fatalf("archive was inspected before signed verification: trace=%q, err=%v", trace, err)
	}
}

func TestArchiveAndReleaseMetadataRejected(t *testing.T) {
	cases := []struct {
		name    string
		members []tarMember
	}{
		{name: "traversal", members: []tarMember{regular("sofa", "binary"), regular("sofa-test", "binary"), regular("../release.json", "{}")}},
		{name: "absolute path", members: []tarMember{regular("/sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "nested path", members: []tarMember{regular("bin/sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "control character", members: []tarMember{regular("sofa\n", "binary"), regular("sofa-test", "binary"), regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "symlink", members: []tarMember{regular("sofa", "binary"), {Name: "sofa-test", Type: tar.TypeSymlink, Link: "/bin/sh"}, regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "hardlink", members: []tarMember{regular("sofa", "binary"), {Name: "sofa-test", Type: tar.TypeLink, Link: "sofa"}, regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "duplicate", members: []tarMember{regular("sofa", "binary"), regular("sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "missing member", members: []tarMember{regular("sofa", "binary"), regular("release.json", metadata("v0.1.0", shaA))}},
		{name: "wrong metadata", members: []tarMember{regular("sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", metadata("v0.1.0", shaB))}},
		{name: "wrong platform", members: []tarMember{regular("sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", `{"schema_version":1,"version":"v0.1.0","source_sha":"`+shaA+`","platform":"darwin/arm64"}`)}},
		{name: "wrong schema", members: []tarMember{regular("sofa", "binary"), regular("sofa-test", "binary"), regular("release.json", `{"schema_version":2,"version":"v0.1.0","source_sha":"`+shaA+`","platform":"linux/amd64"}`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.addRelease("v0.1.0", shaA, tc.members)
			f.state.Promoted["v0"] = shaA
			f.save()
			got := f.run("job", "v0", "")
			if got.err == nil || got.version != "" {
				t.Fatalf("bad archive installed: version=%q, output=%s", got.version, got.output)
			}
		})
	}
}

func TestUnavailableReleaseFailsBeforeInstall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*release)
	}{
		{name: "mutable release", change: func(r *release) { r.Immutable = false }},
		{name: "draft release", change: func(r *release) { r.Draft = true }},
		{name: "prerelease", change: func(r *release) { r.Prerelease = true }},
		{name: "missing attestation", change: func(r *release) { r.Attested = false }},
		{name: "missing asset", change: func(r *release) { r.Bundle = "/missing/sofa-linux-amd64.tar.gz" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.addRelease("v0.1.0", shaA, nil)
			f.state.Promoted["v0"] = shaA
			tc.change(&f.state.Releases[0])
			f.save()
			got := f.run("job", "v0", "v0.1.0")
			if got.err == nil || got.version != "" {
				t.Fatalf("unavailable exact release installed: version=%q, output=%s", got.version, got.output)
			}
			if _, err := os.Stat(filepath.Join(f.jobDir("job"), "sofa-cli", "sofa")); !os.IsNotExist(err) {
				t.Fatalf("unavailable release installed executable: %v", err)
			}
		})
	}
}

func TestExistingDestinationWithoutSelectionIsNotOverwritten(t *testing.T) {
	f := newFixture(t)
	f.addRelease("v0.1.0", shaA, nil)
	f.state.Promoted["v0"] = shaA
	f.save()
	bin := filepath.Join(f.jobDir("job"), "sofa-cli")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(bin, "sofa")
	if err := os.WriteFile(marker, []byte("existing binary"), 0700); err != nil {
		t.Fatal(err)
	}
	got := f.run("job", "v0", "")
	if got.err == nil || got.version != "" {
		t.Fatalf("preexisting destination was accepted: version=%q, output=%s", got.version, got.output)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "existing binary" {
		t.Fatalf("installer changed preexisting destination: data=%q, err=%v", data, err)
	}
}

func TestActualGNUTarAcceptsOnlyExpectedMembers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("GNU tar archive parsing is exercised on Linux runners")
	}
	f := newFixture(t)
	if err := os.Remove(filepath.Join(f.path, "tar")); err != nil {
		t.Fatal(err)
	}
	f.addRelease("v0.1.0", shaA, nil)
	f.state.Promoted["v0"] = shaA
	f.save()
	valid := f.run("valid", "v0", "")
	if valid.err != nil || valid.version != "v0.1.0" {
		t.Fatalf("real GNU tar rejected valid signed bundle: version=%q, err=%v, output=%s", valid.version, valid.err, valid.output)
	}

	bad := newFixture(t)
	if err := os.Remove(filepath.Join(bad.path, "tar")); err != nil {
		t.Fatal(err)
	}
	bad.addRelease("v0.1.0", shaA, []tarMember{
		regular("sofa\n", "binary"),
		regular("sofa-test", "binary"),
		regular("release.json", metadata("v0.1.0", shaA)),
	})
	bad.state.Promoted["v0"] = shaA
	bad.save()
	unsafe := bad.run("unsafe", "v0", "")
	if unsafe.err == nil || unsafe.version != "" {
		t.Fatalf("real GNU tar accepted control-character archive member: version=%q, output=%s", unsafe.version, unsafe.output)
	}
}

func TestMajorChannelVersionSelector(t *testing.T) {
	f := newFixture(t)
	f.addRelease("v0.1.0", shaA, nil)
	f.addRelease("v1.0.0", shaB, nil)
	f.state.Promoted["v0"] = shaA
	f.state.Promoted["v1"] = shaB
	f.save()
	for _, tc := range []struct {
		name     string
		guard    string
		selector string
		want     string
	}{
		{name: "explicit v0", selector: "v0", want: "v0.1.0"},
		{name: "explicit v1", selector: "v1", want: "v1.0.0"},
		{name: "default", want: "v0.1.0"},
		{name: "exact release", selector: "v1.0.0", want: "v1.0.0"},
		{name: "workflow major guard", guard: "v0", selector: "v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := f.run(tc.name, tc.guard, tc.selector)
			if tc.want == "" {
				if got.err == nil {
					t.Fatal("workflow accepted another major")
				}
				return
			}
			if got.err != nil || got.version != tc.want {
				t.Fatalf("selector %q: version=%q err=%v output=%s", tc.selector, got.version, got.err, got.output)
			}
			got.assertBinaries(t, tc.want)
		})
	}
}
