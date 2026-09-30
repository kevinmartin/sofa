package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

type publishOptions struct {
	configPath   string
	manifestPath string
	bundlePath   string
	evidencePath string
	baseBranch   string
}

func newPublishCommand() *cobra.Command {
	var opts publishOptions
	cmd := newStageCommand("publish", "Publish a verified candidate as a draft pull request", "publish requires --config, --manifest, --bundle, --evidence, --base-branch", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.manifestPath == "" || opts.bundlePath == "" || opts.evidencePath == "" || opts.baseBranch == "" {
			return errors.New("publish requires --config, --manifest, --bundle, --evidence, --base-branch")
		}
		return runPublish(cmd.Context(), opts)
	})
	cmd.Flags().StringVar(&opts.configPath, "config", "", "Path to the consumer configuration")
	cmd.Flags().StringVar(&opts.manifestPath, "manifest", "", "Path to the admitted manifest")
	cmd.Flags().StringVar(&opts.bundlePath, "bundle", "", "Path to the candidate bundle")
	cmd.Flags().StringVar(&opts.evidencePath, "evidence", "", "Path to verification evidence")
	cmd.Flags().StringVar(&opts.baseBranch, "base-branch", "", "Base branch for the draft pull request")
	return cmd
}

func runPublish(ctx context.Context, opts publishOptions) error {
	c, err := readConfig(opts.configPath)
	if err != nil {
		return err
	}
	if os.Getenv(c.Profile.SecretEnv) != "" {
		return errors.New("model credential present in publication stage")
	}
	m, err := readManifest(opts.manifestPath, c)
	if err != nil {
		return err
	}
	currentOwner, err := ownerFromEnv()
	if err != nil || currentOwner != m.Fence.Owner {
		return errors.New("publication manifest does not belong to this Actions attempt")
	}
	b, err := readBundle(opts.bundlePath)
	if err != nil {
		return err
	}
	var evidence []integrity.CheckEvidence
	if err := readJSON(opts.evidencePath, 1<<20, &evidence); err != nil {
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
	store := github.StateStore{
		Client:     publisher,
		Repository: c.Repository,
	}
	engine := state.Engine{
		Store: store,
	}
	guard := func(ctx context.Context) error {
		if err := engine.AssertOwner(ctx, m.Fence); err != nil {
			return err
		}
		snapshot, err := projects.Issue(ctx, c, m.Grant.IssueNumber)
		if err != nil {
			return err
		}
		if c.Lifecycle != nil {
			policy, err := discoveryPolicy(c)
			if err != nil {
				return err
			}
			snapshot, err = discovery.ApprovedSnapshot(ctx, projects, store, policy, snapshot)
			if err != nil {
				return err
			}
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
	if err := observeCandidate(ctx, engine, m.Fence, b.CandidateDigest); err != nil {
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
		checkpoint := state.Checkpoint{
			Version:      state.Version,
			Phase:        state.Validating,
			ArtifactID:   fmt.Sprintf("sofa-verified-candidate-%s-%d", m.Fence.Owner.RunID, m.Fence.Owner.RunAttempt),
			Digest:       b.CandidateDigest,
			CandidateSHA: b.CandidateDigest,
			Producer:     m.Fence.Owner,
			Generation:   m.Fence.Generation,
			AcceptedAt:   now,
			ExpiresAt:    now.Add(24 * time.Hour),
		}
		if err := engine.SaveCheckpoint(ctx, m.Fence, checkpoint); err != nil {
			return err
		}
	}
	intent := state.Publication{
		Branch:          "sofa/" + b.AttemptID[:24],
		ExpectedHead:    b.BaseSHA,
		CandidateDigest: b.CandidateDigest,
	}
	if err := engine.BeginPublication(ctx, m.Fence, intent); err != nil {
		return err
	}
	var spec struct{ Title, Body string }
	if err := json.Unmarshal(m.CanonicalSpec, &spec); err != nil {
		return errors.New("invalid publication specification")
	}
	result, err := publisher.PublishDraft(ctx, github.PublishInput{
		Bundle:         b,
		Expected:       expected,
		Policy:         policy,
		Checks:         evidence,
		RequiredChecks: required,
		BaseBranch:     opts.baseBranch,
		Title:          spec.Title,
		Body:           fmt.Sprintf("Draft for approved issue #%d.\n\nCandidate %s", m.Grant.IssueNumber, b.CandidateDigest),
		Guard:          guard,
	})
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
