package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/review"
	"github.com/kevinmartin/sofa/internal/state"
	"github.com/kevinmartin/sofa/internal/worker"
)

type repairManifest struct {
	Version       int                `json:"version"`
	Admission     state.Admission    `json:"admission"`
	Fence         state.Fence        `json:"fence"`
	Publication   state.Publication  `json:"publication"`
	Repair        state.RepairIntent `json:"repair"`
	ReviewID      int64              `json:"review_id"`
	CanonicalSpec json.RawMessage    `json:"canonical_spec"`
	FeedbackText  string             `json:"feedback_text"`
}

// readRepairManifest accepts manifests up to 256 KiB and verifies configuration, specification,
// feedback, and PR bindings. It returns read or validation errors and does not
// establish live ownership or review authority.
func readRepairManifest(path string, c config.Config) (repairManifest, error) {
	var m repairManifest
	if err := readJSON(path, 256<<10, &m); err != nil {
		return m, err
	}
	configDigest, err := c.Digest()
	if err != nil {
		return m, err
	}
	specHash := sha256.Sum256(m.CanonicalSpec)
	feedbackHash := sha256.Sum256([]byte(m.FeedbackText))
	if m.Version != 1 || m.Admission.Validate() != nil || m.Admission.Repository != strings.ToLower(c.Repository) || m.Admission.ConfigDigest != configDigest || m.Admission.SpecDigest != hex.EncodeToString(specHash[:]) || m.Fence.AttemptID != state.AttemptID(m.Admission) || m.Fence.Generation < 1 || m.Fence.Owner.RunID == "" || m.Fence.Owner.RunAttempt < 1 || m.Repair.FeedbackID == "" || m.Repair.FeedbackHash != hex.EncodeToString(feedbackHash[:]) || m.Repair.PRNumber != m.Publication.PRNumber || m.Repair.PRHeadSHA != m.Publication.HeadSHA || m.Publication.PRNumber < 1 || m.ReviewID < 1 || len(m.FeedbackText) > 64<<10 {
		return repairManifest{}, errors.New("repair manifest identity invalid")
	}
	return m, nil
}

