package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
	"github.com/kevinmartin/sofa/internal/worker"
)

func testManifest(t *testing.T) (config.Config, Manifest) {
	t.Helper()
	f, err := os.Open("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := config.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	spec, specDigest, err := admission.CanonicalSpec("Fixture", "Change the fixture")
	if err != nil {
		t.Fatal(err)
	}
	configDigest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	g := admission.Grant{Version: 1, Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_123", IssueNumber: 7, ProjectID: c.ProjectID, OwnerID: c.OwnerID, SpecDigest: specDigest, ConfigDigest: configDigest, BaseSHA: strings.Repeat("a", 40), ProjectItemID: "PVTI_123", StatusOptionID: "ready-option", StatusUpdatedAt: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)}
	a := ledgerAdmission(g)
	m := Manifest{Version: 1, Grant: g, Fence: state.Fence{AttemptID: state.AttemptID(a), Generation: 1, Owner: state.Owner{RunID: "1234", RunAttempt: 1}}, CanonicalSpec: spec}
	return c, m
}

func TestManifestBindsSpecAndConfiguration(t *testing.T) {
	c, m := testManifest(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := writeJSON(path, m); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(path, c); err != nil {
		t.Fatal(err)
	}
	m.CanonicalSpec = []byte(`{"title":"Fixture","body":"Changed without approval"}`)
	if err := writeJSON(path, m); err != nil {
		t.Fatal(err)
	}
	if _, err := readManifest(path, c); err == nil {
		t.Fatal("accepted changed specification")
	}
}

func TestRecoveryRequiresPersistedCandidateIdentity(t *testing.T) {
	_, m := testManifest(t)
	m.Fence.Generation = 3
	m.Recovery = &state.Publication{CandidateDigest: strings.Repeat("b", 64)}
	m.RecoverySource = &state.Owner{RunID: "100", RunAttempt: 1}
	prior := integrity.Bundle{Generation: 2, CandidateDigest: m.Recovery.CandidateDigest}
	if got := bundleExpected(m, prior).Generation; got != 2 {
		t.Fatalf("prior verified generation was lost: %d", got)
	}
	prior.CandidateDigest = strings.Repeat("c", 64)
	if got := bundleExpected(m, prior).Generation; got != 3 {
		t.Fatalf("unrelated bundle reused prior generation: %d", got)
	}
	m.Recovery = nil
	m.RecoveryCheckpoint = &state.Checkpoint{Generation: 2, CandidateSHA: strings.Repeat("d", 64)}
	prior.CandidateDigest = m.RecoveryCheckpoint.CandidateSHA
	if got := bundleExpected(m, prior).Generation; got != 2 {
		t.Fatalf("checkpoint candidate generation was lost: %d", got)
	}
	prior.Generation = 1
	if got := bundleExpected(m, prior).Generation; got != 3 {
		t.Fatalf("wrong checkpoint generation accepted: %d", got)
	}
}

func TestSecretlessStageRejectsPrivilegedCredential(t *testing.T) {
	t.Setenv("SOFA_PUBLISH_TOKEN", "inert-sentinel")
	if err := forbidPrivilegedEnv("SOFA_COPILOT_TOKEN", false); err == nil || strings.Contains(err.Error(), "inert-sentinel") {
		t.Fatal("privileged credential was accepted or exposed")
	}
}

func TestFailureClassificationKeepsDeterministicErrorsOutOfRetry(t *testing.T) {
	if got := executionFailureKind(worker.ErrValidation); got != "validation" {
		t.Fatalf("worker validation classified as %s", got)
	}
	if got := executionFailureKind(errExecutionValidation); got != "validation" {
		t.Fatalf("empty or stale candidate classified as %s", got)
	}
	if got := executionFailureKind(errors.New("unavailable transport")); got != "infrastructure" {
		t.Fatalf("unknown transport classified as %s", got)
	}
}

func TestFailureObservationsRemainDistinctAcrossRecoveredGenerations(t *testing.T) {
	_, m := testManifest(t)
	e := state.Engine{Store: &state.MemoryStore{}}
	if _, _, err := e.Admit(context.Background(), ledgerAdmission(m.Grant), state.Limits{ModelCalls: 2, InfrastructureRetries: 2, RuntimeSeconds: 1200}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		generation int64
		outcome    string
	}{{1, "infrastructure"}, {2, "quota"}} {
		scope := fmt.Sprintf("g%d", item.generation)
		if err := observeOnce(context.Background(), e, m.Fence.AttemptID, "failure-finalizer", item.outcome, m.Grant.BaseSHA, "", scope); err != nil {
			t.Fatal(err)
		}
		if err := observeOnce(context.Background(), e, m.Fence.AttemptID, "failure-finalizer", item.outcome, m.Grant.BaseSHA, "", scope); err != nil {
			t.Fatalf("finalizer replay was not idempotent: %v", err)
		}
	}
	if err := observeOnce(context.Background(), e, m.Fence.AttemptID, "failure-finalizer", "validation", m.Grant.BaseSHA, "", "g2"); !errors.Is(err, state.ErrConflict) {
		t.Fatalf("changed outcome rewrote generation two: %v", err)
	}
	snapshot, err := e.Store.Load(context.Background())
	if err != nil || len(snapshot.State.Observations) != 2 {
		t.Fatalf("expected both failed generations in append-only observations: %v", err)
	}
}

type synchronizedReadStore struct {
	state.Store
	mu        sync.Mutex
	remaining int
	release   chan struct{}
}

func (s *synchronizedReadStore) Load(ctx context.Context) (state.Snapshot, error) {
	snapshot, err := s.Store.Load(ctx)
	if err != nil {
		return snapshot, err
	}
	s.mu.Lock()
	wait := s.remaining > 0
	if wait {
		s.remaining--
		if s.remaining == 0 {
			close(s.release)
		}
	}
	s.mu.Unlock()
	if wait {
		select {
		case <-s.release:
		case <-ctx.Done():
			return state.Snapshot{}, ctx.Err()
		}
	}
	return snapshot, nil
}

func TestConcurrentEquivalentObservationsAreIdempotent(t *testing.T) {
	_, m := testManifest(t)
	store := &state.MemoryStore{}
	e := state.Engine{Store: store}
	if _, _, err := e.Admit(context.Background(), ledgerAdmission(m.Grant), state.Limits{ModelCalls: 2, InfrastructureRetries: 2, RuntimeSeconds: 1200}); err != nil {
		t.Fatal(err)
	}
	e.Store = &synchronizedReadStore{Store: store, remaining: 2, release: make(chan struct{})}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- observeOnce(context.Background(), e, m.Fence.AttemptID, "admission", "accepted", m.Grant.BaseSHA, "", "")
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("equivalent observation conflicted: %v", err)
		}
	}
	snapshot, err := store.Load(context.Background())
	if err != nil || len(snapshot.State.Observations) != 1 {
		t.Fatalf("expected exactly one admission observation: %v", err)
	}
}

func TestVerifyAppliesOnlyCandidateAndRunsFixtureCheck(t *testing.T) {
	for _, name := range []string{"SOFA_MODEL_TOKEN", "SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(name, "")
	}
	root := t.TempDir()
	for _, name := range []string{"go.mod", ".sofa.yml", "fixture/greeting.go", "fixture/greeting_test.go"} {
		data, err := os.ReadFile(filepath.Join("../../examples/consumer", name))
		if err != nil {
			t.Fatal(err)
		}
		location := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(location, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = gitEnv()
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	_, m := testManifest(t)
	m.Grant.BaseSHA = base
	m.Fence.AttemptID = state.AttemptID(ledgerAdmission(m.Grant))
	original, err := os.ReadFile(filepath.Join(root, "fixture/greeting.go"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte("package fixture\n\nimport \"strings\"\n\nfunc Greeting(name string) string {\n name = strings.TrimSpace(name)\n if name == \"\" { name = \"friend\" }; return \"Hello, \" + name\n}\n")
	b := integrity.Bundle{Version: integrity.Version, Repository: m.Grant.Repository, AttemptID: m.Fence.AttemptID, Generation: uint64(m.Fence.Generation), BaseSHA: base, Files: []integrity.File{{Path: "fixture/greeting.go", Operation: "update", Mode: integrity.RegularMode, BeforeSHA256: integrity.Hash(original), Content: replacement}}}
	if err := integrity.Seal(&b); err != nil {
		t.Fatal(err)
	}
	transport := t.TempDir()
	manifestPath, bundlePath, evidencePath := filepath.Join(transport, "manifest.json"), filepath.Join(transport, "bundle.json"), filepath.Join(transport, "checks.json")
	if err := writeJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(bundlePath, b); err != nil {
		t.Fatal(err)
	}
	if err := verify(context.Background(), []string{"--config", filepath.Join(root, ".sofa.yml"), "--manifest", manifestPath, "--workspace", root, "--bundle", bundlePath, "--out", evidencePath}); err != nil {
		t.Fatal(err)
	}
	var checks []integrity.CheckEvidence
	content, err := os.ReadFile(evidencePath)
	if err != nil || json.Unmarshal(content, &checks) != nil || len(checks) != 1 || !checks[0].Passed || checks[0].CandidateDigest != b.CandidateDigest {
		t.Fatal("verified candidate has no exact passing check evidence")
	}
	testBefore, _ := os.ReadFile(filepath.Join("../../examples/consumer", "fixture/greeting_test.go"))
	testAfter, _ := os.ReadFile(filepath.Join(root, "fixture/greeting_test.go"))
	if string(testBefore) != string(testAfter) {
		t.Fatal("verifier changed the independent fixture test")
	}
}

func TestExecuteExactRecipeUsesNoModelCredential(t *testing.T) {
	for _, name := range []string{"SOFA_MODEL_TOKEN", "SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_APP_PRIVATE_KEY"} {
		t.Setenv(name, "")
	}
	root := t.TempDir()
	for _, name := range []string{"go.mod", ".sofa.yml", "fixture/greeting.go", "fixture/greeting_test.go"} {
		data, err := os.ReadFile(filepath.Join("../../examples/consumer", name))
		if err != nil {
			t.Fatal(err)
		}
		location := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(location, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "fixture/greeting.go"), []byte("package fixture\nfunc Greeting(name string)string{return \"Hello, \"+name}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = gitEnv()
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	_, m := testManifest(t)
	spec, digest, err := admission.CanonicalSpec("Format fixture", "Format the fixture.\n<!-- sofa:recipe=gofmt -->")
	if err != nil {
		t.Fatal(err)
	}
	m.CanonicalSpec, m.Grant.SpecDigest, m.Grant.BaseSHA = spec, digest, base
	m.Fence.AttemptID = state.AttemptID(ledgerAdmission(m.Grant))
	transport := t.TempDir()
	manifestPath := filepath.Join(transport, "manifest.json")
	bundlePath := filepath.Join(transport, "bundle.json")
	if err := writeJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), []string{"--config", filepath.Join(root, ".sofa.yml"), "--manifest", manifestPath, "--workspace", root, "--out", bundlePath}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version         int    `json:"version"`
		UsedAgent       bool   `json:"used_agent"`
		PromptRequests  int    `json:"prompt_requests"`
		ModelCalls      *int   `json:"model_calls"`
		CandidateDigest string `json:"candidate_digest"`
	}
	if err := readJSON(filepath.Join(transport, "execution.json"), 4096, &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.UsedAgent || result.PromptRequests != 0 || result.ModelCalls != nil {
		t.Fatal("exact recipe invoked or reported model use")
	}
	b, err := readBundle(bundlePath)
	if err != nil || len(b.Files) != 1 || b.Files[0].Path != "fixture/greeting.go" || result.CandidateDigest != b.CandidateDigest {
		t.Fatal("exact recipe did not produce the bounded formatter candidate")
	}
}
