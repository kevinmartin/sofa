package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/integrity"
)

func TestPrepareAndRecoverRetainExactCandidateIdentity(t *testing.T) {
	root := t.TempDir()
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	base := strings.Repeat("a", 40)
	candidate := strings.Repeat("b", 40)
	prBase := strings.Repeat("c", 40)
	if err := run([]string{"prepare", "--config", "../../examples/consumer/.sofa.yml", "--out-dir", first, "--suite-id", "suite-1", "--candidate-sha", candidate, "--pr-base-sha", prBase, "--disposable-base-sha", base, "--run-id", "101", "--run-attempt", "1"}); err != nil {
		t.Fatal(err)
	}
	var original manifest
	var before identity
	if err := readJSON(filepath.Join(first, "manifest.json"), &original); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(filepath.Join(first, "identity.json"), &before); err != nil {
		t.Fatal(err)
	}
	if before.CandidateSHA != candidate || before.PRBaseSHA != prBase || before.DisposableBaseSHA != base || before.FakeAgent != "fake-acp" || original.Grant.BaseSHA != base {
		t.Fatalf("fixture identities changed: %+v %+v", before, original.Grant)
	}
	f, err := os.Open(filepath.Join(first, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Decode(f)
	_ = f.Close()
	if err != nil || c.Recipe != nil || c.Repository != disposableRepository {
		t.Fatalf("fake ACP configuration was not isolated: %+v %v", c, err)
	}
	bundle := integrity.Bundle{Version: integrity.Version, Repository: disposableRepository, AttemptID: original.Fence.AttemptID, Generation: 1, BaseSHA: base}
	if err := integrity.Seal(&bundle); err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "bundle.json")
	if err := writeJSON(bundlePath, bundle); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"recover", "--from-dir", first, "--out-dir", second, "--bundle", bundlePath, "--run-id", "102", "--run-attempt", "1"}); err != nil {
		t.Fatal(err)
	}
	var recovered manifest
	var after identity
	if err := readJSON(filepath.Join(second, "manifest.json"), &recovered); err != nil {
		t.Fatal(err)
	}
	if err := readJSON(filepath.Join(second, "identity.json"), &after); err != nil {
		t.Fatal(err)
	}
	if recovered.Fence.AttemptID != original.Fence.AttemptID || recovered.Fence.Generation != 2 || recovered.RecoveryCheckpoint == nil || recovered.RecoveryCheckpoint.CandidateSHA != bundle.CandidateDigest || recovered.RecoverySource == nil || recovered.RecoverySource.RunID != "101" || after.CandidateSHA != candidate || after.PRBaseSHA != prBase || after.Generation != 2 {
		t.Fatalf("recovery lost attempt, producer, or PR identity: %+v %+v", recovered, after)
	}
	executionPath, evidencePath, publicationPath, reportPath := filepath.Join(root, "execution.json"), filepath.Join(root, "evidence.json"), filepath.Join(root, "publication.json"), filepath.Join(root, "report.json")
	for _, artifact := range []struct {
		path  string
		value any
	}{
		{executionPath, map[string]any{"version": 1, "used_agent": true, "prompt_requests": 1, "model_calls": nil, "candidate_digest": bundle.CandidateDigest}},
		{evidencePath, []integrity.CheckEvidence{{Version: integrity.Version, Name: "go-test", CandidateDigest: bundle.CandidateDigest, Passed: true}}},
		{publicationPath, map[string]any{"schema_version": 1, "simulation": "fake-github-transport", "candidate_digest": bundle.CandidateDigest, "base_sha": base, "attempt_id": bundle.AttemptID, "generation": 1, "pr_number": 7, "pr_url": "https://github.com/kevinmartin/sofa-disposable/pull/7", "pr_posts": 1, "provider_requests": 0}},
	} {
		if err := writeJSON(artifact.path, artifact.value); err != nil {
			t.Fatal(err)
		}
	}
	if err := run([]string{"report", "--identity", filepath.Join(second, "identity.json"), "--manifest", filepath.Join(second, "manifest.json"), "--bundle", bundlePath, "--execution", executionPath, "--evidence", evidencePath, "--publication", publicationPath, "--out", reportPath}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		CandidateSHA     string `json:"candidate_sha"`
		PRBaseSHA        string `json:"pr_base_sha"`
		ProducerRunID    string `json:"producer_run_id"`
		BundleGeneration int    `json:"bundle_generation"`
	}
	if err := readJSON(reportPath, &report); err != nil || report.CandidateSHA != candidate || report.PRBaseSHA != prBase || report.ProducerRunID != "101" || report.BundleGeneration != 1 {
		t.Fatalf("redacted report lost exact source or producer identity: %+v, %v", report, err)
	}
	bundle.BaseSHA = strings.Repeat("d", 40)
	if err := writeJSON(bundlePath, bundle); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"recover", "--from-dir", first, "--out-dir", filepath.Join(root, "bad"), "--bundle", bundlePath, "--run-id", "103", "--run-attempt", "1"}); err == nil {
		t.Fatal("accepted retained bundle from a different base")
	}
}