// newReviewRepairCommand wires the admission, execution, verification,
// publication, and failure stages.
func newReviewRepairCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "review-repair",
		Short: "Admit and repair an existing PR from exact owner review feedback",
	}
	var admit struct {
		configPath string
		issue      int
		outDir     string
	}
	admitCmd := newStageCommand("admit", "Reserve an owner review repair", "review-repair admit requires --config, --issue, --out-dir", func(cmd *cobra.Command, _ []string) error {
		if admit.configPath == "" || admit.issue < 1 || admit.outDir == "" {
			return errors.New("review-repair admit requires --config, --issue, --out-dir")
		}
		return runReviewRepairAdmit(cmd.Context(), admit.configPath, admit.issue, admit.outDir)
	})
	admitCmd.Flags().StringVar(&admit.configPath, "config", "", "Trusted consumer configuration")
	admitCmd.Flags().IntVar(&admit.issue, "issue", 0, "Original admitted issue number")
	admitCmd.Flags().StringVar(&admit.outDir, "out-dir", "", "Directory for bounded repair artifacts")
	root.AddCommand(admitCmd)
	var execute struct {
		configPath, manifestPath, workspace, out string
	}
	executeCmd := newStageCommand("execute", "Produce a bounded repair candidate", "review-repair execute requires --config, --manifest, --workspace, --out", func(cmd *cobra.Command, _ []string) error {
		if execute.configPath == "" || execute.manifestPath == "" || execute.workspace == "" || execute.out == "" {
			return errors.New("review-repair execute requires --config, --manifest, --workspace, --out")
		}
		return runReviewRepairExecute(cmd.Context(), execute.configPath, execute.manifestPath, execute.workspace, execute.out)
	})
	executeCmd.Flags().StringVar(&execute.configPath, "config", "", "Trusted consumer configuration")
	executeCmd.Flags().StringVar(&execute.manifestPath, "manifest", "", "Admitted repair manifest")
	executeCmd.Flags().StringVar(&execute.workspace, "workspace", "", "Isolated candidate checkout")
	executeCmd.Flags().StringVar(&execute.out, "out", "", "Candidate bundle output")
	root.AddCommand(executeCmd)
	var verify struct {
		configPath, manifestPath, workspace, bundlePath, out string
	}
	verifyCmd := newStageCommand("verify", "Run secretless checks against the exact repair", "review-repair verify requires --config, --manifest, --workspace, --bundle, --out", func(cmd *cobra.Command, _ []string) error {
		if verify.configPath == "" || verify.manifestPath == "" || verify.workspace == "" || verify.bundlePath == "" || verify.out == "" {
			return errors.New("review-repair verify requires --config, --manifest, --workspace, --bundle, --out")
		}
		return runReviewRepairVerify(cmd.Context(), verify.configPath, verify.manifestPath, verify.workspace, verify.bundlePath, verify.out)
	})
	verifyCmd.Flags().StringVar(&verify.configPath, "config", "", "Trusted consumer configuration")
	verifyCmd.Flags().StringVar(&verify.manifestPath, "manifest", "", "Admitted repair manifest")
	verifyCmd.Flags().StringVar(&verify.workspace, "workspace", "", "Clean verification checkout")
	verifyCmd.Flags().StringVar(&verify.bundlePath, "bundle", "", "Untrusted candidate bundle")
	verifyCmd.Flags().StringVar(&verify.out, "out", "", "Trusted check evidence output")
	root.AddCommand(verifyCmd)
	var publish struct {
		configPath, manifestPath, bundlePath, evidencePath, baseBranch string
	}
	publishCmd := newStageCommand("publish", "Update the same PR with a verified repair", "review-repair publish requires --config, --manifest, --bundle, --evidence, --base-branch", func(cmd *cobra.Command, _ []string) error {
		if publish.configPath == "" || publish.manifestPath == "" || publish.bundlePath == "" || publish.evidencePath == "" || publish.baseBranch == "" {
			return errors.New("review-repair publish requires --config, --manifest, --bundle, --evidence, --base-branch")
		}
		return runReviewRepairPublish(cmd.Context(), publish.configPath, publish.manifestPath, publish.bundlePath, publish.evidencePath, publish.baseBranch)
	})
	publishCmd.Flags().StringVar(&publish.configPath, "config", "", "Trusted consumer configuration")
	publishCmd.Flags().StringVar(&publish.manifestPath, "manifest", "", "Admitted repair manifest")
	publishCmd.Flags().StringVar(&publish.bundlePath, "bundle", "", "Verified candidate bundle")
	publishCmd.Flags().StringVar(&publish.evidencePath, "evidence", "", "Trusted check evidence")
	publishCmd.Flags().StringVar(&publish.baseBranch, "base-branch", "", "Original PR base branch")
	root.AddCommand(publishCmd)
	var fail struct {
		configPath, manifestPath, stage string
	}
	failCmd := newStageCommand("fail", "Record a bounded repair-stage failure", "review-repair fail requires --config, --manifest, --stage", func(cmd *cobra.Command, _ []string) error {
		if fail.configPath == "" || fail.manifestPath == "" || fail.stage == "" {
			return errors.New("review-repair fail requires --config, --manifest, --stage")
		}
		return runReviewRepairFail(cmd.Context(), fail.configPath, fail.manifestPath, fail.stage)
	})
	failCmd.Flags().StringVar(&fail.configPath, "config", "", "Trusted consumer configuration")
	failCmd.Flags().StringVar(&fail.manifestPath, "manifest", "", "Admitted repair manifest")
	failCmd.Flags().StringVar(&fail.stage, "stage", "", "Failed stage: execute, verify, or publish")
	root.AddCommand(failCmd)
	return root
}

