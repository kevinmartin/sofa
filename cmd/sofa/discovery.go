package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

// discoveryManifest is produced only after a live private-Project admission.
// It is transport data, never authority: the publisher checks its fence and
// re-reads the issue, Project revision, and trusted ledger before any comment.
type discoveryManifest struct {
	Version      int                      `json:"version"`
	Repository   string                   `json:"repository"`
	ConfigDigest string                   `json:"config_digest"`
	Source       admission.Snapshot       `json:"source"`
	Admission    state.DiscoveryAdmission `json:"admission"`
	Fence        state.DiscoveryFence     `json:"fence"`
	Facts        string                   `json:"facts"`
	TimeoutSecs  int                      `json:"timeout_seconds"`
	Recovery     *state.Owner             `json:"recovery_source,omitempty"`
}

type discoveryStatus struct {
	Dispatch        bool   `json:"dispatch"`
	Reason          string `json:"reason,omitempty"`
	RecoveryRunID   string `json:"recovery_run_id,omitempty"`
	RecoveryAttempt int    `json:"recovery_run_attempt,omitempty"`
}

func newDiscoveryCommand() *cobra.Command {
	root := &cobra.Command{Use: "discovery", Short: "Research an admitted idea and present an unapproved specification"}
	var admit struct {
		config, workspace, outDir string
		issue                     int
	}
	admitCmd := newStageCommand("admit", "Reserve one bounded Discovery prompt", "discovery admit requires --config, --issue, --workspace, --out-dir", func(cmd *cobra.Command, _ []string) error {
		if admit.config == "" || admit.issue < 1 || admit.workspace == "" || admit.outDir == "" {
			return errors.New("discovery admit requires --config, --issue, --workspace, --out-dir")
		}
		return runDiscoveryAdmit(cmd.Context(), admit.config, admit.issue, admit.workspace, admit.outDir)
	})
	admitCmd.Flags().StringVar(&admit.config, "config", "", "Trusted consumer configuration")
	admitCmd.Flags().IntVar(&admit.issue, "issue", 0, "Project issue number")
	admitCmd.Flags().StringVar(&admit.workspace, "workspace", "", "Consumer checkout at observed default-branch SHA")
	admitCmd.Flags().StringVar(&admit.outDir, "out-dir", "", "Bounded admission artifact directory")
	root.AddCommand(admitCmd)
	var generate struct{ manifest, out string }
	generateCmd := newStageCommand("generate", "Fill a versioned specification in an isolated worker", "discovery generate requires --manifest and --out", func(cmd *cobra.Command, _ []string) error {
		if generate.manifest == "" || generate.out == "" {
			return errors.New("discovery generate requires --manifest and --out")
		}
		return runDiscoveryGenerate(cmd.Context(), generate.manifest, generate.out)
	})
	generateCmd.Flags().StringVar(&generate.manifest, "manifest", "", "Admitted Discovery manifest")
	generateCmd.Flags().StringVar(&generate.out, "out", "", "Candidate specification artifact")
	root.AddCommand(generateCmd)
	var publish struct{ config, manifest, candidate string }
	publishCmd := newStageCommand("publish", "Revalidate and present an unapproved specification", "discovery publish requires --config, --manifest, --candidate", func(cmd *cobra.Command, _ []string) error {
		if publish.config == "" || publish.manifest == "" || publish.candidate == "" {
			return errors.New("discovery publish requires --config, --manifest, --candidate")
		}
		return runDiscoveryPublish(cmd.Context(), publish.config, publish.manifest, publish.candidate)
	})
	publishCmd.Flags().StringVar(&publish.config, "config", "", "Trusted consumer configuration")
	publishCmd.Flags().StringVar(&publish.manifest, "manifest", "", "Admitted Discovery manifest")
	publishCmd.Flags().StringVar(&publish.candidate, "candidate", "", "Untrusted specification artifact")
	root.AddCommand(publishCmd)
	var fail struct{ config, manifest, kind string }
	failCmd := newStageCommand("fail", "Record a bounded Discovery failure", "discovery fail requires --config, --manifest, --kind", func(cmd *cobra.Command, _ []string) error {
		if fail.config == "" || fail.manifest == "" || fail.kind == "" {
			return errors.New("discovery fail requires --config, --manifest, --kind")
		}
		return runDiscoveryFail(cmd.Context(), fail.config, fail.manifest, fail.kind)
	})
	failCmd.Flags().StringVar(&fail.config, "config", "", "Trusted consumer configuration")
	failCmd.Flags().StringVar(&fail.manifest, "manifest", "", "Admitted Discovery manifest")
	failCmd.Flags().StringVar(&fail.kind, "kind", "", "Bounded failure category")
	root.AddCommand(failCmd)
	return root
}

