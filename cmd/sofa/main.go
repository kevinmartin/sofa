package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/agent"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
	"github.com/kevinmartin/sofa/internal/worker"
)

// Manifest is emitted by the privileged admission job, then treated as input
// data by each later stage. Publication checks it against live source and state.
type Manifest struct {
	Version            int                `json:"version"`
	Grant              admission.Grant    `json:"grant"`
	Fence              state.Fence        `json:"fence"`
	CanonicalSpec      json.RawMessage    `json:"canonical_spec"`
	Recovery           *state.Publication `json:"recovery_publication,omitempty"`
	RecoveryCheckpoint *state.Checkpoint  `json:"recovery_checkpoint,omitempty"`
	RecoverySource     *state.Owner       `json:"recovery_source,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		// Package errors and local validation messages never include token values.
		fmt.Fprintln(os.Stderr, "sofa:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("command required: admit, execute, verify, publish")
	}
	switch args[0] {
	case "admit":
		return admit(ctx, args[1:])
	case "execute":
		return execute(ctx, args[1:])
	case "verify":
		return verify(ctx, args[1:])
	case "publish":
		return publish(ctx, args[1:])
	case "fail":
		return fail(ctx, args[1:])
	case "spec-digest":
		return specDigest(args[1:])
	default:
		return errors.New("unknown command")
	}
}

func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	return f
}

func readConfig(name string) (config.Config, error) {
	f, err := os.Open(name)
	if err != nil {
		return config.Config{}, errors.New("cannot read configuration")
	}
	defer f.Close()
	return config.Decode(f)
}

func clientFromEnv(name string) (*github.Client, error) {
	return github.New(os.Getenv(name), nil)
}

func ownerFromEnv() (state.Owner, error) {
	attempt, err := strconv.Atoi(os.Getenv("GITHUB_RUN_ATTEMPT"))
	if err != nil || attempt < 1 || os.Getenv("GITHUB_RUN_ID") == "" {
		return state.Owner{}, errors.New("Actions run identity unavailable")
	}
	return state.Owner{RunID: os.Getenv("GITHUB_RUN_ID"), RunAttempt: attempt}, nil
}

func writeJSON(name string, value any) error {
	if name == "" {
		return errors.New("output path required")
	}
	// Raw canonical spec bytes are part of the admitted digest. Reformatting
	// nested JSON while serializing a manifest would silently change identity.
	b, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode output")
	}
	b = append(b, '\n')
	if err := os.WriteFile(name, b, 0600); err != nil {
		return errors.New("cannot write output")
	}
	return nil
}

func readJSON(name string, max int64, value any) error {
	f, err := os.Open(name)
	if err != nil {
		return errors.New("cannot read input artifact")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return errors.New("input artifact unavailable or oversized")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid input artifact")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing input artifact data")
	}
	return nil
}

func readManifest(name string, c config.Config) (Manifest, error) {
	var m Manifest
	if err := readJSON(name, 128<<10, &m); err != nil {
		return m, err
	}
	digest, err := c.Digest()
	if err != nil {
		return m, err
	}
	h := sha256.Sum256(m.CanonicalSpec)
	if m.Version != 1 || m.Grant.Version != 1 || m.Grant.Repository != c.Repository || m.Grant.RepositoryID != c.RepositoryID || m.Grant.ProjectID != c.ProjectID || m.Grant.OwnerID != c.OwnerID || m.Grant.ConfigDigest != digest || m.Grant.SpecDigest != hex.EncodeToString(h[:]) || m.Fence.AttemptID == "" || m.Fence.Generation < 1 || m.Fence.Owner.RunID == "" || m.Fence.Owner.RunAttempt < 1 || m.Grant.BaseSHA == "" {
		return Manifest{}, errors.New("manifest identity does not match configuration")
	}
	a := ledgerAdmission(m.Grant)
	if err := a.Validate(); err != nil || state.AttemptID(a) != m.Fence.AttemptID {
		return Manifest{}, errors.New("manifest admission identity invalid")
	}
	if (m.Recovery == nil && m.RecoveryCheckpoint == nil) != (m.RecoverySource == nil) || (m.Recovery != nil && m.RecoveryCheckpoint != nil) {
		return Manifest{}, errors.New("incomplete candidate recovery manifest")
	}
	return m, nil
}

func ledgerAdmission(g admission.Grant) state.Admission {
	return state.Admission{Repository: g.Repository, Issue: int64(g.IssueNumber), SpecDigest: g.SpecDigest, ConfigDigest: g.ConfigDigest, BaseSHA: g.BaseSHA, ProjectID: g.ProjectID, ProjectItemID: g.ProjectItemID, StatusOptionID: g.StatusOptionID, StatusUpdatedAt: g.StatusUpdatedAt}
}

func observeOnce(ctx context.Context, engine state.Engine, attemptID, stage, outcome, revision, evidenceRef, scope string) error {
	id := stage + "-" + attemptID
	if scope != "" {
		id += "-" + scope
	}
	lookup := func() (bool, error) {
		snapshot, err := engine.Store.Load(ctx)
		if err != nil {
			return false, err
		}
		for _, prior := range snapshot.State.Observations {
			if prior.ID == id {
				if prior.AttemptID == attemptID && prior.Stage == stage && prior.Outcome == outcome && prior.Revision == revision && prior.EvidenceRef == evidenceRef {
					return true, nil
				}
				return false, state.ErrConflict
			}
		}
		return false, nil
	}
	if found, err := lookup(); err != nil || found {
		return err
	}
	err := engine.Observe(ctx, state.Observation{Version: state.Version, ID: id, AttemptID: attemptID, Stage: stage, Outcome: outcome, Revision: revision, EvidenceRef: evidenceRef, RecordedAt: time.Now().UTC()})
	if errors.Is(err, state.ErrConflict) {
		if found, currentErr := lookup(); currentErr != nil || found {
			return currentErr
		}
	}
	return err
}

func admit(ctx context.Context, args []string) error {
	f := flags("admit")
	configPath := f.String("config", "", "")
	issue := f.Int("issue", 0, "")
	outDir := f.String("out-dir", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *issue < 1 || *outDir == "" {
		return errors.New("admit requires --config, --issue, --out-dir")
	}
	c, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	owner, err := ownerFromEnv()
	if err != nil {
		return err
	}
	projects, err := clientFromEnv("SOFA_PROJECTS_TOKEN")
	if err != nil {
		return err
	}
	ledgerClient, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	snapshot, err := projects.Issue(ctx, c, *issue)
	if err != nil {
		return err
	}
	grant, spec, err := admission.Authorize(c, snapshot)
	if err != nil {
		return err
	}
	if err := integrity.ScanSecrets(spec, [][]byte{[]byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_STATE_TOKEN"))}); err != nil {
		return errors.New("approved specification contains sensitive material")
	}
	store := github.StateStore{Client: ledgerClient, Repository: c.Repository}
	engine := state.Engine{Store: store}
	maxAttempts := int64(c.Limits.InfraRetries + 1)
	limits := state.Limits{ModelCalls: int64(c.Limits.MaxAgentTurns) * maxAttempts, Repairs: int64(c.Limits.RepairAttempts), InfrastructureRetries: int64(c.Limits.InfraRetries), RuntimeSeconds: int64(c.Limits.AttemptSeconds) * maxAttempts}
	attempt, _, err := engine.Admit(ctx, ledgerAdmission(grant), limits)
	if err != nil {
		return err
	}
	if err := observeOnce(ctx, engine, attempt.ID, "admission", "accepted", grant.BaseSHA, "", ""); err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		return errors.New("cannot create admission output")
	}
	status := filepath.Join(*outDir, "status.json")
	if attempt.Owner != nil && attempt.Phase != state.Draft && attempt.Phase != state.Blocked {
		proof, proofErr := ledgerClient.RunProof(ctx, c.Repository, *attempt.Owner)
		if proofErr == nil {
			if err := engine.Recover(ctx, attempt.ID, proof); err == nil {
				loaded, loadErr := store.Load(ctx)
				if loadErr != nil {
					return loadErr
				}
				attempt = loaded.State.Attempts[attempt.ID]
			} else if !errors.Is(err, state.ErrActive) {
				return err
			}
		}
	}
	if attempt.Phase == state.Draft || attempt.Phase == state.Blocked || attempt.Phase == state.Deferred || attempt.Owner != nil {
		reason := "already-active"
		if attempt.Phase == state.Draft {
			if attempt.Publication == nil {
				return errors.New("draft attempt has no publication identity")
			}
			if err := observeOnce(ctx, engine, attempt.ID, "publication", "draft", attempt.Publication.HeadSHA, attempt.Publication.PRURL, ""); err != nil {
				return err
			}
			reason = "already-complete"
		} else if attempt.Phase == state.Blocked || attempt.Phase == state.Deferred {
			reason = "blocked"
		}
		return writeJSON(status, map[string]any{"dispatch": false, "reason": reason, "attempt_id": attempt.ID})
	}
	if attempt.Checkpoint != nil {
		available := attempt.Checkpoint.ExpiresAt.After(time.Now().UTC())
		if available {
			available, err = ledgerClient.RetainedCandidateAvailable(ctx, c.Repository, *attempt.Checkpoint)
			if err != nil {
				return err // An API failure cannot justify discarding a checkpoint.
			}
		}
		if !available {
			if attempt.Publication != nil {
				return errors.New("publication recovery candidate unavailable; inspect existing branch and PR")
			}
			if err := engine.DiscardUnavailableCheckpoint(ctx, attempt.ID, *attempt.Checkpoint); err != nil {
				return err
			}
			loaded, loadErr := store.Load(ctx)
			if loadErr != nil {
				return loadErr
			}
			attempt = loaded.State.Attempts[attempt.ID]
		}
	}
	if attempt.Publication != nil && attempt.Checkpoint == nil {
		return errors.New("publication recovery checkpoint unavailable; inspect existing branch and PR")
	}
	if err := store.SaveSpec(ctx, grant.IssueID, grant.SpecDigest, spec); err != nil {
		return err
	}
	fence, err := engine.Claim(ctx, attempt.ID, owner)
	if err != nil {
		return err
	}
	if attempt.Publication == nil && attempt.Checkpoint == nil {
		charge := state.Counters{RuntimeSeconds: int64(c.Limits.AttemptSeconds)}
		if !(c.Recipe != nil && strings.Contains(snapshot.Body, "<!-- sofa:recipe=gofmt -->")) {
			charge.ModelCalls = 1 // One ACP prompt reservation; internal provider calls remain unknown.
		}
		if err := engine.Charge(ctx, fence, charge); err != nil {
			kind := "infrastructure"
			if errors.Is(err, state.ErrLimit) {
				kind = "validation"
			}
			_ = engine.Fail(ctx, fence, kind)
			return err
		}
	}
	manifest := Manifest{Version: 1, Grant: grant, Fence: fence, CanonicalSpec: spec}
	if attempt.Publication != nil || attempt.Checkpoint != nil {
		// The last terminal owner may be a second or third recovery run. The
		// retained artifact is identified by the checkpoint's original producer.
		artifactSource := attempt.Checkpoint.Producer
		manifest.RecoverySource = &artifactSource
		if attempt.Publication != nil {
			manifest.Recovery = attempt.Publication
		} else {
			manifest.RecoveryCheckpoint = attempt.Checkpoint
		}
	}
	if err := writeJSON(filepath.Join(*outDir, "manifest.json"), manifest); err != nil {
		return err
	}
	return writeJSON(status, map[string]any{"dispatch": true, "attempt_id": attempt.ID, "reconcile_candidate": manifest.RecoverySource != nil, "reconcile_publication": manifest.Recovery != nil, "recovery_run_id": func() string {
		if manifest.RecoverySource != nil {
			return manifest.RecoverySource.RunID
		}
		return ""
	}()})
}

func forbidPrivilegedEnv(modelEnv string, includeModel bool) error {
	names := []string{"SOFA_PROJECTS_TOKEN", "SOFA_STATE_TOKEN", "SOFA_PUBLISH_TOKEN", "SOFA_APP_PRIVATE_KEY"}
	if includeModel {
		names = append(names, modelEnv, "GITHUB_TOKEN", "GH_TOKEN")
	}
	for _, name := range names {
		if os.Getenv(name) != "" {
			return errors.New("privileged credential present in secretless stage")
		}
	}
	return nil
}

type ExecutionFailure struct {
	Version        int    `json:"version"`
	AttemptID      string `json:"attempt_id"`
	Generation     int64  `json:"generation"`
	Kind           string `json:"kind"`
	Reason         string `json:"reason,omitempty"`
	UsedAgent      bool   `json:"used_agent,omitempty"`
	PromptRequests int    `json:"prompt_requests,omitempty"`
}

var errExecutionValidation = errors.New("execution input or candidate validation failed")

func executionFailureKind(err error) string {
	switch {
	case errors.Is(err, agent.ErrAuthentication):
		return "authentication"
	case errors.Is(err, agent.ErrQuota):
		return "quota"
	case errors.Is(err, agent.ErrPermission):
		return "validation"
	case errors.Is(err, worker.ErrValidation), errors.Is(err, errExecutionValidation):
		return "validation"
	default:
		return "infrastructure"
	}
}

func validateExecutionFailure(f ExecutionFailure, m Manifest) error {
	if f.AttemptID != m.Fence.AttemptID || f.Generation != m.Fence.Generation {
		return errors.New("failure artifact identity mismatch")
	}
	switch f.Kind {
	case "authentication", "quota", "infrastructure", "validation":
	default:
		return errors.New("unsupported failure class")
	}
	// Version one is the workflow's bounded infrastructure fallback when the
	// execution job could not upload an artifact. It cannot assert telemetry.
	if f.Version == 1 && f.Kind == "infrastructure" && f.Reason == "" && !f.UsedAgent && f.PromptRequests == 0 {
		return nil
	}
	if f.Version != 2 || f.PromptRequests < 0 || f.PromptRequests > 1 || (!f.UsedAgent && f.PromptRequests != 0) {
		return errors.New("invalid execution failure telemetry")
	}
	switch f.Reason {
	case "recovery-input", "base-checkout", "candidate-no-change", "worker-validation", "agent-error", "artifact-write", "unexpected":
	default:
		return errors.New("unsupported execution failure reason")
	}
	if (f.Reason == "recovery-input" || f.Reason == "base-checkout") && (f.UsedAgent || f.PromptRequests != 0) {
		return errors.New("invalid pre-execution failure telemetry")
	}
	switch f.Reason {
	case "recovery-input", "base-checkout", "candidate-no-change", "worker-validation":
		if f.Kind != "validation" {
			return errors.New("execution failure reason and class mismatch")
		}
	case "artifact-write", "unexpected":
		if f.Kind != "infrastructure" {
			return errors.New("execution failure reason and class mismatch")
		}
	}
	return nil
}

func execute(ctx context.Context, args []string) (retErr error) {
	f := flags("execute")
	configPath, manifestPath, workspace, out := f.String("config", "", ""), f.String("manifest", "", ""), f.String("workspace", "", ""), f.String("out", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *manifestPath == "" || *workspace == "" || *out == "" {
		return errors.New("execute requires --config, --manifest, --workspace, --out")
	}
	c, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, false); err != nil {
		return err
	}
	m, err := readManifest(*manifestPath, c)
	if err != nil {
		return err
	}
	failure := ExecutionFailure{Version: 2, AttemptID: m.Fence.AttemptID, Generation: m.Fence.Generation, Reason: "unexpected"}
	defer func() {
		if retErr != nil {
			failure.Kind = executionFailureKind(retErr)
			_ = writeJSON(filepath.Join(filepath.Dir(*out), "execution-failure.json"), failure)
			fmt.Fprintf(os.Stderr, "sofa: execution failure reason=%s used_agent=%t prompt_requests=%d\n", failure.Reason, failure.UsedAgent, failure.PromptRequests)
		}
	}()
	if m.Recovery != nil || m.RecoveryCheckpoint != nil {
		failure.Reason = "recovery-input"
		return errExecutionValidation
	}
	if err := cleanBase(ctx, *workspace, m.Grant.BaseSHA); err != nil {
		failure.Reason = "base-checkout"
		return fmt.Errorf("%w: admitted base checkout", errExecutionValidation)
	}
	result, err := worker.Execute(ctx, worker.Input{Config: c, CanonicalSpec: m.CanonicalSpec, Directory: *workspace, AttemptID: m.Fence.AttemptID, Generation: uint64(m.Fence.Generation), BaseSHA: m.Grant.BaseSHA, ModelToken: os.Getenv(c.Profile.SecretEnv)})
	failure.UsedAgent = result.UsedAgent
	failure.PromptRequests = result.PromptRequests
	if err != nil {
		failure.Reason = "agent-error"
		if errors.Is(err, worker.ErrValidation) {
			failure.Reason = "worker-validation"
		}
		return err
	}
	if result.NoChange {
		failure.Reason = "candidate-no-change"
		return fmt.Errorf("%w: no candidate changes", errExecutionValidation)
	}
	failure.Reason = "artifact-write"
	if err := writeJSON(*out, result.Bundle); err != nil {
		return err
	}
	return writeJSON(filepath.Join(filepath.Dir(*out), "execution.json"), struct {
		Version         int    `json:"version"`
		UsedAgent       bool   `json:"used_agent"`
		PromptRequests  int    `json:"prompt_requests"`
		ModelCalls      *int   `json:"model_calls"`
		CandidateDigest string `json:"candidate_digest"`
	}{Version: 1, UsedAgent: result.UsedAgent, PromptRequests: result.PromptRequests, ModelCalls: result.ModelCalls, CandidateDigest: result.Bundle.CandidateDigest})
}

func cleanBase(ctx context.Context, workspace, expected string) error {
	if !filepath.IsAbs(workspace) {
		return errors.New("workspace must be absolute")
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "rev-parse", "HEAD")
	cmd.Dir = workspace
	cmd.Env = gitEnv()
	b, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(b)) != expected {
		return errors.New("workspace is not at admitted base")
	}
	cmd = exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=all")
	cmd.Dir = workspace
	cmd.Env = gitEnv()
	b, err = cmd.Output()
	if err != nil || len(b) != 0 {
		return errors.New("workspace must be clean before candidate work")
	}
	return nil
}

func gitEnv() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
}

func readBundle(name string) (integrity.Bundle, error) {
	f, err := os.Open(name)
	if err != nil {
		return integrity.Bundle{}, errors.New("cannot read candidate bundle")
	}
	defer f.Close()
	return integrity.Decode(f, integrity.MaxEncodedBytes)
}

func bundlePolicy(c config.Config, forbidden ...[]byte) integrity.Policy {
	return integrity.Policy{AllowedPaths: c.AllowedPaths, MaxFiles: c.Limits.MaxFiles, MaxFileBytes: c.Limits.MaxFileBytes, MaxTotalBytes: c.Limits.MaxTotalBytes, ForbiddenValues: forbidden}
}

func bundleExpected(m Manifest, b integrity.Bundle) integrity.Expected {
	generation := uint64(m.Fence.Generation)
	if m.Recovery != nil && b.Generation < generation && b.CandidateDigest == m.Recovery.CandidateDigest {
		generation = b.Generation
	}
	if m.RecoveryCheckpoint != nil && b.Generation == uint64(m.RecoveryCheckpoint.Generation) && b.Generation < generation && b.CandidateDigest == m.RecoveryCheckpoint.CandidateSHA {
		generation = b.Generation
	}
	return integrity.Expected{Repository: m.Grant.Repository, AttemptID: m.Fence.AttemptID, Generation: generation, BaseSHA: m.Grant.BaseSHA, CandidateDigest: b.CandidateDigest}
}

func verify(ctx context.Context, args []string) error {
	f := flags("verify")
	configPath, manifestPath, workspace, bundlePath, out := f.String("config", "", ""), f.String("manifest", "", ""), f.String("workspace", "", ""), f.String("bundle", "", ""), f.String("out", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *manifestPath == "" || *workspace == "" || *bundlePath == "" || *out == "" {
		return errors.New("verify requires --config, --manifest, --workspace, --bundle, --out")
	}
	c, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, true); err != nil {
		return err
	}
	checkHome, err := os.MkdirTemp("", "sofa-check-home-")
	if err != nil {
		return errors.New("cannot create isolated check home")
	}
	defer os.RemoveAll(checkHome)
	m, err := readManifest(*manifestPath, c)
	if err != nil {
		return err
	}
	b, err := readBundle(*bundlePath)
	if err != nil {
		return err
	}
	if err := cleanBase(ctx, *workspace, m.Grant.BaseSHA); err != nil {
		return err
	}
	if err := integrity.Apply(*workspace, b, bundleExpected(m, b), bundlePolicy(c)); err != nil {
		return err
	}
	checks := make([]integrity.CheckEvidence, 0, len(c.Checks))
	for _, check := range c.Checks {
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
		cmd := exec.CommandContext(checkCtx, check.Argv[0], check.Argv[1:]...)
		cmd.Dir = *workspace
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + checkHome, "GOCACHE=" + filepath.Join(checkHome, "cache"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GOPROXY=off", "GOSUMDB=off", "GOENV=off"}
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		err := cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("configured check %s failed", check.ID)
		}
		checks = append(checks, integrity.CheckEvidence{Version: integrity.Version, Name: check.ID, CandidateDigest: b.CandidateDigest, Passed: true})
	}
	return writeJSON(*out, checks)
}

func publish(ctx context.Context, args []string) error {
	f := flags("publish")
	configPath, manifestPath, bundlePath, evidencePath, baseBranch := f.String("config", "", ""), f.String("manifest", "", ""), f.String("bundle", "", ""), f.String("evidence", "", ""), f.String("base-branch", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *manifestPath == "" || *bundlePath == "" || *evidencePath == "" || *baseBranch == "" {
		return errors.New("publish requires --config, --manifest, --bundle, --evidence, --base-branch")
	}
	c, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	if os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("model credential present in publication stage")
	}
	m, err := readManifest(*manifestPath, c)
	if err != nil {
		return err
	}
	currentOwner, err := ownerFromEnv()
	if err != nil || currentOwner != m.Fence.Owner {
		return errors.New("publication manifest does not belong to this Actions attempt")
	}
	b, err := readBundle(*bundlePath)
	if err != nil {
		return err
	}
	var evidence []integrity.CheckEvidence
	if err := readJSON(*evidencePath, 1<<20, &evidence); err != nil {
		return err
	}
	expected := bundleExpected(m, b)
	policy := bundlePolicy(c, []byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_PUBLISH_TOKEN")), []byte(os.Getenv(c.Profile.SecretEnv)))
	if err := integrity.Validate(b, expected, policy); err != nil {
		return err
	}
	required := make([]string, 0, len(c.Checks))
	for _, check := range c.Checks {
		required = append(required, check.ID)
	}
	if err := integrity.ValidateEvidence(b, evidence, required); err != nil {
		return err
	}
	projects, err := clientFromEnv("SOFA_PROJECTS_TOKEN")
	if err != nil {
		return err
	}
	publisher, err := clientFromEnv("SOFA_PUBLISH_TOKEN")
	if err != nil {
		return err
	}
	store := github.StateStore{Client: publisher, Repository: c.Repository}
	engine := state.Engine{Store: store}
	guard := func(ctx context.Context) error {
		if err := engine.AssertOwner(ctx, m.Fence); err != nil {
			return err
		}
		snapshot, err := projects.Issue(ctx, c, m.Grant.IssueNumber)
		if err != nil {
			return err
		}
		return admission.Revalidate(c, snapshot, m.Grant)
	}
	if err := guard(ctx); err != nil {
		return err
	}
	current, err := store.Load(ctx)
	if err != nil {
		return err
	}
	attempt, ok := current.State.Attempts[m.Fence.AttemptID]
	if !ok || attempt.Admission != ledgerAdmission(m.Grant) || attempt.Generation != m.Fence.Generation || attempt.Owner == nil || *attempt.Owner != m.Fence.Owner {
		return errors.New("publication ledger identity mismatch")
	}
	if err := observeOnce(ctx, engine, m.Fence.AttemptID, "execution", "candidate", b.CandidateDigest, "", ""); err != nil {
		return err
	}
	if err := observeOnce(ctx, engine, m.Fence.AttemptID, "verification", "passed", b.CandidateDigest, "", ""); err != nil {
		return err
	}
	if m.Recovery != nil && (attempt.Publication == nil || *attempt.Publication != *m.Recovery) {
		return errors.New("publication recovery identity mismatch")
	}
	if m.RecoveryCheckpoint != nil && (attempt.Checkpoint == nil || *attempt.Checkpoint != *m.RecoveryCheckpoint || attempt.Publication != nil) {
		return errors.New("checkpoint recovery identity mismatch")
	}
	if attempt.Phase == state.Executing {
		if err := engine.Advance(ctx, m.Fence, state.Validating); err != nil {
			return err
		}
	}
	if (attempt.Phase == state.Executing || attempt.Phase == state.Validating) && m.Recovery == nil && m.RecoveryCheckpoint == nil {
		now := time.Now().UTC()
		checkpoint := state.Checkpoint{Version: state.Version, Phase: state.Validating, ArtifactID: fmt.Sprintf("sofa-verified-candidate-%s-%d", m.Fence.Owner.RunID, m.Fence.Owner.RunAttempt), Digest: b.CandidateDigest, CandidateSHA: b.CandidateDigest, Producer: m.Fence.Owner, Generation: m.Fence.Generation, AcceptedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if err := engine.SaveCheckpoint(ctx, m.Fence, checkpoint); err != nil {
			return err
		}
	}
	intent := state.Publication{Branch: "sofa/" + b.AttemptID[:24], ExpectedHead: b.BaseSHA, CandidateDigest: b.CandidateDigest}
	if err := engine.BeginPublication(ctx, m.Fence, intent); err != nil {
		return err
	}
	var spec struct{ Title, Body string }
	if err := json.Unmarshal(m.CanonicalSpec, &spec); err != nil {
		return errors.New("invalid publication specification")
	}
	result, err := publisher.PublishDraft(ctx, github.PublishInput{Bundle: b, Expected: expected, Policy: policy, Checks: evidence, RequiredChecks: required, BaseBranch: *baseBranch, Title: spec.Title, Body: fmt.Sprintf("Draft for approved issue #%d.\n\nCandidate %s", m.Grant.IssueNumber, b.CandidateDigest), Guard: guard})
	if err != nil {
		return err
	}
	intent.HeadSHA, intent.PRNumber, intent.PRURL = result.CommitSHA, result.Number, result.URL
	if err := engine.MarkPublished(ctx, m.Fence, intent); err != nil {
		return err
	}
	if err := observeOnce(ctx, engine, m.Fence.AttemptID, "publication", "draft", result.CommitSHA, result.URL, ""); err != nil {
		return err
	}
	return writeJSON("publication.json", result)
}

// fail is a privileged finalizer for a failed, secretless execution job. The
// worker supplies only a fixed failure class; it cannot mint ownership.
func fail(ctx context.Context, args []string) error {
	f := flags("fail")
	configPath, manifestPath, failurePath := f.String("config", "", ""), f.String("manifest", "", ""), f.String("failure", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *configPath == "" || *manifestPath == "" || *failurePath == "" {
		return errors.New("fail requires --config, --manifest, --failure")
	}
	c, err := readConfig(*configPath)
	if err != nil {
		return err
	}
	if os.Getenv("SOFA_PROJECTS_TOKEN") != "" || os.Getenv("SOFA_PUBLISH_TOKEN") != "" || os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("finalizer has an unnecessary privileged credential")
	}
	m, err := readManifest(*manifestPath, c)
	if err != nil {
		return err
	}
	currentOwner, err := ownerFromEnv()
	if err != nil || currentOwner != m.Fence.Owner {
		return errors.New("failure manifest does not belong to this Actions attempt")
	}
	var failure ExecutionFailure
	if err := readJSON(*failurePath, 4096, &failure); err != nil {
		return err
	}
	if err := validateExecutionFailure(failure, m); err != nil {
		return err
	}
	client, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	engine := state.Engine{Store: github.StateStore{Client: client, Repository: c.Repository}}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		return err
	}
	attempt, ok := snapshot.State.Attempts[m.Fence.AttemptID]
	if !ok || attempt.Admission != ledgerAdmission(m.Grant) || attempt.Generation != m.Fence.Generation || attempt.Owner == nil || *attempt.Owner != m.Fence.Owner {
		return errors.New("failure ledger identity mismatch")
	}
	if !((attempt.Phase == state.Blocked || attempt.Phase == state.Deferred) && attempt.Failure == failure.Kind) {
		if err := engine.AssertOwner(ctx, m.Fence); err != nil {
			return err
		}
		if err := engine.Fail(ctx, m.Fence, failure.Kind); err != nil {
			return err
		}
	}
	return observeOnce(ctx, engine, m.Fence.AttemptID, "failure-finalizer", failure.Kind, m.Grant.BaseSHA, "", fmt.Sprintf("g%d", m.Fence.Generation))
}

func specDigest(args []string) error {
	f := flags("spec-digest")
	title, bodyFile := f.String("title", "", ""), f.String("body-file", "", "")
	if f.Parse(args) != nil || f.NArg() != 0 || *title == "" || *bodyFile == "" {
		return errors.New("spec-digest requires --title and --body-file")
	}
	fh, err := os.Open(*bodyFile)
	if err != nil {
		return errors.New("cannot read issue body")
	}
	defer fh.Close()
	content, err := io.ReadAll(io.LimitReader(fh, (64<<10)+1))
	if err != nil || len(content) > 64<<10 {
		return errors.New("issue body unavailable or oversized")
	}
	_, digest, err := admission.CanonicalSpec(*title, string(content))
	if err != nil {
		return err
	}
	fmt.Println(digest)
	return nil
}