// runReviewRepairFail validates the current Actions owner and records an execute,
// verify, or publish failure. It releases unprepared work or blocks prepared work
// for reconciliation. Identity, credential, and state errors propagate.
func runReviewRepairFail(ctx context.Context, configPath, manifestPath, stage string) error {
	if stage != "execute" && stage != "verify" && stage != "publish" {
		return errors.New("invalid bounded repair failure stage")
	}
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if os.Getenv("SOFA_PROJECTS_TOKEN") != "" || os.Getenv("SOFA_PUBLISH_TOKEN") != "" || os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("unnecessary credential present in repair failure finalizer")
	}
	m, err := readRepairManifest(manifestPath, c)
	if err != nil {
		return err
	}
	owner, err := ownerFromEnv()
	if err != nil || owner != m.Fence.Owner {
		return errors.New("repair failure manifest belongs to another Actions attempt")
	}
	client, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	engine := state.Engine{
		Store: github.StateStore{
			Client:     client,
			Repository: c.Repository,
		},
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		return err
	}
	attempt, ok := snapshot.State.Attempts[m.Fence.AttemptID]
	if !ok || attempt.Admission != m.Admission || attempt.Repair == nil || attempt.Repair.FeedbackID != m.Repair.FeedbackID || attempt.Generation != m.Fence.Generation || attempt.Owner == nil || *attempt.Owner != m.Fence.Owner {
		return errors.New("repair failure ledger identity changed")
	}
	if attempt.Phase == state.Blocked && attempt.Failure == "validation" {
		return nil
	}
	if err := engine.FailReviewRepair(ctx, m.Fence); err != nil {
		return err
	}
	return observeOnce(ctx, engine, m.Fence.AttemptID, "review-repair-failure", stage, m.Repair.PRHeadSHA, "", fmt.Sprintf("g%d", m.Fence.Generation))
}

// runReviewRepairExecute edits a clean checkout at the reviewed PR head and writes
// the candidate bundle plus execution.json beside it. Prepared candidates are rejected;
// no edits wraps errExecutionValidation. Other validation, worker, and output errors
// propagate, and checkout edits are not rolled back on failure.
func runReviewRepairExecute(ctx context.Context, configPath, manifestPath, workspace, out string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, false); err != nil {
		return err
	}
	m, err := readRepairManifest(manifestPath, c)
	if err != nil {
		return err
	}
	if m.Repair.CandidateSHA != "" || m.Repair.CandidateDigest != "" {
		return errors.New("repair candidate already prepared; execute cannot repeat it")
	}
	if err := cleanBase(ctx, workspace, m.Repair.PRHeadSHA); err != nil {
		return err
	}
	result, err := worker.Execute(ctx, worker.Input{
		Config:         c,
		CanonicalSpec:  m.CanonicalSpec,
		ReviewFeedback: m.FeedbackText,
		Directory:      workspace,
		AttemptID:      m.Fence.AttemptID,
		Generation:     uint64(m.Fence.Generation),
		BaseSHA:        m.Repair.PRHeadSHA,
		ModelToken:     os.Getenv(c.Profile.SecretEnv),
	})
	if err != nil {
		return err
	}
	if result.NoChange {
		return fmt.Errorf("%w: review repair made no change", errExecutionValidation)
	}
	if err := writeJSON(out, result.Bundle); err != nil {
		return err
	}
	return writeJSON(filepath.Join(filepath.Dir(out), "execution.json"), map[string]any{
		"version":          1,
		"used_agent":       result.UsedAgent,
		"prompt_requests":  result.PromptRequests,
		"candidate_digest": result.Bundle.CandidateDigest,
	})
}

// runReviewRepairVerify applies the candidate to a clean reviewed-head checkout,
// runs configured checks without credentials, and writes evidence to out. Failed
// checks or workspace changes wrap errExecutionValidation; input, application,
// and output errors propagate. The candidate remains applied after verification.
func runReviewRepairVerify(ctx context.Context, configPath, manifestPath, workspace, bundlePath, out string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if err := forbidPrivilegedEnv(c.Profile.SecretEnv, true); err != nil {
		return err
	}
	m, err := readRepairManifest(manifestPath, c)
	if err != nil {
		return err
	}
	b, err := readBundle(bundlePath)
	if err != nil {
		return err
	}
	if err := cleanBase(ctx, workspace, m.Repair.PRHeadSHA); err != nil {
		return err
	}
	if err := integrity.Apply(workspace, b, repairEvidenceExpected(m, b), bundlePolicy(c)); err != nil {
		return err
	}
	baseline, err := candidateState(ctx, workspace, b)
	if err != nil {
		return err
	}
	checkHome, err := os.MkdirTemp("", "sofa-review-check-home-")
	if err != nil {
		return errors.New("cannot create isolated review check home")
	}
	defer os.RemoveAll(checkHome)
	checks := make([]integrity.CheckEvidence, 0, len(c.Checks))
	for _, check := range c.Checks {
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
		err := runCheckProcess(checkCtx, workspace, checkHome, check.Argv)
		cancel()
		if err != nil {
			return fmt.Errorf("%w: repair check %s failed", errExecutionValidation, check.ID)
		}
		current, err := candidateState(ctx, workspace, b)
		if err != nil || !baseline.matches(current) {
			return fmt.Errorf("%w: repair check %s changed candidate workspace", errExecutionValidation, check.ID)
		}
		checks = append(checks, integrity.CheckEvidence{
			Version:         integrity.Version,
			Name:            check.ID,
			CandidateDigest: b.CandidateDigest,
			Passed:          true,
		})
	}
	return writeJSON(out, checks)
}

