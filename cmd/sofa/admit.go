package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

type admitOptions struct {
	configPath string
	issue      int
	outDir     string
}

func newAdmitCommand() *cobra.Command {
	var opts admitOptions
	cmd := newStageCommand("admit", "Authorize an approved issue and claim its attempt", "admit requires --config, --issue, --out-dir", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.issue < 1 || opts.outDir == "" {
			return errors.New("admit requires --config, --issue, --out-dir")
		}
		return runAdmit(cmd.Context(), opts)
	})
	cmd.Flags().StringVar(&opts.configPath, "config", "", "Path to the consumer configuration")
	cmd.Flags().IntVar(&opts.issue, "issue", 0, "Approved issue number")
	cmd.Flags().StringVar(&opts.outDir, "out-dir", "", "Directory for admission artifacts")
	return cmd
}

// runAdmit validates live approval, persists admission, and claims or recovers
// bounded delivery work. It writes status.json and, for dispatched work, manifest.json
// in opts.outDir. Active, completed, or blocked work produces a nondispatch status;
// run-proof read failures also leave owned work active. Other validation, state,
// GitHub, and file errors propagate and may follow a durable reservation.
func runAdmit(ctx context.Context, opts admitOptions) error {
	c, err := readConfig(opts.configPath)
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
	store := github.StateStore{
		Client:     ledgerClient,
		Repository: c.Repository,
	}
	snapshot, err := projects.Issue(ctx, c, opts.issue)
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
	grant, spec, err := admission.Authorize(c, snapshot)
	if err != nil {
		return err
	}
	if err := integrity.ScanSecrets(spec, [][]byte{[]byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_STATE_TOKEN"))}); err != nil {
		return errors.New("approved specification contains sensitive material")
	}
	engine := state.Engine{
		Store: store,
	}
	maxAttempts := int64(c.Limits.InfraRetries + c.Limits.RepairAttempts + 1)
	limits := state.Limits{
		ModelCalls:            int64(c.Limits.MaxAgentTurns) * maxAttempts,
		Repairs:               int64(c.Limits.RepairAttempts),
		InfrastructureRetries: int64(c.Limits.InfraRetries),
		RuntimeSeconds:        int64(c.Limits.AttemptSeconds) * maxAttempts,
	}
	legacyAttempts := int64(c.Limits.InfraRetries + 1)
	legacyLimits := limits
	legacyLimits.ModelCalls = int64(c.Limits.MaxAgentTurns) * legacyAttempts
	legacyLimits.RuntimeSeconds = int64(c.Limits.AttemptSeconds) * legacyAttempts
	attempt, _, err := admitWithLegacyLimits(ctx, engine, ledgerAdmission(grant), limits, legacyLimits)
	if err != nil {
		return err
	}
	if err := observeOnce(ctx, engine, attempt.ID, "admission", "accepted", grant.BaseSHA, "", ""); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.outDir, 0700); err != nil {
		return errors.New("cannot create admission output")
	}
	status := filepath.Join(opts.outDir, "status.json")
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
		charge := state.Counters{
			RuntimeSeconds: int64(c.Limits.AttemptSeconds),
		}
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
	manifest := Manifest{
		Version:       1,
		Grant:         grant,
		Fence:         fence,
		CanonicalSpec: spec,
	}
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
	if err := writeJSON(filepath.Join(opts.outDir, "manifest.json"), manifest); err != nil {
		return err
	}
	return writeJSON(status, map[string]any{"dispatch": true, "attempt_id": attempt.ID, "reconcile_candidate": manifest.RecoverySource != nil, "reconcile_publication": manifest.Recovery != nil, "recovery_run_id": func() string {
		if manifest.RecoverySource != nil {
			return manifest.RecoverySource.RunID
		}
		return ""
	}()})
}

// Existing attempts keep the limits persisted by the previous toolkit formula.
// Only the exact same admission may replay them; new attempts use current limits.
func admitWithLegacyLimits(ctx context.Context, engine state.Engine, admission state.Admission, current, legacy state.Limits) (state.Attempt, bool, error) {
	attempt, created, err := engine.Admit(ctx, admission, current)
	if !errors.Is(err, state.ErrAdmissionChanged) || current == legacy || attempt.ID != state.AttemptID(admission) || attempt.Admission != admission || attempt.Limits != legacy || !attempt.SupersededAt.IsZero() {
		return attempt, created, err
	}
	return engine.Admit(ctx, admission, legacy)
}
