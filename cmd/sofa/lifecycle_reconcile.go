package main

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/discovery"
	"github.com/kevinmartin/sofa/internal/github"
	"github.com/kevinmartin/sofa/internal/lifecycle"
	"github.com/kevinmartin/sofa/internal/release"
	"github.com/kevinmartin/sofa/internal/review"
	"github.com/kevinmartin/sofa/internal/state"
)

type lifecycleReconcileOptions struct {
	configPath string
	outPath    string
	wakeID     string
}

type lifecycleReconcileResult struct {
	Claimed     bool  `json:"claimed"`
	Generation  int64 `json:"generation,omitempty"`
	MissedTicks int64 `json:"missed_ticks,omitempty"`
	Discovery   []int `json:"discovery_issue_numbers"`
	Ready       []int `json:"ready_issue_numbers"`
	Held        []int `json:"held_issue_numbers"`
	Moved       []int `json:"moved_issue_numbers"`
}

// newLifecycleCommand exposes reconciliation with a stable optional event wake ID.
func newLifecycleCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lifecycle",
		Short: "Reconcile the trusted Project lifecycle",
	}
	var opts lifecycleReconcileOptions
	reconcile := newStageCommand("reconcile", "Observe and project due lifecycle work", "lifecycle reconcile requires --config and --out", func(cmd *cobra.Command, _ []string) error {
		if opts.configPath == "" || opts.outPath == "" {
			return errors.New("lifecycle reconcile requires --config and --out")
		}
		return runLifecycleReconcile(cmd.Context(), opts)
	})
	reconcile.Flags().StringVar(&opts.configPath, "config", "", "Trusted consumer configuration")
	reconcile.Flags().StringVar(&opts.outPath, "out", "", "Bounded JSON result path")
	reconcile.Flags().StringVar(&opts.wakeID, "wake-id", "", "Stable manual or event revision; empty for scheduled tick")
	cmd.AddCommand(reconcile)
	return cmd
}

// lifecycleStatuses copies the configured display-name mapping; Lifecycle must be nonnil.
func lifecycleStatuses(c config.Config) lifecycle.Statuses {
	result := make(lifecycle.Statuses, len(c.Lifecycle.Statuses))
	for name, display := range c.Lifecycle.Statuses {
		result[lifecycle.Stage(name)] = display
	}
	return result
}