func readDiscoveryManifest(path string, c *config.Config) (discoveryManifest, error) {
	var m discoveryManifest
	if err := readJSON(path, 192<<10, &m); err != nil {
		return m, err
	}
	if m.Version != 1 || m.Fence.IssueID == "" || m.Fence.IssueID != m.Source.IssueID || m.Fence.Generation < 1 || m.Fence.Owner.RunID == "" || m.Fence.Owner.RunAttempt < 1 || m.Source.Number < 1 || !m.Source.Complete || !m.Source.Open || m.Source.IssueID != m.Admission.IssueID || m.Source.ProjectID != m.Admission.ProjectID || m.Source.ProjectItemID != m.Admission.ProjectItemID || m.Source.StatusOptionID != m.Admission.StatusOptionID || !m.Source.StatusUpdatedAt.Equal(m.Admission.StatusUpdatedAt) || m.Source.Number != int(m.Admission.Issue) || !strings.EqualFold(m.Repository, m.Admission.Repository) || len(m.Facts) > 64<<10 || len(m.Source.Title) > 1024 || len(m.Source.Body) > 64<<10 || m.TimeoutSecs < 1 || m.TimeoutSecs > 900 || m.Recovery != nil && (m.Recovery.RunID == "" || m.Recovery.RunAttempt < 1) {
		return discoveryManifest{}, errors.New("Discovery manifest identity invalid")
	}
	if c != nil {
		digest, err := c.Digest()
		if err != nil || c.Lifecycle == nil || m.ConfigDigest != digest || !strings.EqualFold(m.Repository, c.Repository) || !strings.EqualFold(m.Source.Repository, c.Repository) || m.Source.RepositoryID != c.RepositoryID || m.Source.ProjectID != c.ProjectID || !m.Source.ProjectPrivate || m.Source.CurrentStatus != c.Lifecycle.Statuses["discovery"] || m.TimeoutSecs != min(c.Limits.AttemptSeconds, 900) {
			return discoveryManifest{}, errors.New("Discovery manifest configuration changed")
		}
	}
	return m, nil
}

func discoveryClients(c config.Config) (*github.Client, github.StateStore, error) {
	projects, err := clientFromEnv("SOFA_PROJECTS_TOKEN")
	if err != nil {
		return nil, github.StateStore{}, err
	}
	ledgerClient, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return nil, github.StateStore{}, err
	}
	return projects, github.StateStore{Client: ledgerClient, Repository: c.Repository}, nil
}

func currentCheckoutSHA(ctx context.Context, root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("Discovery workspace must be absolute")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	b, err := cmd.Output()
	if err != nil || len(b) != 41 {
		return "", errors.New("consumer checkout revision unavailable")
	}
	return strings.TrimSpace(string(b)), nil
}

// Only exact title/body matches are deterministic duplicate candidates.
// Semantic similarity is a hypothesis for the research agent, not authority
// to merge or suppress another issue.
func relatedDiscoveryIssues(source admission.Snapshot, items []github.ProjectWorkItem) ([]int64, error) {
	matchTitle := strings.TrimSpace(source.Title)
	matchBody := strings.TrimSpace(source.Body)
	related := make([]int64, 0)
	for _, item := range items {
		other := item.Issue
		if other.IssueID == source.IssueID || !other.Open || !strings.EqualFold(other.Repository, source.Repository) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(other.Title), matchTitle) || matchBody != "" && strings.TrimSpace(other.Body) == matchBody {
			related = append(related, int64(other.Number))
			if len(related) > 100 {
				return nil, errors.New("exact duplicate issue candidates exceed bound")
			}
		}
	}
	return related, nil
}

