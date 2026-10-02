package setupcli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	shaC = "cccccccccccccccccccccccccccccccccccccccc"
	shaD = "dddddddddddddddddddddddddddddddddddddddd"
)

type release struct {
	Tag        string `json:"tag"`
	SHA        string `json:"sha"`
	Bundle     string `json:"bundle"`
	Digest     string `json:"digest"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Immutable  bool   `json:"immutable"`
	Attested   bool   `json:"attested"`
}

type fakeState struct {
	Promoted map[string]string `json:"promoted"`
	Releases []release         `json:"releases"`
}

type fixture struct {
	t      *testing.T
	root   string
	state  fakeState
	script string
	path   string
}

func TestMain(m *testing.M) {
	if os.Getenv("SOFA_SETUP_FAKE_TOOL") != "" {
		os.Exit(fakeTool(os.Args[1:]))
	}
	os.Exit(m.Run())
}

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

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "tools")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gh", "tar", "uname"} {
		if err := os.Symlink(self, filepath.Join(path, name)); err != nil {
			t.Fatal(err)
		}
	}
	script := readActionScript(t)
	return &fixture{
		t:      t,
		root:   root,
		state:  fakeState{Promoted: make(map[string]string)},
		script: script,
		path:   path,
	}
}

func readActionScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate setup action test")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", ".github", "actions", "setup-cli", "action.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	if len(action.Runs.Steps) != 1 || action.Runs.Steps[0].Run == "" {
		t.Fatal("setup action has no single install script")
	}
	return action.Runs.Steps[0].Run
}

type tarMember struct {
	Name string
	Type byte
	Link string
	Body string
}

func regular(name, body string) tarMember {
	return tarMember{Name: name, Type: tar.TypeReg, Body: body}
}

func metadata(tag, sha string) string {
	return fmt.Sprintf(`{"schema_version":1,"version":%q,"source_sha":%q,"platform":"linux/amd64"}`, tag, sha)
}

func (f *fixture) addRelease(tag, sha string, members []tarMember) {
	f.t.Helper()
	if members == nil {
		members = []tarMember{
			regular("sofa", "#!/bin/sh\necho sofa-"+tag+"\n"),
			regular("sofa-test", "#!/bin/sh\necho sofa-test-"+tag+"\n"),
			regular("release.json", metadata(tag, sha)),
		}
	}
	bundle := f.writeBundle(tag, sha, members)
	data, err := os.ReadFile(bundle)
	if err != nil {
		f.t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	f.state.Releases = append(f.state.Releases, release{
		Tag:       tag,
		SHA:       sha,
		Bundle:    bundle,
		Digest:    hex.EncodeToString(digest[:]),
		Immutable: true,
		Attested:  true,
	})
}

func (f *fixture) addBuiltRelease(tag, sha string) {
	f.t.Helper()
	root := projectRoot(f.t)
	members := make([]tarMember, 0, 3)
	for _, name := range []string{"sofa", "sofa-test"} {
		path := filepath.Join(f.root, tag+"-"+name)
		flags := "-s -w -X github.com/kevinmartin/sofa/internal/version.Version=" + tag +
			" -X github.com/kevinmartin/sofa/internal/version.SourceCommit=" + sha
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", path, "./cmd/"+name)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatalf("build %s %s: %v\n%s", name, tag, err, output)
		}
		binary, err := os.ReadFile(path)
		if err != nil {
			f.t.Fatal(err)
		}
		members = append(members, regular(name, string(binary)))
	}
	members = append(members, regular("release.json", metadata(tag, sha)))
	f.addRelease(tag, sha, members)
}

func (f *fixture) writeBundle(tag, _ string, members []tarMember) string {
	f.t.Helper()
	path := filepath.Join(f.root, tag+".tar.gz")
	file, err := os.Create(path)
	if err != nil {
		f.t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, member := range members {
		h := &tar.Header{Name: member.Name, Typeflag: member.Type, Mode: 0755, Linkname: member.Link, Size: int64(len(member.Body))}
		if member.Type != tar.TypeReg {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			f.t.Fatal(err)
		}
		if h.Size != 0 {
			if _, err := io.WriteString(tw, member.Body); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	for _, closer := range []io.Closer{tw, gz, file} {
		if err := closer.Close(); err != nil {
			f.t.Fatal(err)
		}
	}
	return path
}

func (f *fixture) save() {
	f.t.Helper()
	data, err := json.Marshal(f.state)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "state.json"), data, 0600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) jobDir(job string) string {
	return filepath.Join(f.root, "job-"+strings.ReplaceAll(job, " ", "-"))
}

type runResult struct {
	version      string
	binDirectory string
	path         string
	output       string
	err          error
}

func (f *fixture) run(job, major, version string) runResult {
	f.t.Helper()
	jobDir := f.jobDir(job)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		f.t.Fatal(err)
	}
	outPath := filepath.Join(jobDir, "output")
	pathPath := filepath.Join(jobDir, "path")
	cmd := exec.Command("bash", "-c", f.script)
	cmd.Env = append(os.Environ(),
		"PATH="+f.path+":"+os.Getenv("PATH"),
		"SOFA_SETUP_FAKE_TOOL=1",
		"SOFA_FAKE_STATE="+filepath.Join(f.root, "state.json"),
		"SOFA_SETUP_MAJOR="+major,
		"SOFA_SETUP_VERSION="+version,
		"RUNNER_TEMP="+jobDir,
		"GITHUB_OUTPUT="+outPath,
		"GITHUB_PATH="+pathPath,
		"SOFA_FAKE_TRACE="+filepath.Join(jobDir, "tool-trace"),
	)
	output, err := cmd.CombinedOutput()
	result := runResult{output: string(output), err: err}
	if data, readErr := os.ReadFile(outPath); readErr == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "release_version=") {
				result.version = strings.TrimPrefix(line, "release_version=")
			}
			if strings.HasPrefix(line, "bin_directory=") {
				result.binDirectory = strings.TrimPrefix(line, "bin_directory=")
			}
		}
	}
	if data, readErr := os.ReadFile(pathPath); readErr == nil {
		result.path = strings.TrimSpace(string(data))
	}
	return result
}

func (r runResult) assertBinaries(t *testing.T, version string) {
	t.Helper()
	if r.binDirectory != r.path || filepath.Base(r.path) != "sofa-cli" {
		t.Fatalf("bin_directory output and PATH disagree: output=%q, PATH=%q", r.binDirectory, r.path)
	}
	for _, name := range []string{"sofa", "sofa-test"} {
		cmd := exec.Command(filepath.Join(r.path, name))
		output, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(output)) != name+"-"+version {
			t.Fatalf("%s execution: output=%q, err=%v", name, output, err)
		}
	}
}

func (r runResult) assertRealVersions(t *testing.T, tag, sha string) {
	t.Helper()
	for _, name := range []string{"sofa", "sofa-test"} {
		cmd := exec.Command(filepath.Join(r.path, name), "version")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s version: %v\n%s", name, err, output)
		}
		var info struct {
			Version      string `json:"version"`
			SourceCommit string `json:"source_commit"`
		}
		if err := json.Unmarshal(output, &info); err != nil || info.Version != tag || info.SourceCommit != sha {
			t.Fatalf("%s installed version: got %+v, parse error %v", name, info, err)
		}
	}
}

func (r runResult) roundTripState(t *testing.T, input, output string) {
	t.Helper()
	args := []string{"distribution", "compatibility-fixture"}
	if input != "" {
		args = append(args, "--input", input)
	}
	args = append(args, "--output", output)
	cmd := exec.Command(filepath.Join(r.path, "sofa-test"), args...)
	if log, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compatibility fixture %v: %v\n%s", args, err, log)
	}
}

func assertSameFile(t *testing.T, first, second string) {
	t.Helper()
	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("config, ownership, checkpoint, or cumulative counter bytes changed across CLI versions")
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate setup-cli source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func fakeTool(args []string) int {
	call := filepath.Base(os.Args[0])
	for i, arg := range args {
		if i == 2 {
			break
		}
		call += ":" + arg
	}
	if trace := os.Getenv("SOFA_FAKE_TRACE"); trace != "" {
		file, err := os.OpenFile(trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 2
		}
		if _, err := fmt.Fprintln(file, call); err != nil {
			file.Close()
			return 2
		}
		if err := file.Close(); err != nil {
			return 2
		}
	}
	switch filepath.Base(os.Args[0]) {
	case "uname":
		if len(args) == 1 && args[0] == "-s" {
			fmt.Println("Linux")
			return 0
		}
		if len(args) == 1 && args[0] == "-m" {
			fmt.Println("x86_64")
			return 0
		}
	case "tar":
		return fakeTar(args)
	case "gh":
		return fakeGH(args)
	}
	fmt.Fprintln(os.Stderr, "unexpected fake tool call", filepath.Base(os.Args[0]), args)
	return 2
}

func fakeGH(args []string) int {
	data, err := os.ReadFile(os.Getenv("SOFA_FAKE_STATE"))
	if err != nil {
		return 2
	}
	var state fakeState
	if json.Unmarshal(data, &state) != nil {
		return 2
	}
	if len(args) == 0 {
		return 2
	}
	if args[0] == "api" {
		endpoint := ""
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
				break
			}
		}
		if strings.HasPrefix(endpoint, "repos/kevinmartin/sofa/commits/") {
			ref := strings.TrimPrefix(endpoint, "repos/kevinmartin/sofa/commits/")
			if sha := state.Promoted[ref]; sha != "" {
				fmt.Println(sha)
				return 0
			}
			for _, release := range state.Releases {
				if release.Tag == ref {
					fmt.Println(release.SHA)
					return 0
				}
			}
			return 1
		}
		if strings.HasPrefix(endpoint, "repos/kevinmartin/sofa/releases/tags/") {
			ref := strings.TrimPrefix(endpoint, "repos/kevinmartin/sofa/releases/tags/")
			for _, release := range state.Releases {
				if release.Tag == ref {
					json.NewEncoder(os.Stdout).Encode(map[string]any{"tag_name": ref, "draft": release.Draft, "prerelease": release.Prerelease, "immutable": release.Immutable})
					return 0
				}
			}
			return 1
		}
		if endpoint == "repos/kevinmartin/sofa/releases?per_page=100" {
			for _, release := range state.Releases {
				if !release.Draft && !release.Prerelease {
					fmt.Println(release.Tag)
				}
			}
			return 0
		}
	}
	if len(args) >= 3 && args[0] == "release" {
		ref := args[2]
		for _, release := range state.Releases {
			if release.Tag != ref {
				continue
			}
			switch args[1] {
			case "download":
				for i := 3; i+1 < len(args); i++ {
					if args[i] == "--dir" {
						content, err := os.ReadFile(release.Bundle)
						if err != nil {
							return 1
						}
						if os.WriteFile(filepath.Join(args[i+1], "sofa-linux-amd64.tar.gz"), content, 0600) != nil {
							return 1
						}
						return 0
					}
				}
			case "verify-asset":
				if len(args) < 4 {
					return 2
				}
				if !release.Attested {
					fmt.Fprintln(os.Stderr, "attestation unavailable")
					return 1
				}
				content, err := os.ReadFile(args[3])
				if err != nil {
					return 1
				}
				digest := sha256.Sum256(content)
				if hex.EncodeToString(digest[:]) != release.Digest {
					fmt.Fprintln(os.Stderr, "verification failed")
					return 1
				}
				return 0
			}
		}
	}
	fmt.Fprintln(os.Stderr, "unexpected gh call", args)
	return 2
}

func fakeTar(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("tar (GNU tar) test double")
		return 0
	}
	archive := ""
	directory := ""
	verbose := false
	extract := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			i++
			if i < len(args) {
				archive = args[i]
			}
		case "--directory":
			i++
			if i < len(args) {
				directory = args[i]
			}
		case "--verbose":
			verbose = true
		case "--extract":
			extract = true
		}
	}
	file, err := os.Open(archive)
	if err != nil {
		return 1
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 1
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return 0
		}
		if err != nil {
			return 1
		}
		if extract {
			// Only safe bundles reach extraction in the test. Never follow links.
			if h.Typeflag != tar.TypeReg || strings.Contains(h.Name, "..") || strings.Contains(h.Name, "/") {
				return 1
			}
			content, err := io.ReadAll(tr)
			if err != nil || os.WriteFile(filepath.Join(directory, h.Name), content, 0600) != nil {
				return 1
			}
			continue
		}
		if verbose {
			kind := byte('-')
			if h.Typeflag == tar.TypeSymlink {
				kind = 'l'
			} else if h.Typeflag == tar.TypeLink {
				kind = 'h'
			}
			fmt.Printf("%crwxr-xr-x test test %d Jan 1 00:00 %s\n", kind, h.Size, h.Name)
		} else {
			escaped := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\t", "\\t", "\r", "\\r").Replace(h.Name)
			fmt.Println(escaped)
		}
	}
}
