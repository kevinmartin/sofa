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
	for _, field := range []struct{ name, got, want string }{
		{"candidate SHA", before.CandidateSHA, candidate},
		{"PR base SHA", before.PRBaseSHA, prBase},
		{"disposable base SHA", before.DisposableBaseSHA, base},
		{"fake agent", before.FakeAgent, "fake-acp"},
		{"grant base SHA", original.Grant.BaseSHA, base},
	} {
		if field.got != field.want {
			t.Errorf("%s: got %q, want %q", field.name, field.got, field.want)
		}
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
	if recovered.Fence.AttemptID != original.Fence.AttemptID {
		t.Errorf("recovery attempt ID: got %q, want %q", recovered.Fence.AttemptID, original.Fence.AttemptID)
	}
	if recovered.Fence.Generation != 2 {
		t.Errorf("recovery fence generation: got %d, want 2", recovered.Fence.Generation)
	}
	if recovered.RecoveryCheckpoint == nil {
		t.Error("recovery checkpoint is missing")
	} else if recovered.RecoveryCheckpoint.CandidateSHA != bundle.CandidateDigest {
		t.Errorf("recovery checkpoint candidate SHA: got %q, want %q", recovered.RecoveryCheckpoint.CandidateSHA, bundle.CandidateDigest)
	}
	if recovered.RecoverySource == nil {
		t.Error("recovery source is missing")
	} else if recovered.RecoverySource.RunID != "101" {
		t.Errorf("recovery source run ID: got %q, want 101", recovered.RecoverySource.RunID)
	}
	if after.CandidateSHA != candidate {
		t.Errorf("recovered candidate SHA: got %q, want %q", after.CandidateSHA, candidate)
	}
	if after.PRBaseSHA != prBase {
		t.Errorf("recovered PR base SHA: got %q, want %q", after.PRBaseSHA, prBase)
	}
	if after.Generation != 2 {
		t.Errorf("recovered identity generation: got %d, want 2", after.Generation)
	}
	executionPath, evidencePath, publicationPath, networkPath, reportPath := filepath.Join(root, "execution.json"), filepath.Join(root, "evidence.json"), filepath.Join(root, "publication.json"), filepath.Join(root, "network.json"), filepath.Join(root, "report.json")
	for _, artifact := range []struct {
		path  string
		value any
	}{
		{executionPath, map[string]any{"version": 1, "used_agent": true, "prompt_requests": 1, "model_calls": nil, "candidate_digest": bundle.CandidateDigest}},
		{evidencePath, []integrity.CheckEvidence{{Version: integrity.Version, Name: "go-test", CandidateDigest: bundle.CandidateDigest, Passed: true}}},
		{publicationPath, map[string]any{"schema_version": 1, "simulation": "fake-github-transport", "candidate_digest": bundle.CandidateDigest, "base_sha": base, "attempt_id": bundle.AttemptID, "generation": 1, "pr_number": 7, "pr_url": "https://github.com/kevinmartin/sofa-disposable/pull/7", "pr_posts": 1, "provider_requests": 0}},
		{networkPath, map[string]any{"schema_version": 1, "network_mode": "none", "source": "proc-net-dev", "tx_packets_before": 0, "tx_packets_after": 0, "tx_packets_delta": 0}},
	} {
		if err := writeJSON(artifact.path, artifact.value); err != nil {
			t.Fatal(err)
		}
	}
	reportArgs := []string{"report", "--identity", filepath.Join(second, "identity.json"), "--manifest", filepath.Join(second, "manifest.json"), "--bundle", bundlePath, "--execution", executionPath, "--evidence", evidencePath, "--publication", publicationPath, "--network", networkPath, "--out", reportPath}
	if err := run(reportArgs); err != nil {
		t.Fatal(err)
	}
	var report struct {
		CandidateSHA     string `json:"candidate_sha"`
		PRBaseSHA        string `json:"pr_base_sha"`
		ProducerRunID    string `json:"producer_run_id"`
		BundleGeneration int    `json:"bundle_generation"`
		SchemaVersion    int    `json:"schema_version"`
		NetworkTXPackets uint64 `json:"network_tx_packets"`
		NetworkSource    string `json:"network_measurement_source"`
	}
	if err := readJSON(reportPath, &report); err != nil {
		t.Fatal(err)
	}
	for _, field := range []struct{ name, got, want string }{
		{"report candidate SHA", report.CandidateSHA, candidate},
		{"report PR base SHA", report.PRBaseSHA, prBase},
		{"report producer run ID", report.ProducerRunID, "101"},
		{"report network source", report.NetworkSource, "proc-net-dev"},
	} {
		if field.got != field.want {
			t.Errorf("%s: got %q, want %q", field.name, field.got, field.want)
		}
	}
	if report.BundleGeneration != 1 {
		t.Errorf("report bundle generation: got %d, want 1", report.BundleGeneration)
	}
	if report.SchemaVersion != 2 {
		t.Errorf("report schema version: got %d, want 2", report.SchemaVersion)
	}
	if report.NetworkTXPackets != 0 {
		t.Errorf("report network TX packets: got %d, want 0", report.NetworkTXPackets)
	}
	for _, tc := range []struct {
		name     string
		evidence map[string]any
	}{
		{"missing packet count", map[string]any{"schema_version": 1, "network_mode": "none", "source": "proc-net-dev", "tx_packets_after": 0, "tx_packets_delta": 0}},
		{"transmitted packet", map[string]any{"schema_version": 1, "network_mode": "none", "source": "proc-net-dev", "tx_packets_before": 0, "tx_packets_after": 1, "tx_packets_delta": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeJSON(networkPath, tc.evidence); err != nil {
				t.Fatal(err)
			}
			if err := run(reportArgs); err == nil {
				t.Fatal("accepted missing or nonzero packet evidence")
			}
		})
	}
	bundle.BaseSHA = strings.Repeat("d", 40)
	if err := writeJSON(bundlePath, bundle); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"recover", "--from-dir", first, "--out-dir", filepath.Join(root, "bad"), "--bundle", bundlePath, "--run-id", "103", "--run-attempt", "1"}); err == nil {
		t.Fatal("accepted retained bundle from a different base")
	}
}