func runDiscoveryAdmit(ctx context.Context, configPath string, issue int, workspace, outDir string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if c.Lifecycle == nil || c.Lifecycle.SpecAuthorID == "" {
		return errors.New("trusted Discovery publisher identity unavailable")
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		return err
	}
	owner, err := ownerFromEnv()
	if err != nil {
		return err
	}
	projects, store, err := discoveryClients(c)
	if err != nil {
		return err
	}
	base, err := projects.Issue(ctx, c, issue)
	if err != nil {
		return err
	}
	admitted, _, err := discovery.AuthorizeDiscovery(policy, base)
	if err != nil {
		return err
	}
	if err := integrity.ScanSecrets([]byte(base.Title+"\n"+base.Body), [][]byte{
		[]byte(os.Getenv("SOFA_PROJECTS_TOKEN")),
		[]byte(os.Getenv("SOFA_STATE_TOKEN")),
	}); err != nil {
		return errors.New("Discovery idea contains sensitive material")
	}
	checkout, err := currentCheckoutSHA(ctx, workspace)
	if err != nil || checkout != base.BaseSHA {
		return errors.New("consumer checkout changed during Discovery admission")
	}
	items, err := projects.ProjectWorkItems(ctx, c)
	if err != nil {
		return err
	}
	related, err := relatedDiscoveryIssues(base, items)
	if err != nil {
		return err
	}
	facts, err := discovery.GatherFacts(workspace, related)
	if err != nil {
		return err
	}
	digest, err := c.Digest()
	if err != nil {
		return err
	}
	// Fact enumeration may take time in a large repository. Re-read the issue
	// and its Project option before reserving a model call; later publication
	// repeats this check because GitHub cannot lock a Project item for a run.
	current, err := projects.Issue(ctx, c, issue)
	if err != nil {
		return err
	}
	currentAdmission, _, err := discovery.AuthorizeDiscovery(policy, current)
	if err != nil || currentAdmission != admitted || current.BaseSHA != checkout {
		return discovery.ErrRevision
	}
	engine := state.Engine{Store: store}
	task, _, err := engine.AdmitDiscovery(ctx, admitted, c.Lifecycle.EffectiveDiscoveryWIP(), int64(min(c.Limits.MaxAgentTurns, 20)))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0700); err != nil {
		return errors.New("cannot create Discovery output")
	}
	statusPath := filepath.Join(outDir, "status.json")
	if task.Phase == state.DiscoveryReview || task.Phase == state.DiscoveryBlocked || task.Phase == state.DiscoveryCanceled {
		return writeJSON(statusPath, discoveryStatus{Reason: string(task.Phase)})
	}
	var recovery *state.Owner
	if task.Owner != nil {
		if *task.Owner == owner {
			return writeJSON(statusPath, discoveryStatus{Reason: "already-active"})
		}
		proof, err := store.Client.RunProof(ctx, c.Repository, *task.Owner)
		if err != nil {
			return err
		}
		if err := engine.RecoverDiscovery(ctx, task.IssueID, proof); err != nil {
			return err
		}
		old := *task.Owner
		recovery = &old
		loaded, err := store.Load(ctx)
		if err != nil {
			return err
		}
		task = loaded.State.Discoveries[task.IssueID]
		if task.Phase != state.DiscoveryPending {
			return writeJSON(statusPath, discoveryStatus{Reason: string(task.Phase)})
		}
	}
	// A persisted publication intent can only resume from the exact prior
	// worker artifact. Its producer remains durable even if a recovering admit
	// run was interrupted before writing a replacement manifest.
	if task.Publication != nil {
		if recovery != nil && *recovery != task.Publication.Producer {
			return errors.New("Discovery publication producer changed")
		}
		producer := task.Publication.Producer
		recovery = &producer
	}
	fence, err := engine.ClaimDiscovery(ctx, task.IssueID, owner)
	if err != nil {
		return err
	}
	m := discoveryManifest{
		Version:      1,
		Repository:   c.Repository,
		ConfigDigest: digest,
		Source:       base,
		Admission:    admitted,
		Fence:        fence,
		Facts:        facts,
		TimeoutSecs:  min(c.Limits.AttemptSeconds, 900),
	}
	if task.Publication != nil {
		m.Recovery = recovery
	}
	if err := writeJSON(filepath.Join(outDir, "manifest.json"), m); err != nil {
		return err
	}
	status := discoveryStatus{Dispatch: true}
	if m.Recovery != nil {
		status.RecoveryRunID = m.Recovery.RunID
		status.RecoveryAttempt = m.Recovery.RunAttempt
	}
	return writeJSON(statusPath, status)
}

