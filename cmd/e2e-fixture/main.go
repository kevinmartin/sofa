// e2e-fixture prepares synthetic, credential-free admission artifacts for the
// designated disposable regression tenant. It cannot authorize real work.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
	"go.yaml.in/yaml/v3"
)

const disposableRepository = "kevinmartin/sofa-disposable"

var (
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	suitePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

type manifest struct {
	Version            int               `json:"version"`
	Grant              admission.Grant   `json:"grant"`
	Fence              state.Fence       `json:"fence"`
	CanonicalSpec      json.RawMessage   `json:"canonical_spec"`
	RecoveryCheckpoint *state.Checkpoint `json:"recovery_checkpoint,omitempty"`
	RecoverySource     *state.Owner      `json:"recovery_source,omitempty"`
}

type identity struct {
	Version           int    `json:"version"`
	SuiteID           string `json:"suite_id"`
	Scenario          string `json:"scenario"`
	CandidateSHA      string `json:"candidate_sha"`
	PRBaseSHA         string `json:"pr_base_sha"`
	DisposableBaseSHA string `json:"disposable_base_sha"`
	AttemptID         string `json:"attempt_id"`
	Generation        int64  `json:"generation"`
	ProducerRunID     string `json:"producer_run_id,omitempty"`
	FakeAgent         string `json:"fake_agent"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "e2e-fixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("prepare or recover required")
	}
	switch args[0] {
	case "prepare":
		return prepare(args[1:])
	case "recover":
		return recoverCandidate(args[1:])
	case "report":
		return reportCandidate(args[1:])
	case "deny":
		return reportDenial(args[1:])
	default:
		return errors.New("unsupported fixture command")
	}
}

func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode fixture artifact")
	}
	return os.WriteFile(path, append(data, '\n'), 0600)
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return errors.New("fixture artifact unavailable or oversized")
	}
	if err := json.Unmarshal(data, value); err != nil {
		return errors.New("invalid fixture artifact")
	}
	return nil
}

func owner(runID, runAttempt string) (state.Owner, error) {
	n, err := strconv.Atoi(runAttempt)
	if err != nil || n < 1 || !regexp.MustCompile(`^[0-9]+$`).MatchString(runID) {
		return state.Owner{}, errors.New("invalid Actions run identity")
	}
	return state.Owner{RunID: runID, RunAttempt: n}, nil
}

func loadConfig(path string) (config.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return config.Config{}, errors.New("fixture configuration unavailable")
	}
	defer f.Close()
	c, err := config.Decode(f)
	if err != nil || c.Repository != disposableRepository {
		return config.Config{}, errors.New("fixture requires designated disposable repository")
	}
	c.Recipe = nil // Force the ACP path; normal consumer policy is untouched.
	return c, c.Validate()
}

func prepare(args []string) error {
	f := flag.NewFlagSet("prepare", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	configPath, outDir := f.String("config", "", ""), f.String("out-dir", "", "")
	suiteID, scenario := f.String("suite-id", "", ""), f.String("scenario", "edit", "")
	candidateSHA, prBaseSHA, disposableBaseSHA := f.String("candidate-sha", "", ""), f.String("pr-base-sha", "", ""), f.String("disposable-base-sha", "", "")
	runID, runAttempt := f.String("run-id", "", ""), f.String("run-attempt", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *configPath == "" || *outDir == "" || !suitePattern.MatchString(*suiteID) || *scenario != "edit" || !shaPattern.MatchString(*candidateSHA) || !shaPattern.MatchString(*prBaseSHA) || !shaPattern.MatchString(*disposableBaseSHA) {
		return errors.New("invalid fixture preparation input")
	}
	currentOwner, err := owner(*runID, *runAttempt)
	if err != nil {
		return err
	}
	c, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	snapshot := admission.Snapshot{
		Repository: c.Repository, RepositoryID: c.RepositoryID,
		IssueID: "I_sofa_e2e_" + *suiteID, Number: 1,
		Title: "Format fixture greeting", Body: "Format the approved fixture greeting file.", Open: true,
		ProjectID: c.ProjectID, ProjectPrivate: true, ProjectItemID: "PVTI_" + *suiteID,
		CurrentStatus: c.ReadyStatus, StatusOptionID: "ready-e2e", StatusUpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		BaseSHA: *disposableBaseSHA, Complete: true,
	}
	grant, spec, err := admission.Authorize(c, snapshot)
	if err != nil {
		return err
	}
	digest, err := c.Digest()
	if err != nil || digest != grant.ConfigDigest {
		return errors.New("fixture configuration digest changed")
	}
	a := state.Admission{Repository: grant.Repository, Issue: int64(grant.IssueNumber), SpecDigest: grant.SpecDigest, ConfigDigest: grant.ConfigDigest, BaseSHA: grant.BaseSHA, ProjectID: grant.ProjectID, ProjectItemID: grant.ProjectItemID, StatusOptionID: grant.StatusOptionID, StatusUpdatedAt: grant.StatusUpdatedAt}
	m := manifest{Version: 1, Grant: grant, Fence: state.Fence{AttemptID: state.AttemptID(a), Generation: 1, Owner: currentOwner}, CanonicalSpec: spec}
	id := identity{Version: 1, SuiteID: *suiteID, Scenario: *scenario, CandidateSHA: *candidateSHA, PRBaseSHA: *prBaseSHA, DisposableBaseSHA: *disposableBaseSHA, AttemptID: m.Fence.AttemptID, Generation: 1, FakeAgent: "fake-acp"}
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		return errors.New("cannot create fixture artifact directory")
	}
	configuration, err := yaml.Marshal(c)
	if err != nil || os.WriteFile(filepath.Join(*outDir, "config.yml"), configuration, 0600) != nil {
		return errors.New("cannot write fixture configuration")
	}
	if err := writeJSON(filepath.Join(*outDir, "manifest.json"), m); err != nil {
		return err
	}
	return writeJSON(filepath.Join(*outDir, "identity.json"), id)
}

// reportDenial is a test-only proof for the hosted scheduler's denied path.
// It cannot create a grant, candidate, or publication artifact.
func reportDenial(args []string) error {
	f := flag.NewFlagSet("deny", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	configPath, out := f.String("config", "", ""), f.String("out", "", "")
	suiteID, kind := f.String("suite-id", "", ""), f.String("denial-kind", "", "")
	candidateSHA, prBaseSHA, disposableBaseSHA := f.String("candidate-sha", "", ""), f.String("pr-base-sha", "", ""), f.String("disposable-base-sha", "", "")
	runID, runAttempt := f.String("run-id", "", ""), f.String("run-attempt", "", "")
	executeResult, verifyResult, publishResult := f.String("execute-result", "", ""), f.String("verify-result", "", ""), f.String("publish-result", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *configPath == "" || *out == "" || !suitePattern.MatchString(*suiteID) || (*kind != "non-ready" && *kind != "completed-redelivery") || !shaPattern.MatchString(*candidateSHA) || !shaPattern.MatchString(*prBaseSHA) || !shaPattern.MatchString(*disposableBaseSHA) || *executeResult != "skipped" || *verifyResult != "skipped" || *publishResult != "skipped" {
		return errors.New("invalid denied fixture input or hosted job was not skipped")
	}
	currentOwner, err := owner(*runID, *runAttempt)
	if err != nil {
		return err
	}
	c, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	snapshot := admission.Snapshot{
		Repository: c.Repository, RepositoryID: c.RepositoryID,
		IssueID: "I_sofa_e2e_" + *suiteID, Number: 1,
		Title: "Format fixture greeting", Body: "Format the approved fixture greeting file.", Open: true,
		ProjectID: c.ProjectID, ProjectPrivate: true, ProjectItemID: "PVTI_" + *suiteID,
		CurrentStatus: c.ReadyStatus, StatusOptionID: "ready-e2e", StatusUpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		BaseSHA: *disposableBaseSHA, Complete: true,
	}
	decision := ""
	if *kind == "non-ready" {
		snapshot.CurrentStatus = "In Progress"
		if _, _, err := admission.Authorize(c, snapshot); err == nil {
			return errors.New("non-Ready fixture received authorization")
		}
		decision = "admission-denied"
	} else {
		grant, _, err := admission.Authorize(c, snapshot)
		if err != nil {
			return err
		}
		a := state.Admission{Repository: grant.Repository, Issue: int64(grant.IssueNumber), SpecDigest: grant.SpecDigest, ConfigDigest: grant.ConfigDigest, BaseSHA: grant.BaseSHA, ProjectID: grant.ProjectID, ProjectItemID: grant.ProjectItemID, StatusOptionID: grant.StatusOptionID, StatusUpdatedAt: grant.StatusUpdatedAt}
		ctx := context.Background()
		engine := state.Engine{Store: &state.MemoryStore{}}
		attempt, created, err := engine.Admit(ctx, a, state.Limits{ModelCalls: 1, RuntimeSeconds: 600})
		if err != nil || !created {
			return errors.New("cannot seed completed fixture attempt")
		}
		fence, err := engine.Claim(ctx, attempt.ID, currentOwner)
		if err != nil || engine.Advance(ctx, fence, state.Validating) != nil {
			return errors.New("cannot advance completed fixture attempt")
		}
		publication := state.Publication{Branch: "sofa/e2e-completed", ExpectedHead: *disposableBaseSHA, CandidateDigest: fmt.Sprintf("%064x", sha256.Sum256([]byte(*suiteID)))}
		if err := engine.BeginPublication(ctx, fence, publication); err != nil {
			return err
		}
		publication.HeadSHA = *candidateSHA
		publication.PRNumber = 1
		publication.PRURL = "https://github.com/kevinmartin/sofa-disposable/pull/1"
		if err := engine.MarkPublished(ctx, fence, publication); err != nil {
			return err
		}
		replayed, created, err := engine.Admit(ctx, a, state.Limits{ModelCalls: 1, RuntimeSeconds: 600})
		if err != nil || created || replayed.ID != attempt.ID || replayed.Phase != state.Draft {
			return errors.New("completed redelivery minted a new attempt")
		}
		if _, err := engine.Claim(ctx, replayed.ID, currentOwner); !errors.Is(err, state.ErrClaimed) {
			return errors.New("completed redelivery was claimable")
		}
		decision = "already-completed"
	}
	report := struct {
		SchemaVersion      int      `json:"schema_version"`
		SuiteID            string   `json:"suite_id"`
		Scenario           string   `json:"scenario"`
		DenialKind         string   `json:"denial_kind"`
		Decision           string   `json:"decision"`
		CandidateSHA       string   `json:"candidate_sha"`
		PRBaseSHA          string   `json:"pr_base_sha"`
		DisposableBaseSHA  string   `json:"disposable_base_sha"`
		RunID              string   `json:"run_id"`
		RunAttempt         int      `json:"run_attempt"`
		SkippedJobs        []string `json:"skipped_jobs"`
		FakePromptRequests int      `json:"fake_prompt_requests"`
		ProviderRequests   int      `json:"provider_requests"`
		PublicationWrites  int      `json:"publication_writes"`
		WriteCredentials   int      `json:"write_credentials"`
	}{1, *suiteID, "denied", *kind, decision, *candidateSHA, *prBaseSHA, *disposableBaseSHA, currentOwner.RunID, currentOwner.RunAttempt, []string{"execute", "verify", "publish"}, 0, 0, 0, 0}
	return writeJSON(*out, report)
}

func recoverCandidate(args []string) error {
	f := flag.NewFlagSet("recover", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	fromDir, outDir, bundlePath := f.String("from-dir", "", ""), f.String("out-dir", "", ""), f.String("bundle", "", "")
	runID, runAttempt := f.String("run-id", "", ""), f.String("run-attempt", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *fromDir == "" || *outDir == "" || *bundlePath == "" {
		return errors.New("invalid fixture recovery input")
	}
	currentOwner, err := owner(*runID, *runAttempt)
	if err != nil {
		return err
	}
	var original manifest
	var id identity
	var bundle integrity.Bundle
	if err := readJSON(filepath.Join(*fromDir, "manifest.json"), &original); err != nil {
		return err
	}
	if err := readJSON(filepath.Join(*fromDir, "identity.json"), &id); err != nil {
		return err
	}
	bundleBytes, err := os.ReadFile(*bundlePath)
	if err != nil || len(bundleBytes) > 1<<20 {
		return errors.New("retained bundle unavailable or oversized")
	}
	if err := json.Unmarshal(bundleBytes, &bundle); err != nil {
		return err
	}
	if id.Version != 1 || id.FakeAgent != "fake-acp" || !suitePattern.MatchString(id.SuiteID) || !shaPattern.MatchString(id.CandidateSHA) || !shaPattern.MatchString(id.PRBaseSHA) || !shaPattern.MatchString(id.DisposableBaseSHA) || original.Version != 1 || original.Grant.Repository != disposableRepository || original.Grant.BaseSHA != id.DisposableBaseSHA || original.Fence.AttemptID != id.AttemptID || bundle.AttemptID != id.AttemptID || bundle.BaseSHA != id.DisposableBaseSHA || bundle.Generation != uint64(original.Fence.Generation) || bundle.CandidateDigest == "" || original.Fence.Owner == currentOwner {
		return errors.New("retained candidate identity mismatch")
	}
	h := sha256.Sum256(bundleBytes)
	now := time.Now().UTC()
	checkpoint := state.Checkpoint{Version: state.Version, Phase: state.Validating, ArtifactID: "sofa-e2e-candidate-" + id.SuiteID, Digest: hex.EncodeToString(h[:]), CandidateSHA: bundle.CandidateDigest, Producer: original.Fence.Owner, Generation: original.Fence.Generation, AcceptedAt: now.Add(-time.Second), ExpiresAt: now.Add(20 * time.Minute)}
	original.Fence.Generation++
	original.Fence.Owner = currentOwner
	original.RecoveryCheckpoint = &checkpoint
	producer := checkpoint.Producer
	original.RecoverySource = &producer
	id.Generation = original.Fence.Generation
	id.ProducerRunID = producer.RunID
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		return errors.New("cannot create recovery artifact directory")
	}
	configBytes, err := os.ReadFile(filepath.Join(*fromDir, "config.yml"))
	if err != nil || os.WriteFile(filepath.Join(*outDir, "config.yml"), configBytes, 0600) != nil {
		return errors.New("cannot carry fixture configuration")
	}
	if err := writeJSON(filepath.Join(*outDir, "manifest.json"), original); err != nil {
		return err
	}
	return writeJSON(filepath.Join(*outDir, "identity.json"), id)
}

func reportCandidate(args []string) error {
	f := flag.NewFlagSet("report", flag.ContinueOnError)
	f.SetOutput(os.Stderr)
	identityPath, manifestPath, bundlePath := f.String("identity", "", ""), f.String("manifest", "", ""), f.String("bundle", "", "")
	executionPath, evidencePath, publicationPath := f.String("execution", "", ""), f.String("evidence", "", ""), f.String("publication", "", "")
	out := f.String("out", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 || *identityPath == "" || *manifestPath == "" || *bundlePath == "" || *executionPath == "" || *evidencePath == "" || *publicationPath == "" || *out == "" {
		return errors.New("invalid report input")
	}
	var id identity
	var m manifest
	var b integrity.Bundle
	var execution struct {
		Version         int    `json:"version"`
		UsedAgent       bool   `json:"used_agent"`
		PromptRequests  int    `json:"prompt_requests"`
		ModelCalls      *int   `json:"model_calls"`
		CandidateDigest string `json:"candidate_digest"`
	}
	var evidence []integrity.CheckEvidence
	var publication struct {
		SchemaVersion    int    `json:"schema_version"`
		Simulation       string `json:"simulation"`
		CandidateDigest  string `json:"candidate_digest"`
		BaseSHA          string `json:"base_sha"`
		AttemptID        string `json:"attempt_id"`
		Generation       uint64 `json:"generation"`
		PRNumber         int64  `json:"pr_number"`
		PRURL            string `json:"pr_url"`
		PRPosts          int    `json:"pr_posts"`
		ProviderRequests int    `json:"provider_requests"`
	}
	for _, item := range []struct {
		path  string
		value any
	}{
		{*identityPath, &id}, {*manifestPath, &m}, {*bundlePath, &b},
		{*executionPath, &execution}, {*evidencePath, &evidence}, {*publicationPath, &publication},
	} {
		if err := readJSON(item.path, item.value); err != nil {
			return err
		}
	}
	if id.Version != 1 || id.FakeAgent != "fake-acp" || !suitePattern.MatchString(id.SuiteID) || id.Scenario != "edit" || !shaPattern.MatchString(id.CandidateSHA) || !shaPattern.MatchString(id.PRBaseSHA) || !shaPattern.MatchString(id.DisposableBaseSHA) || m.Version != 1 || m.Grant.Repository != disposableRepository || m.Grant.BaseSHA != id.DisposableBaseSHA || m.Fence.AttemptID != id.AttemptID || m.Fence.Generation != id.Generation || b.AttemptID != id.AttemptID || b.BaseSHA != id.DisposableBaseSHA || b.Generation > uint64(id.Generation) || b.CandidateDigest == "" || execution.Version != 1 || !execution.UsedAgent || execution.PromptRequests != 1 || execution.ModelCalls != nil || execution.CandidateDigest != b.CandidateDigest || publication.SchemaVersion != 1 || publication.Simulation != "fake-github-transport" || publication.CandidateDigest != b.CandidateDigest || publication.BaseSHA != b.BaseSHA || publication.AttemptID != b.AttemptID || publication.Generation != b.Generation || publication.PRNumber < 1 || publication.PRURL == "" || publication.PRPosts != 1 || publication.ProviderRequests != 0 || len(evidence) != 1 || !evidence[0].Passed || evidence[0].CandidateDigest != b.CandidateDigest {
		return errors.New("candidate report identity or fake-ACP evidence mismatch")
	}
	if m.RecoveryCheckpoint != nil && (m.RecoverySource == nil || m.RecoveryCheckpoint.CandidateSHA != b.CandidateDigest || uint64(m.RecoveryCheckpoint.Generation) != b.Generation || m.RecoverySource.RunID != id.ProducerRunID) {
		return errors.New("retained candidate provenance mismatch")
	}
	report := struct {
		SchemaVersion        int    `json:"schema_version"`
		SuiteID              string `json:"suite_id"`
		Scenario             string `json:"scenario"`
		CandidateSHA         string `json:"candidate_sha"`
		PRBaseSHA            string `json:"pr_base_sha"`
		DisposableBaseSHA    string `json:"disposable_base_sha"`
		AttemptID            string `json:"attempt_id"`
		Generation           int64  `json:"generation"`
		BundleGeneration     uint64 `json:"bundle_generation"`
		ProducerRunID        string `json:"producer_run_id,omitempty"`
		CandidateDigest      string `json:"candidate_digest"`
		FakeAgent            string `json:"fake_agent"`
		FakePromptRequests   int    `json:"fake_prompt_requests"`
		ProviderRequests     int    `json:"provider_requests"`
		ProviderRequestBasis string `json:"provider_request_basis"`
		SimulatedPRNumber    int64  `json:"simulated_pr_number"`
		SimulatedPRURL       string `json:"simulated_pr_url"`
		SimulatedPRPostCount int    `json:"simulated_pr_post_count"`
		VerifiedCheckCount   int    `json:"verified_check_count"`
		RealPublicationOwner string `json:"real_publication_owner"`
	}{1, id.SuiteID, id.Scenario, id.CandidateSHA, id.PRBaseSHA, id.DisposableBaseSHA, id.AttemptID, id.Generation, b.Generation, id.ProducerRunID, b.CandidateDigest, id.FakeAgent, execution.PromptRequests, publication.ProviderRequests, "networkless-container-and-fake-peer-without-provider-client", publication.PRNumber, publication.PRURL, publication.PRPosts, len(evidence), "trusted-disposable-coordinator-only"}
	return writeJSON(*out, report)
}