// runReviewRepairPublish revalidates approval, ownership, and the exact owner review
// before updating the existing PR through a leased push. It records the new publication
// and writes repair-publication.json in the current directory. Validation, GitHub,
// state, and file errors propagate; the remote PR may already be updated on error.
func runReviewRepairPublish(ctx context.Context, configPath, manifestPath, bundlePath, evidencePath, baseBranch string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("model credential present in review publication stage")
	}
	m, err := readRepairManifest(manifestPath, c)
	if err != nil {
		return err
	}
	owner, err := ownerFromEnv()
	if err != nil || owner != m.Fence.Owner {
		return errors.New("repair publication manifest belongs to another Actions attempt")
	}
	b, err := readBundle(bundlePath)
	if err != nil {
		return err
	}
	var evidence []integrity.CheckEvidence
	if err := readJSON(evidencePath, 1<<20, &evidence); err != nil {
		return err
	}
	required := make([]string, 0, len(c.Checks))
	for _, check := range c.Checks {
		required = append(required, check.ID)
	}
	policy := bundlePolicy(c, []byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_PUBLISH_TOKEN")))
	if err := integrity.Validate(b, repairEvidenceExpected(m, b), policy); err != nil {
		return err
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
	engine := state.Engine{
		Store: github.StateStore{
			Client:     publisher,
			Repository: c.Repository,
		},
	}
	guard := func(ctx context.Context) error {
		a, err := requireRepairOwner(ctx, engine, m)
		if err != nil {
			return err
		}
		source, err := projects.Issue(ctx, c, int(m.Admission.Issue))
		if err != nil {
			return err
		}
		if _, err := approvedRepairSource(ctx, c, projects, engine.Store, source, a); err != nil {
			return err
		}
		pull, err := publisher.Pull(ctx, c.Repository, m.Publication.PRNumber)
		if err != nil {
			return err
		}
		if pull.BaseRef != baseBranch {
			return errors.New("repair PR base branch changed")
		}
		reviews, err := publisher.PullReviews(ctx, c.Repository, pull.Number)
		if err != nil {
			return err
		}
		if err := attachRepairReviewComments(ctx, publisher, c.Repository, pull.Number, reviews, m.ReviewID); err != nil {
			return err
		}
		return revalidateRepairReview(c, m, pull, reviews, a.Repair.CandidateSHA)
	}
	if err := guard(ctx); err != nil {
		return err
	}
	a, err := requireRepairOwner(ctx, engine, m)
	if err != nil {
		return err
	}
	if a.Phase == state.Executing {
		if err := engine.Advance(ctx, m.Fence, state.Validating); err != nil {
			return err
		}
	} else if a.Phase != state.Validating && a.Phase != state.Publishing {
		return errors.New("repair candidate is not ready for publication")
	}
	prepared := a.Repair.CandidateSHA
	result, err := publisher.PublishRepair(ctx, github.RepairPublishInput{
		PublishInput: github.PublishInput{
			Bundle:         b,
			Expected:       repairEvidenceExpected(m, b),
			Policy:         policy,
			Checks:         evidence,
			RequiredChecks: required,
			BaseBranch:     baseBranch,
			Guard:          guard,
		},
		Previous:    m.Publication,
		FeedbackID:  m.Repair.FeedbackID,
		PreparedSHA: prepared,
		RecordIntent: func(ctx context.Context, commitSHA string) error {
			return engine.BeginRepairPublication(ctx, m.Fence, b.CandidateDigest, commitSHA)
		},
	})
	if err != nil {
		return err
	}
	newPublication := m.Publication
	newPublication.ExpectedHead = m.Repair.PRHeadSHA
	newPublication.CandidateDigest = b.CandidateDigest
	newPublication.HeadSHA = result.CommitSHA
	if err := engine.MarkReviewRepairPublished(ctx, m.Fence, newPublication); err != nil {
		return err
	}
	if err := observeOnce(ctx, engine, m.Fence.AttemptID, "review-repair", "draft", result.CommitSHA, result.URL, fmt.Sprintf("g%d", m.Fence.Generation)); err != nil {
		return err
	}
	return writeJSON("repair-publication.json", result)
}

// runReviewRepairAdmit reserves and claims a bounded repair for current owner
// feedback, then writes manifest.json and status.json to outDir. Ineligible new work
// and active runs produce nondispatch statuses; changed reservations and other
// admission or output failures return errors. Consumed budget is retained on failure.
func runReviewRepairAdmit(ctx context.Context, configPath string, issue int, outDir string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if c.Lifecycle == nil {
		return errors.New("review repair requires lifecycle configuration")
	}
	owner, err := ownerFromEnv()
	if err != nil {
		return err
	}
	projects, err := clientFromEnv("SOFA_PROJECTS_TOKEN")
	if err != nil {
		return err
	}
	ledger, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	store := github.StateStore{
		Client:     ledger,
		Repository: c.Repository,
	}
	engine := state.Engine{
		Store: store,
	}
	issueSnapshot, err := projects.Issue(ctx, c, issue)
	if err != nil {
		return err
	}
	snapshot, err := store.Load(ctx)
	if err != nil {
		return err
	}
	attempt, found, err := state.CurrentAttemptForIssue(snapshot.State, issueSnapshot.IssueID)
	if err != nil {
		return err
	}
	if !found || attempt.Publication == nil {
		return errors.New("review repair requires an admitted published PR")
	}
	spec, err := approvedRepairSource(ctx, c, projects, store, issueSnapshot, attempt)
	if err != nil {
		return err
	}
	pull, err := ledger.Pull(ctx, c.Repository, attempt.Publication.PRNumber)
	if err != nil {
		return err
	}
	reviews, err := ledger.PullReviews(ctx, c.Repository, pull.Number)
	if err != nil {
		return err
	}
	if i := newestOwnerReview(reviews, c.OwnerID); i >= 0 && reviews[i].State == "CHANGES_REQUESTED" && reviews[i].CommitSHA == pull.HeadSHA {
		if err := attachRepairReviewComments(ctx, ledger, c.Repository, pull.Number, reviews, reviews[i].ID); err != nil {
			return err
		}
	}
	selected, selectedReview := selectRepairReview(attempt, pull, reviews, c.OwnerID)
	if selected.FeedbackID == "" {
		if attempt.Repair != nil {
			return errors.New("reserved owner review or exact PR identity changed")
		}
		return writeRepairStatus(outDir, false, "no-current-owner-review", attempt.ID)
	}
	if err := integrity.ScanSecrets(append(append([]byte(nil), spec...), []byte(selected.FeedbackText)...), [][]byte{[]byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_STATE_TOKEN"))}); err != nil {
		return errors.New("review repair input contains sensitive material")
	}
	intent := state.RepairIntent{
		FeedbackID:   selected.FeedbackID,
		FeedbackHash: selected.FeedbackHash,
		PRBaseSHA:    selected.PRBaseSHA,
		PRHeadSHA:    selected.PRHeadSHA,
		PRNumber:     selected.PRNumber,
	}
	if attempt.Repair == nil {
		reserved, err := engine.ReserveReviewRepair(ctx, attempt.ID, intent)
		if err != nil {
			return err
		}
		if !reserved {
			return writeRepairStatus(outDir, false, "already-reserved", attempt.ID)
		}
	} else {
		if attempt.Repair.CandidateSHA != "" || attempt.Repair.CandidateDigest != "" {
			return errors.New("prepared repair candidate requires publication reconciliation")
		}
		attempt, err = recoverReservedReviewRepair(ctx, engine, ledger, c.Repository, attempt)
		if errors.Is(err, state.ErrActive) {
			return writeRepairStatus(outDir, false, "already-active", attempt.ID)
		}
		if err != nil {
			return err
		}
	}
	charge := state.Counters{
		ModelCalls:     1,
		RuntimeSeconds: int64(c.Limits.AttemptSeconds),
	}
	fence, err := engine.ClaimReviewRepair(ctx, attempt.ID, owner, charge)
	if err != nil {
		return err
	}
	// A Project move, issue edit, PR update, or dismissed review between the
	// first read and reservation must stop before the model gets a prompt.
	currentSource, err := projects.Issue(ctx, c, issue)
	if err != nil {
		return err
	}
	if _, err := approvedRepairSource(ctx, c, projects, store, currentSource, attempt); err != nil {
		// A failed GitHub read is not proof that approval was revoked. Keep it
		// recoverable; only a verified authority or revision mismatch blocks it.
		_ = engine.Fail(ctx, fence, repairSourceFailureKind(err))
		return err
	}
	currentPull, err := ledger.Pull(ctx, c.Repository, pull.Number)
	if err != nil {
		return err
	}
	currentReviews, err := ledger.PullReviews(ctx, c.Repository, pull.Number)
	if err != nil {
		return err
	}
	if err := attachRepairReviewComments(ctx, ledger, c.Repository, pull.Number, currentReviews, selectedReview.ID); err != nil {
		return err
	}
	preflightManifest := repairManifest{
		Publication:  *attempt.Publication,
		Repair:       intent,
		ReviewID:     selectedReview.ID,
		FeedbackText: selected.FeedbackText,
	}
	if err := revalidateRepairReview(c, preflightManifest, currentPull, currentReviews, ""); err != nil {
		_ = engine.FailReviewRepair(ctx, fence)
		return err
	}
	m := repairManifest{
		Version:       1,
		Admission:     attempt.Admission,
		Fence:         fence,
		Publication:   *attempt.Publication,
		Repair:        intent,
		ReviewID:      selectedReview.ID,
		CanonicalSpec: spec,
		FeedbackText:  selected.FeedbackText,
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return errors.New("cannot create repair output directory")
	}
	if err := writeJSON(filepath.Join(outDir, "manifest.json"), m); err != nil {
		return err
	}
	return writeRepairStatus(outDir, true, "repair-admitted", attempt.ID)
}

// selectRepairReview evaluates the newest noninformational owner review against
// fresh or reserved repair policy. Missing or ineligible reviews and evaluation
// errors return zero values rather than falling back to older feedback.
func selectRepairReview(attempt state.Attempt, pull github.PullSnapshot, reviews []github.PullReview, ownerID string) (review.Decision, github.PullReview) {
	i := newestOwnerReview(reviews, ownerID)
	if i < 0 {
		return review.Decision{}, github.PullReview{}
	}
	submitted := reviews[i]
	var decision review.Decision
	var err error
	if attempt.Repair != nil {
		decision, err = review.EvaluateReserved(attempt, pull, submitted, ownerID)
	} else {
		decision, err = review.Evaluate(attempt, pull, submitted, ownerID)
	}
	if err != nil {
		return review.Decision{}, github.PullReview{}
	}
	return decision, submitted
}

// newestOwnerReview returns the index of the latest submitted owner review,
// excluding COMMENTED reviews. Larger IDs break timestamp ties; no match returns -1.
func newestOwnerReview(reviews []github.PullReview, ownerID string) int {
	selected := -1
	for i, submitted := range reviews {
		// COMMENTED is informational in GitHub's review state machine. It
		// neither clears a prior change request nor grants a new one.
		if submitted.UserID != ownerID || submitted.ID < 1 || submitted.SubmittedAt.IsZero() || submitted.State == "COMMENTED" {
			continue
		}
		if selected < 0 || submitted.SubmittedAt.After(reviews[selected].SubmittedAt) || submitted.SubmittedAt.Equal(reviews[selected].SubmittedAt) && submitted.ID > reviews[selected].ID {
			selected = i
		}
	}
	return selected
}

// attachRepairReviewComments fills the selected review's Comments in the supplied
// slice. It returns request errors or an error if reviewID is absent.
func attachRepairReviewComments(ctx context.Context, client *github.Client, repository string, number int64, reviews []github.PullReview, reviewID int64) error {
	for i := range reviews {
		if reviews[i].ID != reviewID {
			continue
		}
		comments, err := client.PullReviewComments(ctx, repository, number, reviewID)
		if err != nil {
			return err
		}
		reviews[i].Comments = comments
		return nil
	}
	return errors.New("owner review unavailable")
}

type repairRunProofReader interface {
	RunProof(context.Context, string, state.Owner) (state.RunProof, error)
}

// Only GitHub's terminal proof for the exact persisted owner may release a
// stopped repair. A pending reservation needs no proof; an active run stays
// fenced, and Recover preserves the consumed counters and review identity.
func recoverReservedReviewRepair(ctx context.Context, engine state.Engine, proofReader repairRunProofReader, repository string, attempt state.Attempt) (state.Attempt, error) {
	if attempt.Repair == nil || attempt.Repair.CandidateSHA != "" || attempt.Repair.CandidateDigest != "" {
		return attempt, state.ErrInvalid
	}
	if attempt.Owner != nil {
		proof, err := proofReader.RunProof(ctx, repository, *attempt.Owner)
		if err != nil {
			return attempt, err
		}
		if err := engine.Recover(ctx, attempt.ID, proof); err != nil {
			return attempt, err
		}
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		return attempt, err
	}
	current, ok := snapshot.State.Attempts[attempt.ID]
	if !ok || current.Admission != attempt.Admission || current.Publication == nil || attempt.Publication == nil || *current.Publication != *attempt.Publication || current.Repair == nil || *current.Repair != *attempt.Repair || current.Phase != state.Pending || current.Owner != nil {
		return attempt, state.ErrAdmissionChanged
	}
	return current, nil
}

// writeRepairStatus creates outDir if needed and writes status.json with the
// dispatch decision. Directory and JSON write failures are returned.
func writeRepairStatus(outDir string, dispatch bool, reason, attemptID string) error {
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return errors.New("cannot create repair output directory")
	}
	return writeJSON(filepath.Join(outDir, "status.json"), map[string]any{
		"dispatch":   dispatch,
		"reason":     reason,
		"attempt_id": attemptID,
	})
}

// approvedRepairSource reads the exact bot-authored revision approved in
// Backlog before applying the repair's delivery-stage and admission checks.
// The issue body remains the source idea; it cannot stand in for that revision.
func approvedRepairSource(ctx context.Context, c config.Config, reader discovery.CommentReader, store state.Store, source admission.Snapshot, attempt state.Attempt) (json.RawMessage, error) {
	policy, err := discoveryPolicy(c)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", discovery.ErrAuthority, err)
	}
	approved, err := discovery.VerifyApprovedRevision(ctx, reader, store, policy, source)
	if err != nil {
		return nil, err
	}
	return validateRepairSource(c, approved, attempt)
}

