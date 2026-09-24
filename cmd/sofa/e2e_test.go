package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
	"github.com/kevinmartin/sofa/internal/worker"
)

func TestAdmissionDenialMatrix(t *testing.T) {
	c, m := testManifest(t)
	now := m.Grant.StatusUpdatedAt
	snapshot := admission.Snapshot{
		Repository: c.Repository, RepositoryID: c.RepositoryID,
		IssueID: m.Grant.IssueID, Number: m.Grant.IssueNumber,
		Title: "Fixture", Body: "Change the fixture", Open: true,
		ProjectID: c.ProjectID, ProjectPrivate: true,
		ProjectItemID: m.Grant.ProjectItemID, CurrentStatus: c.ReadyStatus,
		StatusOptionID: m.Grant.StatusOptionID, StatusUpdatedAt: now,
		IssueLastEditedAt: now.Add(-time.Minute), BaseSHA: m.Grant.BaseSHA,
		Complete: true,
	}
	cases := []struct {
		name   string
		change func(*admission.Snapshot)
	}{
		{"non-ready", func(s *admission.Snapshot) { s.CurrentStatus = "Todo" }},
		{"edited-after-ready", func(s *admission.Snapshot) { s.IssueLastEditedAt = now.Add(time.Second) }},
		{"ambiguous-project", func(s *admission.Snapshot) { s.Complete = false }},
		{"foreign-repository", func(s *admission.Snapshot) { s.RepositoryID = "foreign" }},
		{"malformed-specification", func(s *admission.Snapshot) { s.Body = "" }},
		{"closed-issue", func(s *admission.Snapshot) { s.Open = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := snapshot
			tc.change(&observed)
			grant, spec, err := admission.Authorize(c, observed)
			if err == nil || grant != (admission.Grant{}) || len(spec) != 0 {
				t.Fatalf("denied input returned usable authority: grant=%+v, spec length=%d, error=%v", grant, len(spec), err)
			}
		})
	}
}

