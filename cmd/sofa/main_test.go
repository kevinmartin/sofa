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
	"go.yaml.in/yaml/v3"
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

func TestLedgerAdmissionNormalizesRepositoryCasing(t *testing.T) {
	_, m := testManifest(t)
	m.Grant.Repository = "Owner/Fixture"
	a := ledgerAdmission(m.Grant)
	if a.Repository != "owner/fixture" {
		t.Fatalf("ledger repository was not normalized: %q", a.Repository)
	}
	lower := m.Grant
	lower.Repository = "owner/fixture"
	if a != ledgerAdmission(lower) {
		t.Fatal("repository casing changed ledger admission")
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

func TestExecutionFailureVersionAndTelemetryValidation(t *testing.T) {
	_, m := testManifest(t)
	base := ExecutionFailure{Version: 2, AttemptID: m.Fence.AttemptID, Generation: m.Fence.Generation, Kind: "validation", Reason: "candidate-no-change", UsedAgent: true, PromptRequests: 1, Updates: 5, PermissionRequests: 2, PermissionDenials: 1, PermissionExecuteDenials: 1, ToolReads: 1, ToolEdits: 1, ToolExecutes: 1, ToolOthers: 1, ToolFailedUpdates: 1}
	if err := validateExecutionFailure(base, m); err != nil {
		t.Fatal(err)
	}
	verification := ExecutionFailure{Version: 2, AttemptID: m.Fence.AttemptID, Generation: m.Fence.Generation, Kind: "validation", Reason: "configured-check"}
	if err := validateExecutionFailure(verification, m); err != nil {
		t.Fatalf("configured check failure was not finalizable: %v", err)
	}
	legacy := ExecutionFailure{Version: 1, AttemptID: m.Fence.AttemptID, Generation: m.Fence.Generation, Kind: "infrastructure"}
	if err := validateExecutionFailure(legacy, m); err != nil {
		t.Fatal("workflow fallback rejected:", err)
	}
	legacy.Kind = "validation"
	if err := validateExecutionFailure(legacy, m); err == nil {
		t.Fatal("accepted legacy failure with non-infrastructure class")
	}
	for _, mutate := range []func(*ExecutionFailure){
		func(f *ExecutionFailure) { f.Reason = "agent-supplied text" },
		func(f *ExecutionFailure) { f.PromptRequests = -1 },
		func(f *ExecutionFailure) { f.PromptRequests = 2 },
		func(f *ExecutionFailure) { f.Updates = -1 },
		func(f *ExecutionFailure) { f.Updates = maxACPObservationCount + 1 },
		func(f *ExecutionFailure) { f.PermissionRequests = -1 },
		func(f *ExecutionFailure) { f.PermissionRequests = maxACPObservationCount + 1 },
		func(f *ExecutionFailure) { f.PermissionDenials = -1 },
		func(f *ExecutionFailure) { f.PermissionDenials = 3 },
		func(f *ExecutionFailure) { f.PermissionExecuteDenials = -1 },
		func(f *ExecutionFailure) { f.PermissionExecuteDenials = 2 },
		func(f *ExecutionFailure) { f.ToolReads = -1 },
		func(f *ExecutionFailure) { f.ToolReads = 6 },
		func(f *ExecutionFailure) { f.ToolEdits = 3 },
		func(f *ExecutionFailure) { f.ToolFailedUpdates = 6 },
		func(f *ExecutionFailure) { f.UsedAgent = false },
		func(f *ExecutionFailure) { f.AttemptID = "other" },
		func(f *ExecutionFailure) { f.Kind = "retry-anyway" },
		func(f *ExecutionFailure) { f.Kind = "infrastructure" },
		func(f *ExecutionFailure) { f.Reason = "base-checkout" },
		func(f *ExecutionFailure) { f.Version = 1 },
	} {
		candidate := base
		mutate(&candidate)
		if err := validateExecutionFailure(candidate, m); err == nil {
			t.Fatalf("accepted invalid failure telemetry: %+v", candidate)
		}
	}
	withoutAgent := base
	withoutAgent.UsedAgent = false
	withoutAgent.PromptRequests = 0
	if err := validateExecutionFailure(withoutAgent, m); err == nil {
		t.Fatal("accepted ACP activity without an agent")
	}
	legacy.Updates = 1
	legacy.Kind = "infrastructure"
	if err := validateExecutionFailure(legacy, m); err == nil {
		t.Fatal("accepted legacy failure with ACP telemetry")
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
	c, m := testManifest(t)
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
	// A configured check can exit successfully after changing the candidate.
	// That must not create evidence for the original bundle digest.
	git("restore", "--", "fixture/greeting.go")
	c.Checks = []config.Check{{ID: "mutating-check", Argv: []string{"sh", "-c", "printf '\n// changed by check\n' >> fixture/greeting.go"}, TimeoutSeconds: 30}}
	configBytes, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".sofa.yml"), configBytes, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".sofa.yml")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "mutating check")
	m.Grant.BaseSHA = git("rev-parse", "HEAD")
	m.Grant.ConfigDigest, err = c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	m.Fence.AttemptID = state.AttemptID(ledgerAdmission(m.Grant))
	b.BaseSHA = m.Grant.BaseSHA
	b.AttemptID = m.Fence.AttemptID
	if err := integrity.Seal(&b); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(bundlePath, b); err != nil {
		t.Fatal(err)
	}
	mutatedEvidencePath := filepath.Join(transport, "mutated-checks.json")
	if err := verify(context.Background(), []string{"--config", filepath.Join(root, ".sofa.yml"), "--manifest", manifestPath, "--workspace", root, "--bundle", bundlePath, "--out", mutatedEvidencePath}); !errors.Is(err, errExecutionValidation) {
		t.Fatalf("mutating check was accepted: %v", err)
	}
	var failure ExecutionFailure
	if err := readJSON(filepath.Join(transport, "verification-failure.json"), 4096, &failure); err != nil || failure.Kind != "validation" || failure.Reason != "workspace-changed" {
		t.Fatalf("mutating check lacked a validation failure artifact: %+v, %v", failure, err)
	}
	if _, err := os.Stat(mutatedEvidencePath); !os.IsNotExist(err) {
		t.Fatal("mutating check produced passing evidence")
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
		Version                  int    `json:"version"`
		UsedAgent                bool   `json:"used_agent"`
		PromptRequests           int    `json:"prompt_requests"`
		Updates                  int    `json:"updates"`
		PermissionRequests       int    `json:"permission_requests"`
		PermissionDenials        int    `json:"permission_denials"`
		PermissionExecuteDenials int    `json:"permission_execute_denials"`
		ToolReads                int    `json:"tool_reads"`
		ToolEdits                int    `json:"tool_edits"`
		ToolExecutes             int    `json:"tool_executes"`
		ToolOthers               int    `json:"tool_others"`
		ToolFailedUpdates        int    `json:"tool_failed_updates"`
		ModelCalls               *int   `json:"model_calls"`
		CandidateDigest          string `json:"candidate_digest"`
	}
	if err := readJSON(filepath.Join(transport, "execution.json"), 4096, &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || result.UsedAgent || result.PromptRequests != 0 || result.Updates != 0 || result.PermissionRequests != 0 || result.PermissionDenials != 0 || result.PermissionExecuteDenials != 0 || result.ToolReads != 0 || result.ToolEdits != 0 || result.ToolExecutes != 0 || result.ToolOthers != 0 || result.ToolFailedUpdates != 0 || result.ModelCalls != nil {
		t.Fatal("exact recipe invoked or reported model use")
	}
	b, err := readBundle(bundlePath)
	if err != nil || len(b.Files) != 1 || b.Files[0].Path != "fixture/greeting.go" || result.CandidateDigest != b.CandidateDigest {
		t.Fatal("exact recipe did not produce the bounded formatter candidate")
	}
	git("add", ".")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "formatted")
	m.Grant.BaseSHA = git("rev-parse", "HEAD")
	m.Fence.AttemptID = state.AttemptID(ledgerAdmission(m.Grant))
	if err := writeJSON(manifestPath, m); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), []string{"--config", filepath.Join(root, ".sofa.yml"), "--manifest", manifestPath, "--workspace", root, "--out", bundlePath}); !errors.Is(err, errExecutionValidation) {
		t.Fatalf("expected bounded no-change failure: %v", err)
	}
	var failure ExecutionFailure
	if err := readJSON(filepath.Join(transport, "execution-failure.json"), 4096, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Version != 2 || failure.Kind != "validation" || failure.Reason != "candidate-no-change" || failure.UsedAgent || failure.PromptRequests != 0 {
		t.Fatalf("no-change failure was not distinguished: %+v", failure)
	}
	if err := os.WriteFile(filepath.Join(root, "fixture", "untracked.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), []string{"--config", filepath.Join(root, ".sofa.yml"), "--manifest", manifestPath, "--workspace", root, "--out", bundlePath}); !errors.Is(err, errExecutionValidation) {
		t.Fatalf("expected dirty-base failure: %v", err)
	}
	if err := readJSON(filepath.Join(transport, "execution-failure.json"), 4096, &failure); err != nil || failure.Reason != "base-checkout" || failure.UsedAgent || failure.PromptRequests != 0 {
		t.Fatalf("dirty-base failure was not distinguished: %+v, %v", failure, err)
	}
}