func runDiscoveryGenerate(ctx context.Context, manifestPath, out string) error {
	if err := forbidPrivilegedEnv("SOFA_MODEL_TOKEN", false); err != nil {
		return err
	}
	m, err := readDiscoveryManifest(manifestPath, nil)
	if err != nil {
		return err
	}
	if m.Recovery != nil {
		return errors.New("Discovery recovery must reuse retained candidate")
	}
	result, err := discovery.Generate(ctx, discovery.GenerateInput{
		Title:      m.Source.Title,
		Idea:       m.Source.Body,
		Facts:      m.Facts,
		ModelToken: os.Getenv("SOFA_MODEL_TOKEN"),
		Timeout:    time.Duration(m.TimeoutSecs) * time.Second,
	})
	if err != nil {
		return err
	}
	if _, err := discovery.Parse(result.Body); err != nil {
		return errors.New("invalid generated specification")
	}
	return writeJSON(out, struct {
		Version int    `json:"version"`
		Body    string `json:"body"`
	}{Version: 1, Body: result.Body})
}

func runDiscoveryPublish(ctx context.Context, configPath, manifestPath, candidatePath string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	if c.Lifecycle == nil || c.Lifecycle.SpecAuthorID == "" {
		return errors.New("trusted Discovery publisher identity unavailable")
	}
	m, err := readDiscoveryManifest(manifestPath, &c)
	if err != nil {
		return err
	}
	var candidate struct {
		Version int    `json:"version"`
		Body    string `json:"body"`
	}
	if err := readJSON(candidatePath, 70<<10, &candidate); err != nil {
		return err
	}
	if candidate.Version != 1 {
		return errors.New("invalid Discovery candidate version")
	}
	projects, store, err := discoveryClients(c)
	if err != nil {
		return err
	}
	commenter, err := clientFromEnv("SOFA_COMMENT_TOKEN")
	if err != nil {
		return err
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		return err
	}
	if m.Recovery != nil {
		ledger, err := store.Load(ctx)
		if err != nil {
			return err
		}
		task, ok := ledger.State.Discoveries[m.Source.IssueID]
		if !ok || task.Publication == nil || task.Publication.Producer != *m.Recovery {
			return errors.New("Discovery recovery producer unavailable")
		}
	}
	// Ensure the manifest itself still describes the currently admitted source.
	guard := func(ctx context.Context) (admission.Snapshot, error) {
		current, err := projects.Issue(ctx, c, m.Source.Number)
		if err != nil {
			return admission.Snapshot{}, err
		}
		admissionNow, _, err := discovery.AuthorizeDiscovery(policy, current)
		if err != nil || admissionNow != m.Admission {
			return admission.Snapshot{}, discovery.ErrRevision
		}
		return current, nil
	}
	_, err = discovery.PublishSpecification(ctx, state.Engine{Store: store}, store, commenter, discovery.PublishInput{
		Policy: policy, IssueID: m.Source.IssueID, Fence: m.Fence, DraftBody: candidate.Body,
		ExpectedAuthorID: c.Lifecycle.SpecAuthorID,
		ForbiddenValues:  [][]byte{[]byte(os.Getenv("SOFA_PROJECTS_TOKEN")), []byte(os.Getenv("SOFA_STATE_TOKEN")), []byte(os.Getenv("SOFA_COMMENT_TOKEN"))},
		Guard:            guard,
	})
	return err
}

func runDiscoveryFail(ctx context.Context, configPath, manifestPath, kind string) error {
	c, err := readConfig(configPath)
	if err != nil {
		return err
	}
	m, err := readDiscoveryManifest(manifestPath, &c)
	if err != nil {
		return err
	}
	ledgerClient, err := clientFromEnv("SOFA_STATE_TOKEN")
	if err != nil {
		return err
	}
	store := github.StateStore{Client: ledgerClient, Repository: c.Repository}
	return (state.Engine{Store: store}).FailDiscovery(ctx, m.Fence, kind)
}
