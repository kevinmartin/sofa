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
	"regexp"
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

type fixture struct {
	t     *testing.T
	root  string
	state fakeState
	steps []actionStep
	path  string
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
	steps := readActionSteps(t)
	return &fixture{
		t:     t,
		root:  root,
		state: fakeState{Promoted: make(map[string]string)},
		steps: steps,
		path:  path,
	}
}

type actionStep struct {
	ID  string
	Run string
	If  string
	Env map[string]string
}

func readActionSteps(t *testing.T) []actionStep {
	t.Helper()
	path := filepath.Join(projectRoot(t), ".github", "actions", "setup-cli", "action.yml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var action struct{ Runs struct{ Steps []actionStep } }
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	return action.Runs.Steps
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

// Execute every script phase of the actual composite Action independently.
// Checkout/setup-go are third-party Actions: fake tools supply their prepared
// source/toolchain in unit tests; the hosted source proof exercises them live.
func (f *fixture) run(job, major, version string) runResult {
	f.t.Helper()
	jobDir := f.jobDir(job)
	if err := os.MkdirAll(jobDir, 0755); err != nil {
		f.t.Fatal(err)
	}
	pathPath := filepath.Join(jobDir, "path")
	outputs := make(map[string]map[string]string)
	var log strings.Builder
	var runErr error
	for _, step := range f.steps {
		if step.Run == "" {
			continue
		}
		if step.If != "" {
			match := regexp.MustCompile("^steps[.]([a-z_]+)[.]outputs[.]([a-z_]+) == '([^']*)'$").FindStringSubmatch(step.If)
			if match == nil {
				f.t.Fatalf("unsupported script condition %q", step.If)
			}
			if outputs[match[1]][match[2]] != match[3] {
				continue
			}
		}
		outPath := filepath.Join(jobDir, "output-"+step.ID)
		if err := os.WriteFile(outPath, nil, 0600); err != nil {
			f.t.Fatal(err)
		}
		cmd := exec.Command("bash", "-c", step.Run)
		cmd.Env = append(os.Environ(),
			"PATH="+f.path+":"+os.Getenv("PATH"),
			"SOFA_SETUP_FAKE_TOOL=1",
			"SOFA_FAKE_STATE="+filepath.Join(f.root, "state.json"),
			"GH_TOKEN=fixture-token",
			"GITHUB_TOKEN=fixture-token",
			"RUNNER_TEMP="+jobDir,
			"GITHUB_OUTPUT="+outPath,
			"GITHUB_PATH="+pathPath,
			"SOFA_FAKE_TRACE="+filepath.Join(jobDir, "tool-trace"),
		)
		for key, value := range step.Env {
			resolved := f.actionEnv(value, major, version, outputs)
			cmd.Env = append(cmd.Env, key+"="+resolved)
		}
		output, err := cmd.CombinedOutput()
		log.Write(output)
		if err != nil {
			runErr = err
			break
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			f.t.Fatal(err)
		}
		outputs[step.ID] = make(map[string]string)
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				outputs[step.ID][key] = value
			}
		}
	}
	installed := outputs["install"]
	result := runResult{
		version:      installed["version"],
		binDirectory: installed["bin_directory"],
		output:       log.String(),
		err:          runErr,
	}
	if data, err := os.ReadFile(pathPath); err == nil {
		result.path = strings.TrimSpace(string(data))
	}
	return result
}

func (f *fixture) actionEnv(value, major, version string, outputs map[string]map[string]string) string {
	f.t.Helper()
	values := map[string]string{
		"inputs.major":     major,
		"inputs.version":   version,
		"github.token":     "fixture-token",
		"github.workspace": f.root,
	}
	pattern := regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	return pattern.ReplaceAllStringFunc(value, func(expression string) string {
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expression, "${{"), "}}"))
		for _, part := range strings.Split(body, "||") {
			part = strings.TrimSpace(part)
			if result, exists := values[part]; exists {
				return result
			}
			match := regexp.MustCompile("^steps[.]([a-z_]+)[.]outputs[.]([a-z_]+)$").FindStringSubmatch(part)
			if match == nil {
				f.t.Fatalf("unsupported Action environment expression %q", part)
			}
			if result := outputs[match[1]][match[2]]; result != "" {
				return result
			}
		}
		return ""
	})
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