// repairSourceFailureKind distinguishes a verified revocation from an
// unavailable ledger or GitHub comment. Unknown errors fail closed for this
// run, but remain eligible for bounded infrastructure recovery.
func repairSourceFailureKind(err error) string {
	if errors.Is(err, discovery.ErrAuthority) || errors.Is(err, discovery.ErrRevision) {
		return "authority"
	}
	return "infrastructure"
}

// validateRepairSource returns canonical specification bytes only for the admitted
// scope and configuration in Building, Verification, or Review. snapshot must already
// contain the approved comment body; identity, stage, or digest changes return errors.
func validateRepairSource(c config.Config, snapshot admission.Snapshot, attempt state.Attempt) (json.RawMessage, error) {
	if c.Lifecycle == nil || !snapshot.Complete || !snapshot.Open || !strings.EqualFold(snapshot.Repository, c.Repository) || snapshot.RepositoryID != c.RepositoryID || snapshot.Number != int(attempt.Admission.Issue) || snapshot.ProjectID != c.ProjectID || !snapshot.ProjectPrivate || snapshot.ProjectItemID != attempt.Admission.ProjectItemID {
		return nil, fmt.Errorf("%w: repair issue or restricted Project identity changed", discovery.ErrAuthority)
	}
	status := snapshot.CurrentStatus
	allowed := status == c.Lifecycle.Statuses["review"] || status == c.Lifecycle.Statuses["verification"] || status == c.Lifecycle.Statuses["building"]
	if !allowed {
		return nil, fmt.Errorf("%w: repair issue is outside the authorized delivery lifecycle", discovery.ErrAuthority)
	}
	configDigest, err := c.Digest()
	if err != nil {
		return nil, err
	}
	if configDigest != attempt.Admission.ConfigDigest {
		return nil, fmt.Errorf("%w: repair configuration changed since admission", discovery.ErrAuthority)
	}
	spec, digest, err := admission.CanonicalSpec(snapshot.Title, snapshot.Body)
	if err != nil || digest != attempt.Admission.SpecDigest {
		return nil, fmt.Errorf("%w: approved repair specification changed", discovery.ErrRevision)
	}
	return spec, nil
}