func TestNoChangeACPTurnCannotPublish(t *testing.T) {
	for _, key := range []string{"SOFA_MODEL_TOKEN", "SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	c, m := testManifest(t)
	root := e2eFixture(t)
	baseSHA := e2eGit(t, root, "rev-parse", "HEAD")
	t.Setenv("SOFA_COPILOT_PATH", e2eFakeACP(t, "no-change"))
	t.Setenv("SOFA_COPILOT_ENTRY", "")
	result, err := worker.Execute(context.Background(), worker.Input{Config: c, CanonicalSpec: m.CanonicalSpec, Directory: e2eClone(t, root), AttemptID: m.Fence.AttemptID, Generation: 1, BaseSHA: baseSHA, ModelToken: "sofa-fake-acp-inert-token"})
	if err != nil || !result.UsedAgent || result.PromptRequests != 1 || !result.NoChange || len(result.Bundle.Files) != 0 {
		t.Fatalf("no-change turn was not measured and rejected as candidate: %+v, %v", result, err)
	}
	fixture := &e2ePublisher{repo: c.Repository, baseSHA: baseSHA}
	client, err := github.New("inert-publisher-token", fixture)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.PublishDraft(context.Background(), github.PublishInput{Bundle: result.Bundle, Expected: integrity.Expected{Repository: c.Repository, AttemptID: m.Fence.AttemptID, Generation: 1, BaseSHA: baseSHA}, Policy: integrity.Policy{AllowedPaths: c.AllowedPaths, MaxFiles: c.Limits.MaxFiles, MaxFileBytes: c.Limits.MaxFileBytes, MaxTotalBytes: c.Limits.MaxTotalBytes}, RequiredChecks: []string{"go-test"}, BaseBranch: "main", Title: "Fixture", Body: "No change", Guard: func(context.Context) error { return nil }})
	if err == nil || fixture.writes != 0 || fixture.prPosts != 0 {
		t.Fatalf("empty candidate reached GitHub mutation: %v, writes=%d, PRs=%d", err, fixture.writes, fixture.prPosts)
	}
}

// TestHostedArtifactPublication is invoked only by the secretless disposable
// canary. It uses candidate-produced artifacts from separate hosted jobs, but
// all GitHub writes are simulated by the bounded transport below. The trusted
// fixture coordinator separately verifies this result before any real PR write.
func TestHostedArtifactPublication(t *testing.T) {
	resultPath := os.Getenv("SOFA_E2E_RESULT")
	if resultPath == "" {
		t.Skip("hosted artifact paths are not configured")
	}
	required := []string{"SOFA_E2E_CONFIG", "SOFA_E2E_MANIFEST", "SOFA_E2E_BUNDLE", "SOFA_E2E_EVIDENCE", "SOFA_E2E_BASE_ROOT"}
	for _, name := range required {
		if os.Getenv(name) == "" {
			t.Fatalf("hosted fixture missing %s", name)
		}
	}
	c, err := readConfig(os.Getenv("SOFA_E2E_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, true); err != nil {
		t.Fatal(err)
	}
	m, err := readManifest(os.Getenv("SOFA_E2E_MANIFEST"), c)
	if err != nil {
		t.Fatal(err)
	}
	b, err := readBundle(os.Getenv("SOFA_E2E_BUNDLE"))
	if err != nil {
		t.Fatal(err)
	}
	var checks []integrity.CheckEvidence
	if err := readJSON(os.Getenv("SOFA_E2E_EVIDENCE"), 1<<20, &checks); err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 1 || b.Files[0].Path != "fixture/greeting.go" || b.Files[0].Operation != "update" {
		t.Fatal("hosted fixture requires one approved greeting update")
	}
	expected := bundleExpected(m, b)
	if err := integrity.Validate(b, expected, bundlePolicy(c)); err != nil {
		t.Fatal(err)
	}
	requiredChecks := make([]string, 0, len(c.Checks))
	for _, check := range c.Checks {
		requiredChecks = append(requiredChecks, check.ID)
	}
	if err := integrity.ValidateEvidence(b, checks, requiredChecks); err != nil {
		t.Fatal(err)
	}
	baseRoot := os.Getenv("SOFA_E2E_BASE_ROOT")
	if got := e2eGit(t, baseRoot, "rev-parse", "HEAD"); got != b.BaseSHA || got != m.Grant.BaseSHA {
		t.Fatalf("hosted fixture checkout is not the admitted base: %s", got)
	}
	fixture := &e2ePublisher{repo: c.Repository, baseSHA: b.BaseSHA, original: e2eRead(t, filepath.Join(baseRoot, "fixture/greeting.go")), candidate: b.Files[0].Content, branch: "sofa/" + b.AttemptID[:24]}
	client, err := github.New("inert-publisher-token", fixture)
	if err != nil {
		t.Fatal(err)
	}
	input := github.PublishInput{Bundle: b, Expected: expected, Policy: bundlePolicy(c), Checks: checks, RequiredChecks: requiredChecks, BaseBranch: "main", Title: "Fixture", Body: "Simulated hosted publication", Guard: func(context.Context) error { return nil }}
	pr, err := client.PublishDraft(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	writes := fixture.writes
	second, err := client.PublishDraft(context.Background(), input)
	if err != nil || second != pr || fixture.writes != writes || fixture.prPosts != 1 {
		t.Fatalf("hosted fake publication replay was not idempotent: %+v, %v, %d/%d, PR posts %d", second, err, fixture.writes, writes, fixture.prPosts)
	}
	report := struct {
		SchemaVersion    int    `json:"schema_version"`
		Simulation       string `json:"simulation"`
		CandidateDigest  string `json:"candidate_digest"`
		BaseSHA          string `json:"base_sha"`
		AttemptID        string `json:"attempt_id"`
		Generation       uint64 `json:"generation"`
		PRNumber         int64  `json:"pr_number"`
		PRURL            string `json:"pr_url"`
		Branch           string `json:"branch"`
		CommitSHA        string `json:"commit_sha"`
		FakeGitWrites    int    `json:"fake_git_writes"`
		PRPosts          int    `json:"pr_posts"`
		ProviderRequests int    `json:"provider_requests"`
	}{1, "fake-github-transport", b.CandidateDigest, b.BaseSHA, b.AttemptID, b.Generation, pr.Number, pr.URL, pr.Branch, pr.CommitSHA, fixture.writes, fixture.prPosts, fixture.providerRequests}
	if err := writeJSON(resultPath, report); err != nil {
		t.Fatal(err)
	}
}

// TestDeliveryBoundaryMatrix follows one admitted fixture through the real
// worker, secretless verifier and GitHub publisher boundaries. Only the ACP
// peer and GitHub transport are fake; the latter records every external write.
func TestDeliveryBoundaryMatrix(t *testing.T) {
	for _, key := range []string{"SOFA_MODEL_TOKEN", "SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(key, "")
	}
	ctx := context.Background()
	c, m := testManifest(t)
	baseRoot := e2eFixture(t)
	baseSHA := e2eGit(t, baseRoot, "rev-parse", "HEAD")
	m.Grant.BaseSHA = baseSHA
	ledger := state.Engine{Store: &state.MemoryStore{}, Now: func() time.Time { return time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC) }}
	a, created, err := ledger.Admit(ctx, ledgerAdmission(m.Grant), state.Limits{ModelCalls: 1, InfrastructureRetries: 1, RuntimeSeconds: 600})
	if err != nil || !created {
		t.Fatalf("admission: %v, created=%v", err, created)
	}
	if replay, newWork, err := ledger.Admit(ctx, ledgerAdmission(m.Grant), a.Limits); err != nil || newWork || replay.ID != a.ID {
		t.Fatalf("admission replay dispatched new work: %+v, %v, %v", replay, newWork, err)
	}
	fence, err := ledger.Claim(ctx, a.ID, m.Fence.Owner)
	if err != nil {
		t.Fatal(err)
	}
	m.Fence = fence
	if err := ledger.Charge(ctx, fence, state.Counters{ModelCalls: 1, RuntimeSeconds: 60}); err != nil {
		t.Fatal(err)
	}

	workerRoot := e2eClone(t, baseRoot)
	t.Setenv("SOFA_COPILOT_PATH", e2eFakeACP(t, "edit"))
	t.Setenv("SOFA_COPILOT_ENTRY", "")
	result, err := worker.Execute(ctx, worker.Input{Config: c, CanonicalSpec: m.CanonicalSpec, Directory: workerRoot, AttemptID: a.ID, Generation: uint64(fence.Generation), BaseSHA: baseSHA, ModelToken: "sofa-fake-acp-inert-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UsedAgent || result.PromptRequests != 1 || result.ModelCalls != nil || result.NoChange || len(result.Bundle.Files) != 1 {
		t.Fatalf("fake ACP edit did not produce one bounded candidate: %+v", result)
	}
	if got := result.Bundle.Files[0].Path; got != "fixture/greeting.go" {
		t.Fatalf("fake peer edited %s", got)
	}

	verifyRoot := e2eClone(t, baseRoot)
	transport := t.TempDir()
	manifestPath, bundlePath, evidencePath := filepath.Join(transport, "manifest.json"), filepath.Join(transport, "bundle.json"), filepath.Join(transport, "evidence.json")
	if err := writeJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(bundlePath, result.Bundle); err != nil {
		t.Fatal(err)
	}
	if err := verify(ctx, []string{"--config", filepath.Join(verifyRoot, ".sofa.yml"), "--manifest", manifestPath, "--workspace", verifyRoot, "--bundle", bundlePath, "--out", evidencePath}); err != nil {
		t.Fatalf("secretless verification: %v", err)
	}
	var checks []integrity.CheckEvidence
	if err := readJSON(evidencePath, 1<<20, &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || !checks[0].Passed || checks[0].CandidateDigest != result.Bundle.CandidateDigest {
		t.Fatalf("check did not bind exact candidate digest: %+v", checks)
	}
	hostedReport := filepath.Join(transport, "hosted-publish.json")
	for name, value := range map[string]string{
		"SOFA_E2E_CONFIG":    filepath.Join(baseRoot, ".sofa.yml"),
		"SOFA_E2E_MANIFEST":  manifestPath,
		"SOFA_E2E_BUNDLE":    bundlePath,
		"SOFA_E2E_EVIDENCE":  evidencePath,
		"SOFA_E2E_BASE_ROOT": baseRoot,
		"SOFA_E2E_RESULT":    hostedReport,
	} {
		t.Setenv(name, value)
	}
	TestHostedArtifactPublication(t)
	var hosted struct {
		Simulation       string `json:"simulation"`
		CandidateDigest  string `json:"candidate_digest"`
		BaseSHA          string `json:"base_sha"`
		PRPosts          int    `json:"pr_posts"`
		ProviderRequests int    `json:"provider_requests"`
	}
	hostedBytes := e2eRead(t, hostedReport)
	if err := json.Unmarshal(hostedBytes, &hosted); err != nil || hosted.Simulation != "fake-github-transport" || hosted.CandidateDigest != result.Bundle.CandidateDigest || hosted.BaseSHA != baseSHA || hosted.PRPosts != 1 || hosted.ProviderRequests != 0 {
		t.Fatalf("hosted artifact report did not bind candidate and one simulated PR: %+v, %v", hosted, err)
	}

	fixture := &e2ePublisher{repo: c.Repository, baseSHA: baseSHA, original: e2eRead(t, filepath.Join(baseRoot, "fixture/greeting.go")), candidate: result.Bundle.Files[0].Content, branch: "sofa/" + a.ID[:24]}
	client, err := github.New("inert-publisher-token", fixture)
	if err != nil {
		t.Fatal(err)
	}
	guard := func(ctx context.Context) error { return ledger.AssertOwner(ctx, fence) }
	input := github.PublishInput{Bundle: result.Bundle, Expected: integrity.Expected{Repository: c.Repository, AttemptID: a.ID, Generation: uint64(fence.Generation), BaseSHA: baseSHA, CandidateDigest: result.Bundle.CandidateDigest}, Policy: integrity.Policy{AllowedPaths: c.AllowedPaths, MaxFiles: c.Limits.MaxFiles, MaxFileBytes: c.Limits.MaxFileBytes, MaxTotalBytes: c.Limits.MaxTotalBytes}, Checks: checks, RequiredChecks: []string{"go-test"}, BaseBranch: "main", Title: "Fixture", Body: "Validated fixture", Guard: guard}
	fixture.mainOverride = strings.Repeat("9", 40)
	if _, err := client.PublishDraft(ctx, input); err == nil || fixture.writes != 0 {
		t.Fatalf("changed default-branch base allowed a write: %v, writes=%d", err, fixture.writes)
	}
	fixture.mainOverride = ""
	if err := ledger.Advance(ctx, fence, state.Validating); err != nil {
		t.Fatal(err)
	}
	intent := state.Publication{Branch: fixture.branch, ExpectedHead: baseSHA, CandidateDigest: result.Bundle.CandidateDigest}
	if err := ledger.BeginPublication(ctx, fence, intent); err != nil {
		t.Fatal(err)
	}
	pr, err := client.PublishDraft(ctx, input)
	if err != nil {
		t.Fatalf("publish fixture: %v", err)
	}
	// Simulate a crash after GitHub accepted the PR but before the ledger
	// acknowledged it. The retry must reconcile the same draft without writes.
	writes := fixture.writes
	second, err := client.PublishDraft(ctx, input)
	if err != nil || second != pr || fixture.writes != writes || fixture.prPosts != 1 {
		t.Fatalf("redelivery duplicated publication: pr=%+v, err=%v, writes=%d/%d, prPosts=%d", second, err, fixture.writes, writes, fixture.prPosts)
	}
	intent.PRNumber, intent.PRURL, intent.HeadSHA = pr.Number, pr.URL, pr.CommitSHA
	if err := ledger.MarkPublished(ctx, fence, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Claim(ctx, a.ID, state.Owner{RunID: "redelivery", RunAttempt: 1}); err != state.ErrClaimed {
		t.Fatalf("completed redelivery claimed work: %v", err)
	}
	final, err := ledger.Store.Load(ctx)
	if err != nil || final.State.Attempts[a.ID].Phase != state.Draft || final.State.Attempts[a.ID].Counts.ModelCalls != 1 {
		t.Fatalf("ledger did not preserve draft and budget: %+v, %v", final, err)
	}
	if fixture.providerRequests != 0 || fixture.prPosts != 1 {
		t.Fatalf("fake test used provider or made duplicate PR: %+v", fixture)
	}
}

func e2eFakeACP(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-acp")
	build := exec.Command("go", "build", "-ldflags=-X main.mode="+mode, "-o", path, "../../cmd/fake-acp")
	build.Env = append(os.Environ(), "GOCACHE="+filepath.Join(t.TempDir(), "go-cache"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake ACP peer: %v: %s", err, output)
	}
	return path
}

func e2eFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"go.mod", ".sofa.yml", "fixture/greeting.go", "fixture/greeting_test.go"} {
		content := e2eRead(t, filepath.Join("../../examples/consumer", name))
		if name == "fixture/greeting.go" {
			// The fake ACP peer makes a real file-capability edit by formatting a
			// passing but deliberately unformatted fixture. The assertion remains
			// independent of the edit: test code is copied unchanged.
			content = []byte("package fixture\n\nimport \"strings\"\nfunc Greeting(name string) string { name = strings.TrimSpace(name); if name == \"\" { name = \"friend\" }; return \"Hello, \" + name }\n")
		}
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	e2eGit(t, root, "init", "-q")
	e2eGit(t, root, "add", ".")
	e2eGit(t, root, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	return root
}

func e2eClone(t *testing.T, original string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "checkout")
	cmd := exec.Command("git", "clone", "-q", "--no-hardlinks", original, root)
	cmd.Env = gitEnv()
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone fixture: %v: %s", err, output)
	}
	return root
}

func e2eGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = root, gitEnv()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func e2eRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type e2ePublisher struct {
	repo, baseSHA, branch string
	original, candidate   []byte
	commitMessage         string
	mainOverride          string
	branchExists, pr      bool
	writes, prPosts       int
	providerRequests      int // No endpoint is configured for this model-free fixture.
}

func (f *e2ePublisher) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer inert-publisher-token" {
		return e2eResponse(403, map[string]any{}), nil
	}
	p := r.URL.Path
	if r.Method == http.MethodPost {
		f.writes++
	}
	oldBlob, newBlob := e2eBlobSHA(f.original), e2eBlobSHA(f.candidate)
	const baseTree = "cccccccccccccccccccccccccccccccccccccccc"
	const newTree = "dddddddddddddddddddddddddddddddddddddddd"
	const commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/ref/heads/main"):
		if f.mainOverride != "" {
			return e2eResponse(200, map[string]any{"object": map[string]any{"sha": f.mainOverride}}), nil
		}
		return e2eResponse(200, map[string]any{"object": map[string]any{"sha": f.baseSHA}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/ref/heads/"+f.branch):
		if !f.branchExists {
			return e2eResponse(404, map[string]any{}), nil
		}
		return e2eResponse(200, map[string]any{"object": map[string]any{"sha": commit}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/commits/"+f.baseSHA):
		return e2eResponse(200, map[string]any{"sha": f.baseSHA, "tree": map[string]any{"sha": baseTree}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/commits/"+commit):
		return e2eResponse(200, map[string]any{"sha": commit, "tree": map[string]any{"sha": newTree}, "message": f.commitMessage, "parents": []any{map[string]any{"sha": f.baseSHA}}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/trees/"+baseTree):
		return e2eResponse(200, map[string]any{"tree": []any{map[string]any{"path": "fixture/greeting.go", "mode": "100644", "type": "blob", "sha": oldBlob}}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/trees/"+newTree):
		return e2eResponse(200, map[string]any{"tree": []any{map[string]any{"path": "fixture/greeting.go", "mode": "100644", "type": "blob", "sha": newBlob}}}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/git/blobs/"+oldBlob):
		return e2eResponse(200, map[string]any{"content": base64.StdEncoding.EncodeToString(f.original), "encoding": "base64", "size": len(f.original)}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/blobs"):
		return e2eResponse(201, map[string]any{"sha": newBlob}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/trees"):
		return e2eResponse(201, map[string]any{"sha": newTree}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/commits"):
		var input struct{ Message string }
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			return e2eResponse(422, map[string]any{}), nil
		}
		f.commitMessage = input.Message
		return e2eResponse(201, map[string]any{"sha": commit}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/git/refs"):
		f.branchExists = true
		return e2eResponse(201, map[string]any{"ref": "refs/heads/" + f.branch}), nil
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls"):
		if !f.pr {
			return e2eResponse(200, []any{}), nil
		}
		return e2eResponse(200, []any{f.prObject(commit)}), nil
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls"):
		f.pr, f.prPosts = true, f.prPosts+1
		return e2eResponse(201, f.prObject(commit)), nil
	}
	return e2eResponse(404, map[string]any{}), nil
}

func (f *e2ePublisher) prObject(commit string) any {
	return map[string]any{"number": 12, "html_url": "https://github.com/" + f.repo + "/pull/12", "state": "open", "draft": true, "head": map[string]any{"ref": f.branch, "sha": commit, "repo": map[string]any{"full_name": f.repo}}}
}

func e2eBlobSHA(b []byte) string {
	h := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(b))), b...))
	return fmt.Sprintf("%x", h[:])
}

func e2eResponse(code int, value any) *http.Response {
	b, _ := json.Marshal(value)
	return &http.Response{StatusCode: code, Body: io.NopCloser(bytes.NewReader(b)), Header: make(http.Header)}
}