func TestDeniedReportRequiresRealSchedulerSkips(t *testing.T) {
	for _, tc := range []struct{ kind, decision string }{
		{"non-ready", "admission-denied"},
		{"completed-redelivery", "already-completed"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "denial.json")
			args := []string{"deny", "--config", "../../examples/consumer/.sofa.yml", "--out", out, "--suite-id", "suite-1", "--denial-kind", tc.kind,
				"--candidate-sha", strings.Repeat("b", 40), "--pr-base-sha", strings.Repeat("c", 40), "--disposable-base-sha", strings.Repeat("a", 40),
				"--run-id", "101", "--run-attempt", "1", "--execute-result", "skipped", "--verify-result", "skipped", "--publish-result", "skipped"}
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			var report struct {
				Scenario           string   `json:"scenario"`
				DenialKind         string   `json:"denial_kind"`
				Decision           string   `json:"decision"`
				SkippedJobs        []string `json:"skipped_jobs"`
				FakePromptRequests int      `json:"fake_prompt_requests"`
				ProviderRequests   int      `json:"provider_requests"`
				PublicationWrites  int      `json:"publication_writes"`
				WriteCredentials   int      `json:"write_credentials"`
			}
			if err := readJSON(out, &report); err != nil || report.Scenario != "denied" || report.DenialKind != tc.kind || report.Decision != tc.decision || strings.Join(report.SkippedJobs, ",") != "execute,verify,publish" || report.FakePromptRequests != 0 || report.ProviderRequests != 0 || report.PublicationWrites != 0 || report.WriteCredentials != 0 {
				t.Fatalf("denial evidence mismatch: %+v, %v", report, err)
			}
			for _, name := range []string{"--execute-result", "--verify-result", "--publish-result"} {
				bad := append([]string(nil), args...)
				for i := range bad {
					if bad[i] == name {
						bad[i+1] = "success"
						break
					}
				}
				if err := run(bad); err == nil {
					t.Fatalf("accepted running %s job", name)
				}
			}
		})
	}
}