// repairEvidenceExpected binds candidate validation to the repair's reviewed PR
// head and ownership generation, using the supplied bundle's candidate digest.
func repairEvidenceExpected(m repairManifest, b integrity.Bundle) integrity.Expected {
	return integrity.Expected{
		Repository:      m.Admission.Repository,
		AttemptID:       m.Fence.AttemptID,
		Generation:      uint64(m.Fence.Generation),
		BaseSHA:         m.Repair.PRHeadSHA,
		CandidateDigest: b.CandidateDigest,
	}
}

// revalidateRepairReview rejects changes to PR identity, base, or reserved owner
// feedback. preparedSHA permits the already prepared child head during publication
// recovery; the review must still target the original head. Feedback errors propagate.
func revalidateRepairReview(c config.Config, m repairManifest, pull github.PullSnapshot, reviews []github.PullReview, preparedSHA string) error {
	if pull.Number != m.Publication.PRNumber || pull.URL != m.Publication.PRURL || pull.HeadRef != m.Publication.Branch || pull.BaseSHA != m.Repair.PRBaseSHA || pull.State != "open" || pull.Merged || !strings.EqualFold(pull.HeadRepository, c.Repository) || !strings.EqualFold(pull.BaseRepository, c.Repository) || (pull.HeadSHA != m.Repair.PRHeadSHA && pull.HeadSHA != preparedSHA) {
		return errors.New("repair PR changed since owner feedback")
	}
	i := newestOwnerReview(reviews, c.OwnerID)
	if i < 0 || reviews[i].ID != m.ReviewID {
		return errors.New("owner review superseded since repair admission")
	}
	submitted := reviews[i]
	feedback, err := review.FeedbackText(submitted)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(feedback))
	if submitted.State != "CHANGES_REQUESTED" || submitted.CommitSHA != m.Repair.PRHeadSHA || hex.EncodeToString(h[:]) != m.Repair.FeedbackHash || feedback != m.FeedbackText {
		return errors.New("owner review changed since repair admission")
	}
	return nil
}

// requireRepairOwner checks the fence and returns the stored attempt only when its
// admission, publication, and reserved feedback match the manifest. Ownership and
// store errors propagate; mismatched ledger identity wraps state.ErrInvalid.
func requireRepairOwner(ctx context.Context, engine state.Engine, m repairManifest) (state.Attempt, error) {
	if err := engine.AssertOwner(ctx, m.Fence); err != nil {
		return state.Attempt{}, err
	}
	snapshot, err := engine.Store.Load(ctx)
	if err != nil {
		return state.Attempt{}, err
	}
	a, ok := snapshot.State.Attempts[m.Fence.AttemptID]
	if !ok || a.Admission != m.Admission || a.Publication == nil || *a.Publication != m.Publication || a.Repair == nil || a.Repair.FeedbackID != m.Repair.FeedbackID || a.Repair.FeedbackHash != m.Repair.FeedbackHash || a.Repair.PRHeadSHA != m.Repair.PRHeadSHA || a.Repair.PRBaseSHA != m.Repair.PRBaseSHA || a.Repair.PRNumber != m.Repair.PRNumber {
		return state.Attempt{}, fmt.Errorf("%w: repair ledger identity changed", state.ErrInvalid)
	}
	return a, nil
}