// runLifecycleReconcile claims a due poll, records approvals and release evidence,
// and applies justified Project moves before writing the dispatch plan to opts.outPath.
// Item-specific failures become held issue numbers; configuration, poll, scan, shared
// read, and output failures return errors. A failed scan does not undo its poll claim
// or earlier writes.
func runLifecycleReconcile(ctx context.Context, opts lifecycleReconcileOptions) error {
	c, err := readConfig(opts.configPath)
	if err != nil {
		return err
	}
	if c.Lifecycle == nil {
		return errors.New("lifecycle configuration is unavailable")
	}
	statuses := lifecycleStatuses(c)
	if err := statuses.Validate(); err != nil {
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
	engine := state.Engine{
		Store: store,
	}
	claim, err := engine.ClaimPoll(ctx, time.Now().UTC(), time.Duration(c.Lifecycle.EffectivePollMinutes())*time.Minute, opts.wakeID)
	if err != nil {
		return err
	}
	result := lifecycleReconcileResult{
		Claimed:     claim.Claimed,
		Generation:  claim.Generation,
		MissedTicks: claim.Missed,
		Discovery:   []int{},
		Ready:       []int{},
		Held:        []int{},
		Moved:       []int{},
	}
	if !claim.Claimed {
		return writeJSON(opts.outPath, result)
	}
	items, err := projects.ProjectWorkItems(ctx, c)
	if err != nil {
		return err
	}
	_, options, err := projects.ProjectStatusField(ctx, c.ProjectID)
	if err != nil {
		return err
	}
	for _, stage := range lifecycle.Stages {
		if options[statuses[stage]] == "" {
			return errors.New("configured Project Status option unavailable")
		}
	}
	policy, err := discoveryPolicy(c)
	if err != nil {
		return err
	}
	issues := make([]admission.Snapshot, 0, len(items))
	byID := make(map[string]admission.Snapshot, len(items))
	for _, item := range items {
		issues = append(issues, item.Issue)
		byID[item.Issue.IssueID] = item.Issue
	}
	// Approval observations are deterministic and scoped to one item. One
	// invalid candidate must not stop unrelated Project items in the same scan.
	for _, item := range items {
		stage, known := statuses.StageFor(item.Issue.CurrentStatus)
		if !known {
			continue
		}
		switch stage {
		case lifecycle.SpecReview:
			if _, _, err := discovery.ObserveSpecReview(ctx, projects, store, policy, item.Issue); err != nil {
				result.Held = append(result.Held, item.Issue.Number)
			}
		case lifecycle.Backlog:
			if _, _, err := discovery.ObserveBacklog(ctx, projects, store, policy, item.Issue); err != nil {
				result.Held = append(result.Held, item.Issue.Number)
			}
		}
	}
	ledger, err := store.Load(ctx)
	if err != nil {
		return err
	}
	// Reconcile terminal Discovery owners even when their workflow could not
	// finalize. Only the exact Actions attempt can release its durable claim.
	recovered, recoveryHeld := recoverStoppedDiscoveries(ctx, ledgerClient, engine, c, items, ledger.State, statuses)
	result.Held = append(result.Held, recoveryHeld...)
	if recovered {
		ledger, err = store.Load(ctx)
		if err != nil {
			return err
		}
	}
	// A Discovery presenter may have published and ledgered its exact spec
	// comment before an Actions interruption prevented the Project move. Only
	// that durable task plus a re-read matching comment can resume the move.
	// Skip these items in this scan because the initial Project list is stale
	// after a successful write (or a conflicting human edit).
	recovering := make(map[string]bool)
	for _, item := range items {
		stage, known := statuses.StageFor(item.Issue.CurrentStatus)
		if !known || stage != lifecycle.Discovery {
			continue
		}
		task, exists := ledger.State.Discoveries[item.Issue.IssueID]
		if !exists || task.Phase != state.DiscoveryReview {
			continue
		}
		recovering[item.Issue.IssueID] = true
		if err := recoverDiscoveryReview(ctx, projects, engine, c, policy, statuses, item.Issue, task); err != nil {
			result.Held = append(result.Held, item.Issue.Number)
		} else {
			result.Moved = append(result.Moved, item.Issue.Number)
		}
	}
	if len(recovering) != 0 {
		ledger, err = store.Load(ctx)
		if err != nil {
			return err
		}
	}
	authority := make(map[string]bool, len(items))
	evidence := make(map[string]lifecycle.DeliveryEvidence)
	// Historical Done items need correction patrols, but scanning every merge,
	// check, comment and recent commit on every wake grows without bound. The
	// durable poll generation rotates a fixed slice; active stages are never
	// excluded by this budget.
	donePatrol := doneCorrectionPatrol(items, statuses, claim.Generation, 2)
	for _, item := range items {
		issue := item.Issue
		stage, known := statuses.StageFor(issue.CurrentStatus)
		if !known {
			continue
		}
		if stage == lifecycle.Done && !donePatrol[issue.IssueID] {
			continue
		}
		if stage != lifecycle.Ready && stage != lifecycle.Building && stage != lifecycle.Verification && stage != lifecycle.Review && stage != lifecycle.Release && stage != lifecycle.Done {
			continue
		}
		// A Ready item without a recorded specification cannot be approved.
		// Avoid a fresh remote ledger read for every historical Project item;
		// candidates with a record still get full live revision validation.
		if _, recorded := ledger.State.Specs[issue.IssueID]; !recorded {
			result.Held = append(result.Held, issue.Number)
			continue
		}
		if stage == lifecycle.Ready {
			_, err = discovery.ApprovedSnapshot(ctx, projects, store, policy, issue)
		} else {
			_, err = discovery.VerifyApprovedRevision(ctx, projects, store, policy, issue)
		}
		if err != nil {
			result.Held = append(result.Held, issue.Number)
			continue
		}
		authority[issue.IssueID] = true
		attempt, found, lookupErr := attemptForItem(ledger.State, c.Repository, issue)
		if lookupErr != nil {
			result.Held = append(result.Held, issue.Number)
			continue
		}
		if !found || attempt.Publication == nil || attempt.Publication.PRNumber < 1 {
			continue
		}
		pull, err := projects.Pull(ctx, c.Repository, attempt.Publication.PRNumber)
		if err != nil {
			result.Held = append(result.Held, issue.Number)
			continue
		}
		observed := lifecycle.DeliveryEvidence{
			PRNumber:  pull.Number,
			PRURL:     pull.URL,
			HeadSHA:   pull.HeadSHA,
			BaseSHA:   pull.BaseSHA,
			Closed:    pull.State == "closed",
			Merged:    pull.Merged,
			MergedSHA: pull.MergeCommitSHA,
			// Milestone 04 supplies the independent required-gate plan and
			// evidence. Until then, an empty plan must remain blocked.
			RequiredGates: []string{},
		}
		if pull.Merged {
			observed.ReleaseRequired = true
			if err := observeRelease(ctx, projects, engine, c, attempt, pull, issue.BaseSHA, &observed); err != nil {
				result.Held = append(result.Held, issue.Number)
			} else if stage == lifecycle.Done && observed.ReleasePassed {
				// Completion corrections are observations only. A failed comment or
				// commit read holds this item without rewriting its prior Done event.
				if err := observeCompletionCorrections(ctx, projects, engine, attempt, pull, issue.BaseSHA); err != nil {
					result.Held = append(result.Held, issue.Number)
				}
			}
		}
		evidence[issue.IssueID] = observed
	}
	scanIssues := make([]admission.Snapshot, 0, len(issues))
	for _, issue := range issues {
		if !recovering[issue.IssueID] {
			scanIssues = append(scanIssues, issue)
		}
	}
	effects, err := lifecycle.Scan(lifecycle.ScanInput{
		Repository: c.Repository,
		ProjectID:  c.ProjectID,
		Statuses:   statuses,
		Issues:     scanIssues,
		Ledger:     ledger.State,
		Authority:  authority,
		Evidence:   evidence,
	})
	if err != nil {
		return err
	}
	for _, effect := range effects {
		item, found := byID[effect.IssueID]
		if !found {
			return errors.New("project item disappeared during scan")
		}
		switch effect.Kind {
		case lifecycle.Observe:
			if err := engine.ObserveBoard(ctx, boardFromSnapshot(c, effect.From, item)); err != nil {
				result.Held = append(result.Held, effect.IssueNumber)
			}
		case lifecycle.Move:
			if err := applyBoardMove(ctx, projects, engine, c, statuses, effect, item); err != nil {
				result.Held = append(result.Held, effect.IssueNumber)
			} else {
				result.Moved = append(result.Moved, effect.IssueNumber)
			}
		case lifecycle.Hold:
			if effect.BlockedReason != "" {
				result.Held = append(result.Held, effect.IssueNumber)
			}
		}
	}
	result.Discovery = readyForDiscovery(c, items, ledger.State, policy, statuses, result.Held)
	result.Ready = readyForDelivery(c, items, ledger.State, authority, statuses, result.Held)
	result.Held = uniqueInts(result.Held)
	result.Moved = uniqueInts(result.Moved)
	return writeJSON(opts.outPath, result)
}

// doneCorrectionPatrol selects a stable, round-robin slice so every Done
// issue is revisited across successive claimed polls without a new cursor.
func doneCorrectionPatrol(items []github.ProjectWorkItem, statuses lifecycle.Statuses, generation int64, limit int) map[string]bool {
	selected := make(map[string]bool)
	if generation < 1 || limit < 1 {
		return selected
	}
	done := make([]admission.Snapshot, 0)
	for _, item := range items {
		if stage, known := statuses.StageFor(item.Issue.CurrentStatus); known && stage == lifecycle.Done {
			done = append(done, item.Issue)
		}
	}
	if len(done) == 0 {
		return selected
	}
	sort.Slice(done, func(i, j int) bool { return done[i].Number < done[j].Number })
	start := int(((generation - 1) % int64(len(done))) * int64(limit) % int64(len(done)))
	for i := range min(limit, len(done)) {
		selected[done[(start+i)%len(done)].IssueID] = true
	}
	return selected
}

// recoverDiscoveryReview verifies the durable published comment and resumes the
// Discovery-to-Spec Review move. Comment reads, revision checks, ledger writes, and
// Project move errors are returned.
func recoverDiscoveryReview(ctx context.Context, client *github.Client, engine state.Engine, c config.Config, policy discovery.Policy, statuses lifecycle.Statuses, issue admission.Snapshot, task state.DiscoveryTask) error {
	comment, err := client.IssueComment(ctx, c.Repository, int64(issue.Number), task.CommentID)
	if err != nil {
		return err
	}
	if err := discovery.ReviewCommentReadyForMove(policy, issue, comment, task); err != nil {
		return err
	}
	if err := engine.ObserveBoard(ctx, boardFromSnapshot(c, lifecycle.Discovery, issue)); err != nil {
		return err
	}
	effect := lifecycle.Effect{
		Kind:        lifecycle.Move,
		IssueID:     issue.IssueID,
		IssueNumber: issue.Number,
		From:        lifecycle.Discovery,
		To:          lifecycle.SpecReview,
	}
	return applyBoardMove(ctx, client, engine, c, statuses, effect, issue)
}

// attemptForItem returns the current approved attempt bound to the Project item.
// No attempt returns false without error; ambiguous, mismatched, or unmatched
// unsuperseded attempts return an error.
func attemptForItem(ledger state.State, repository string, issue admission.Snapshot) (state.Attempt, bool, error) {
	selected, found, err := state.CurrentAttemptForIssue(ledger, issue.IssueID)
	if err != nil {
		return state.Attempt{}, false, err
	}
	if found {
		if !strings.EqualFold(selected.Admission.Repository, repository) || selected.Admission.ProjectID != issue.ProjectID || selected.Admission.ProjectItemID != issue.ProjectItemID || selected.Admission.Issue != int64(issue.Number) {
			return state.Attempt{}, false, errors.New("current attempt Project identity changed")
		}
		return selected, true, nil
	}
	for _, attempt := range ledger.Attempts {
		if !attempt.SupersededAt.IsZero() || attempt.Admission.ProjectItemID != issue.ProjectItemID {
			continue
		}
		return state.Attempt{}, false, errors.New("unmatched current Project item attempt")
	}
	return state.Attempt{}, false, nil
}

// boardFromSnapshot captures the issue's exact status revision under the supplied policy and stage.
func boardFromSnapshot(c config.Config, stage lifecycle.Stage, item admission.Snapshot) state.BoardProjection {
	return state.BoardProjection{
		Repository:    c.Repository,
		IssueID:       item.IssueID,
		ProjectID:     c.ProjectID,
		ProjectItemID: item.ProjectItemID,
		Stage:         string(stage),
		OptionID:      item.StatusOptionID,
		UpdatedAt:     item.StatusUpdatedAt,
	}
}

// uniqueInts sorts and deduplicates values in place, returning a slice sharing its storage.
func uniqueInts(values []int) []int {
	sort.Ints(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// observeRelease reads merge ancestry and configured checks, records the release
// outcome, and updates observed after recording succeeds. Blocked or failed release
// outcomes are evidence, not errors; retrieval, evaluation, and ledger errors propagate.
func observeRelease(ctx context.Context, client *github.Client, engine state.Engine, c config.Config, attempt state.Attempt, pull github.PullSnapshot, defaultHead string, observed *lifecycle.DeliveryEvidence) error {
	onDefault, err := client.IsAncestor(ctx, c.Repository, pull.MergeCommitSHA, defaultHead)
	if err != nil {
		return err
	}
	checks, err := client.CommitChecks(ctx, c.Repository, pull.MergeCommitSHA)
	if err != nil {
		return err
	}
	required := make([]release.RequiredCheck, 0, len(c.Lifecycle.Release.RequiredChecks))
	for _, check := range c.Lifecycle.Release.RequiredChecks {
		required = append(required, release.RequiredCheck{
			Name:  check.Name,
			AppID: check.AppID,
		})
	}
	decision, err := release.Evaluate(attempt, pull, defaultHead, onDefault, required, checks)
	if err != nil {
		return err
	}
	if err := release.Record(ctx, engine, decision, time.Now().UTC()); err != nil {
		return err
	}
	observed.ReleaseRequired = true
	observed.ReleasePassed = decision.Kind == "done"
	observed.ReleaseSHA = decision.CommitSHA
	return nil
}

type completionReader interface {
	IssueComments(context.Context, string, int64) ([]discovery.SpecComment, error)
	RecentDefaultCommits(context.Context, string, string) ([]github.DefaultCommit, error)
	release.RevertVerifier
}

// observeCompletionCorrections links later reports and strict inverse commits
// to one already completed PR. Commit messages only narrow the bounded search;
// they are never sufficient evidence for a revert event.
func observeCompletionCorrections(ctx context.Context, reader completionReader, engine state.Engine, attempt state.Attempt, pull github.PullSnapshot, defaultHead string) error {
	if attempt.Publication == nil || pull.Number != attempt.Publication.PRNumber || pull.URL != attempt.Publication.PRURL || pull.HeadSHA != attempt.Publication.HeadSHA || pull.HeadRef != attempt.Publication.Branch || !strings.EqualFold(pull.HeadRepository, attempt.Admission.Repository) || !strings.EqualFold(pull.BaseRepository, attempt.Admission.Repository) || !pull.Merged || pull.State != "closed" || pull.MergedAt.IsZero() || pull.MergeCommitSHA == "" {
		return errors.New("completed PR identity changed")
	}
	comments, err := reader.IssueComments(ctx, attempt.Admission.Repository, pull.Number)
	if err != nil {
		return err
	}
	for _, comment := range comments {
		if comment.CreatedAt.Before(pull.MergedAt) {
			continue
		}
		if err := review.RecordLaterFeedback(ctx, engine, attempt, pull, comment); err != nil {
			return err
		}
	}
	commits, err := reader.RecentDefaultCommits(ctx, attempt.Admission.Repository, defaultHead)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		if !revertMessageReferences(commit.Message, pull.MergeCommitSHA) {
			continue
		}
		if err := release.ObserveRevert(ctx, reader, engine, attempt, pull, commit.SHA, defaultHead, time.Now().UTC()); err != nil {
			if errors.Is(err, release.ErrUnverifiedRevert) {
				continue
			}
			return err
		}
	}
	return nil
}

// revertMessageReferences finds a case-insensitive standard revert-message prefix
// for mergedSHA. The text is only a search hint, not proof of an inverse commit.
func revertMessageReferences(message, mergedSHA string) bool {
	return strings.Contains(strings.ToLower(message), "this reverts commit "+strings.ToLower(mergedSHA))
}

type discoveryRunProofReader interface {
	RunProof(context.Context, string, state.Owner) (state.RunProof, error)
}

// Recover only terminal claims for current Discovery items. API failures hold
// their own item; a live owner never yields a second prompt or a WIP slot.
func recoverStoppedDiscoveries(ctx context.Context, reader discoveryRunProofReader, engine state.Engine, c config.Config, items []github.ProjectWorkItem, ledger state.State, statuses lifecycle.Statuses) (bool, []int) {
	recovered := false
	held := make([]int, 0)
	for _, item := range items {
		issue := item.Issue
		stage, known := statuses.StageFor(issue.CurrentStatus)
		if !known || stage != lifecycle.Discovery {
			continue
		}
		task, exists := ledger.Discoveries[issue.IssueID]
		if !exists || task.Phase != state.DiscoveryRunning || task.Owner == nil {
			continue
		}
		if !strings.EqualFold(task.Repository, c.Repository) || task.Issue != int64(issue.Number) || task.ProjectID != c.ProjectID || task.ProjectItemID != issue.ProjectItemID {
			held = append(held, issue.Number)
			continue
		}
		proof, err := reader.RunProof(ctx, c.Repository, *task.Owner)
		if err != nil {
			held = append(held, issue.Number)
			continue
		}
		if err := engine.RecoverDiscovery(ctx, issue.IssueID, proof); err == nil {
			recovered = true
		} else if !errors.Is(err, state.ErrActive) {
			held = append(held, issue.Number)
		}
	}
	return recovered, held
}

// readyForDiscovery queues only owner-admitted Project items with an available
// WIP slot. A pending reservation is retried before new work; a running or
// already presented task never creates a second model prompt on a poll.
func readyForDiscovery(c config.Config, items []github.ProjectWorkItem, ledger state.State, policy discovery.Policy, statuses lifecycle.Statuses, held []int) []int {
	if c.Lifecycle == nil {
		return nil
	}
	cap := int64(min(c.Limits.MaxAgentTurns, 20))
	wip := c.Lifecycle.EffectiveDiscoveryWIP()
	active := 0
	for _, task := range ledger.Discoveries {
		if strings.EqualFold(task.Repository, c.Repository) && (task.Phase == state.DiscoveryPending || task.Phase == state.DiscoveryRunning) {
			active++
		}
	}
	heldNumbers := make(map[int]bool, len(held))
	for _, number := range held {
		heldNumbers[number] = true
	}
	pending := make([]int, 0)
	fresh := make([]int, 0)
	for _, item := range items {
		issue := item.Issue
		stage, known := statuses.StageFor(issue.CurrentStatus)
		if !known || stage != lifecycle.Discovery || heldNumbers[issue.Number] {
			continue
		}
		admitted, _, err := discovery.AuthorizeDiscovery(policy, issue)
		if err != nil {
			continue
		}
		task, exists := ledger.Discoveries[issue.IssueID]
		if !exists {
			fresh = append(fresh, issue.Number)
			continue
		}
		if !strings.EqualFold(task.Repository, admitted.Repository) || task.Issue != admitted.Issue || task.ProjectID != admitted.ProjectID || task.ProjectItemID != admitted.ProjectItemID {
			continue
		}
		sameSource := task.SourceDigest == admitted.SourceDigest && task.StatusOptionID == admitted.StatusOptionID && task.StatusUpdatedAt.Equal(admitted.StatusUpdatedAt)
		if sameSource {
			if task.Phase == state.DiscoveryPending && task.Owner == nil && (task.Publication != nil || task.ModelCalls < cap) && task.MaxModelCalls == cap {
				pending = append(pending, issue.Number)
			} else if task.Phase == state.DiscoveryBlocked && task.Failure == "budget" && cap > task.MaxModelCalls {
				fresh = append(fresh, issue.Number)
			}
			continue
		}
		if !admitted.StatusUpdatedAt.After(task.StatusUpdatedAt) || task.Publication != nil || cap < task.MaxModelCalls || cap <= task.ModelCalls {
			continue
		}
		if task.Phase == state.DiscoveryPending && task.Owner == nil {
			pending = append(pending, issue.Number)
			continue
		}
		record, hasRecord := ledger.Specs[issue.IssueID]
		approvedCurrent := task.Phase == state.DiscoveryReview && hasRecord && record.ApprovedDigest == task.SpecDigest && record.Revision == task.Revision
		if approvedCurrent && task.SourceDigest != admitted.SourceDigest && admitted.StatusUpdatedAt.After(record.BacklogUpdatedAt) ||
			task.Phase == state.DiscoveryReview && !approvedCurrent || task.Phase == state.DiscoveryBlocked {
			fresh = append(fresh, issue.Number)
		}
	}
	sort.Ints(pending)
	sort.Ints(fresh)
	result := make([]int, 0, wip)
	for _, number := range pending {
		if len(result) >= wip || len(result) >= 20 {
			break
		}
		result = append(result, number)
	}
	freshSlots := max(wip-active, 0)
	for _, number := range fresh {
		if len(result) >= 20 || freshSlots == 0 {
			break
		}
		result = append(result, number)
		freshSlots--
	}
	return result
}

// readyForDelivery selects authorized Ready issues with known metadata and Done
// dependencies. Pending recoveries precede new work; each group is ordered by
// priority then issue number within available WIP. Invalid or ambiguous candidates
// are skipped. Lifecycle must be configured; this function does not reserve slots.
func readyForDelivery(c config.Config, items []github.ProjectWorkItem, ledger state.State, authority map[string]bool, statuses lifecycle.Statuses, held []int) []int {
	type candidate struct {
		number int
		rank   int
	}
	newWork := make([]candidate, 0)
	recovery := make([]candidate, 0)
	heldIssues := make(map[int]bool, len(held))
	for _, number := range held {
		heldIssues[number] = true
	}
	stageByNumber := make(map[int]lifecycle.Stage, len(items))
	for _, item := range items {
		stage, ok := statuses.StageFor(item.Issue.CurrentStatus)
		if ok {
			stageByNumber[item.Issue.Number] = stage
		}
	}
	occupied := 0
	pending := make(map[string]bool)
	for _, attempt := range ledger.Attempts {
		if !attempt.SupersededAt.IsZero() || !strings.EqualFold(attempt.Admission.Repository, c.Repository) || attempt.Admission.ProjectID != c.ProjectID || attempt.Phase == state.Draft || attempt.Phase == state.Blocked || attempt.Phase == state.Deferred {
			continue
		}
		occupied++
		if attempt.Phase == state.Pending && attempt.Owner == nil {
			pending[attempt.Admission.ProjectItemID] = true
		}
	}
	for _, item := range items {
		stage, ok := statuses.StageFor(item.Issue.CurrentStatus)
		if !ok || stage != lifecycle.Ready || !authority[item.Issue.IssueID] || heldIssues[item.Issue.Number] || item.MetadataError != "" || !item.DependenciesKnown || !item.PriorityKnown {
			continue
		}
		if dependenciesReady, _, err := lifecycle.DependenciesReady(item.Dependencies, stageByNumber); err != nil || !dependenciesReady {
			continue
		}
		attempt, found, lookupErr := attemptForItem(ledger, c.Repository, item.Issue)
		if lookupErr != nil {
			continue
		}
		if found && (attempt.Phase == state.Draft || attempt.Phase == state.Blocked || attempt.Phase == state.Deferred) {
			continue
		}
		if found && attempt.Phase == state.Pending && attempt.Owner == nil && pending[item.Issue.ProjectItemID] {
			recovery = append(recovery, candidate{
				number: item.Issue.Number,
				rank:   item.PriorityRank,
			})
			continue
		}
		if found {
			continue
		}
		newWork = append(newWork, candidate{
			number: item.Issue.Number,
			rank:   item.PriorityRank,
		})
	}
	// A configured owner field accepts P0..P4, with smaller rank first.
	// Without that field all ranks are equal and issue number is the stable tie.
	order := func(values []candidate) {
		sort.Slice(values, func(i, j int) bool {
			if values[i].rank != values[j].rank {
				return values[i].rank < values[j].rank
			}
			return values[i].number < values[j].number
		})
	}
	order(recovery)
	order(newWork)
	limit := c.Lifecycle.EffectiveDeliveryWIP()
	// Pending work already owns a slot. Retry it before admitting new work,
	// but never dispatch more recoveries than slots not held by active owners.
	recoveryCapacity := max(0, limit-(occupied-len(pending)))
	selected := make([]int, 0, limit)
	for _, item := range recovery[:min(len(recovery), recoveryCapacity)] {
		selected = append(selected, item.number)
	}
	newCapacity := max(0, limit-occupied)
	for _, item := range newWork[:min(len(newWork), newCapacity)] {
		selected = append(selected, item.number)
	}
	return selected
}

// applyBoardMove persists or verifies the exact move intent, updates the Project,
// and records a fresh observation. Read, write, and revision errors are returned;
// the pending intent remains recoverable if the remote write or acknowledgement fails.
func applyBoardMove(ctx context.Context, client *github.Client, engine state.Engine, c config.Config, statuses lifecycle.Statuses, effect lifecycle.Effect, item admission.Snapshot) error {
	_, options, err := client.ProjectStatusField(ctx, c.ProjectID)
	if err != nil {
		return err
	}
	targetOptionID := options[statuses[effect.To]]
	if targetOptionID == "" {
		return errors.New("target Project status option unavailable")
	}
	if effect.RetryIntent {
		pending, found, err := engine.PendingBoardMove(ctx, item.IssueID)
		if err != nil {
			return err
		}
		if !found || pending.PendingStage != string(effect.To) || pending.PendingOptionID != targetOptionID || pending.PendingFromOptionID != item.StatusOptionID || !pending.PendingFromUpdatedAt.Equal(item.StatusUpdatedAt) {
			return errors.New("pending Project transition changed")
		}
	} else {
		if err := engine.BeginBoardMove(ctx, boardFromSnapshot(c, effect.From, item), string(effect.To), targetOptionID); err != nil {
			return err
		}
	}
	if err := client.SetProjectStatusIfCurrent(ctx, item.IssueID, c.ProjectID, item.ProjectItemID, item.StatusOptionID, item.StatusUpdatedAt, statuses, effect.To); err != nil {
		return err
	}
	current, err := client.Issue(ctx, c, item.Number)
	if err != nil {
		return err
	}
	if current.ProjectItemID != item.ProjectItemID || current.CurrentStatus != statuses[effect.To] || current.StatusOptionID == item.StatusOptionID || !current.StatusUpdatedAt.After(item.StatusUpdatedAt) {
		return errors.New("project move outcome not yet observable")
	}
	return engine.ObserveBoard(ctx, boardFromSnapshot(c, effect.To, current))
}
