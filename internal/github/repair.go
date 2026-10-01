package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

// PullReviewComment is one inline comment belonging to an exact submitted
// review. Its path and body are untrusted feedback, not authority.
type PullReviewComment struct {
	ID           int64
	ReviewID     int64
	UserID       string
	CommitSHA    string
	Path         string
	OriginalLine *int
	Body         string
}

// PullReviewComments reads only the comments attached to the selected review.
// Replies to existing threads are skipped. Request errors, invalid comment
// identity, content over the aggregate 64 KiB bound, and a history without a
// short page within 20 pages of 100 return errors rather than partial feedback.
func (c *Client) PullReviewComments(ctx context.Context, repository string, number, reviewID int64) ([]PullReviewComment, error) {
	if !lifecycleRepositoryPattern.MatchString(repository) || number < 1 || reviewID < 1 {
		return nil, errors.New("invalid pull request review identity")
	}
	var comments []PullReviewComment
	var size int
	for page := 1; page <= 20; page++ {
		var response []struct {
			ID           int64  `json:"id"`
			ReviewID     int64  `json:"pull_request_review_id"`
			InReplyToID  int64  `json:"in_reply_to_id"`
			CommitSHA    string `json:"original_commit_id"`
			Path         string `json:"path"`
			OriginalLine *int   `json:"original_line"`
			Body         string `json:"body"`
			User         struct {
				NodeID string `json:"node_id"`
			} `json:"user"`
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d/comments?per_page=100&page=%d", repository, number, reviewID, page)
		if err := c.Request(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, err
		}
		for _, item := range response {
			if item.ID < 1 || item.ReviewID != reviewID {
				return nil, errors.New("pull request review comment identity unavailable")
			}
			// Replies to a thread are later conversation, not the original
			// owner review's submitted inline findings.
			if item.InReplyToID > 0 {
				continue
			}
			if item.User.NodeID == "" || len(item.Body) > 64<<10 {
				return nil, errors.New("pull request review comment identity unavailable")
			}
			size += len(item.Body) + len(item.Path) + 128
			if size > 64<<10 {
				return nil, errors.New("pull request review comments exceed bound")
			}
			comments = append(comments, PullReviewComment{
				ID:           item.ID,
				ReviewID:     item.ReviewID,
				UserID:       item.User.NodeID,
				CommitSHA:    item.CommitSHA,
				Path:         item.Path,
				OriginalLine: item.OriginalLine,
				Body:         item.Body,
			})
		}
		if len(response) < 100 {
			return comments, nil
		}
	}
	return nil, errors.New("pull request review comments exceed bound")
}

// RepairPublishInput updates one already published PR. RecordIntent must
// persist the exact deterministic child commit before the remote branch move.
// Guard must revalidate the owner review, issue authority, ledger fence and
// current PR; it runs immediately before the leased push.
type RepairPublishInput struct {
	PublishInput
	Previous     state.Publication
	FeedbackID   string
	PreparedSHA  string // prior CAS intent, for push-before-acknowledgement replay
	RecordIntent func(context.Context, string) error
}

// PublishRepair is idempotent across a lost response after push: the candidate
// commit is deterministic and a replay accepts only that same child commit.
// Git's explicit old-head lease, rather than GitHub's non-CAS REST updateRef,
// prevents a changed branch from being overwritten.
func (c *Client) PublishRepair(ctx context.Context, in RepairPublishInput) (DraftPR, error) {
	var empty DraftPR
	b := in.Bundle
	p := in.Previous
	if in.Guard == nil || in.RecordIntent == nil || !strings.HasPrefix(p.Branch, "sofa/") || p.PRNumber < 1 || p.HeadSHA != b.BaseSHA || p.CandidateDigest == b.CandidateDigest || in.FeedbackID == "" || strings.ContainsAny(in.FeedbackID, "\r\n\x00 ") {
		return empty, errors.New("invalid repair publication intent")
	}
	if err := integrity.Validate(b, in.Expected, in.Policy); err != nil {
		return empty, err
	}
	if err := integrity.ValidateEvidence(b, in.Checks, in.RequiredChecks); err != nil {
		return empty, err
	}
	if err := in.Guard(ctx); err != nil {
		return empty, err
	}
	pr, err := c.Pull(ctx, b.Repository, p.PRNumber)
	if err != nil {
		return empty, err
	}
	if !repairPullIdentity(pr, b.Repository, p, in.BaseBranch) || (pr.HeadSHA != p.HeadSHA && pr.HeadSHA != in.PreparedSHA) {
		return empty, errors.New("review repair PR identity changed")
	}
	if err := c.verifyBase(ctx, b); err != nil {
		return empty, err
	}
	marker := fmt.Sprintf("sofa-repair=%s; feedback=%s; generation=%d; candidate=%s", b.AttemptID, in.FeedbackID, b.Generation, b.CandidateDigest)
	commitSHA, push, cleanup, err := c.prepareRepairCommit(ctx, b, p.Branch, marker)
	if err != nil {
		return empty, err
	}
	defer cleanup()
	if in.PreparedSHA != "" && in.PreparedSHA != commitSHA {
		return empty, errors.New("recovered repair candidate commit changed")
	}
	if err := in.RecordIntent(ctx, commitSHA); err != nil {
		return empty, err
	}
	if err := in.Guard(ctx); err != nil {
		return empty, err
	}
	pr, err = c.Pull(ctx, b.Repository, p.PRNumber)
	if err != nil {
		return empty, err
	}
	if pr.HeadSHA == commitSHA {
		if err := c.verifyExisting(ctx, b, commitSHA, marker); err != nil {
			return empty, err
		}
	} else {
		if !repairPullMatches(pr, b.Repository, p, in.BaseBranch) {
			return empty, errors.New("review repair PR changed before push")
		}
		if err := push(); err != nil {
			return empty, err
		}
	}
	pr, err = c.Pull(ctx, b.Repository, p.PRNumber)
	if err != nil {
		return empty, err
	}
	if pr.Number != p.PRNumber || pr.URL != p.PRURL || pr.HeadSHA != commitSHA || pr.HeadRef != p.Branch || pr.BaseRef != in.BaseBranch || pr.State != "open" || pr.Merged || !strings.EqualFold(pr.HeadRepository, b.Repository) || !strings.EqualFold(pr.BaseRepository, b.Repository) {
		return empty, errors.New("review repair PR changed after push")
	}
	return DraftPR{
		Number:    pr.Number,
		URL:       pr.URL,
		Branch:    p.Branch,
		CommitSHA: commitSHA,
	}, nil
}

// repairPullMatches requires the same open PR identity and published head.
func repairPullMatches(pr PullSnapshot, repository string, p state.Publication, baseBranch string) bool {
	return repairPullIdentity(pr, repository, p, baseBranch) && pr.HeadSHA == p.HeadSHA
}

// repairPullIdentity checks the open, unmerged PR, branch, base branch, and
// repository identities without comparing commit SHAs.
func repairPullIdentity(pr PullSnapshot, repository string, p state.Publication, baseBranch string) bool {
	return pr.Number == p.PRNumber && pr.URL == p.PRURL && pr.HeadRef == p.Branch && pr.BaseRef == baseBranch && pr.State == "open" && !pr.Merged && strings.EqualFold(pr.HeadRepository, repository) && strings.EqualFold(pr.BaseRepository, repository)
}

// prepareRepairCommit prepares a deterministic child commit against the
// GitHub repository, returning its SHA, a leased push callback, and cleanup.
// The caller must call cleanup after successful preparation. On failure the
// helper attempts to remove its temporary directory before returning the error.
func (c *Client) prepareRepairCommit(ctx context.Context, b integrity.Bundle, branch, marker string) (string, func() error, func(), error) {
	return c.prepareRepairCommitAtURL(ctx, b, branch, marker, "https://github.com/"+b.Repository+".git")
}

// prepareRepairCommitAtURL fetches branch from repositoryURL and prepares a
// deterministic child of b.BaseSHA in a temporary bare repository. On success it
// returns the SHA, a push callback requiring the remote head to equal b.BaseSHA,
// and cleanup, which the caller must invoke after using the callback.
// Git preparation and push each have a two-minute timeout; identity, filesystem,
// and Git failures return bounded errors.
func (c *Client) prepareRepairCommitAtURL(ctx context.Context, b integrity.Bundle, branch, marker, repositoryURL string) (string, func() error, func(), error) {
	if !c.Authenticated() || !safeBranch(branch) || !strings.HasPrefix(branch, "sofa/") || !lifecycleSHAPattern.MatchString(b.BaseSHA) {
		return "", nil, nil, errors.New("invalid repair Git identity")
	}
	dir, err := os.MkdirTemp("", "sofa-repair-git-")
	if err != nil {
		return "", nil, nil, errors.New("cannot create repair Git directory")
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	askpass := filepath.Join(dir, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n  *Username*) printf '%s\\n' x-access-token ;;\n  *Password*) printf '%s\\n' \"$SOFA_GIT_PUSH_TOKEN\" ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(askpass, []byte(script), 0700); err != nil {
		cleanup()
		return "", nil, nil, errors.New("cannot prepare repair Git credential helper")
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=" + askpass,
		"SOFA_GIT_PUSH_TOKEN=" + c.token,
		"GIT_DIR=" + filepath.Join(dir, "repo.git"),
		"GIT_INDEX_FILE=" + filepath.Join(dir, "index"),
		"GIT_AUTHOR_NAME=sofa",
		"GIT_AUTHOR_EMAIL=sofa@users.noreply.github.com",
		"GIT_COMMITTER_NAME=sofa",
		"GIT_COMMITTER_EMAIL=sofa@users.noreply.github.com",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00+0000",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00+0000",
	}
	gitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := runRepairGit(gitCtx, dir, env, nil, "init", "--bare", filepath.Join(dir, "repo.git")); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	if _, err := runRepairGit(gitCtx, dir, env, nil, "fetch", "--quiet", "--no-tags", "--depth=2", repositoryURL, "refs/heads/"+branch); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	current, err := runRepairGit(gitCtx, dir, env, nil, "rev-parse", "FETCH_HEAD")
	if err != nil || !lifecycleSHAPattern.MatchString(current) {
		cleanup()
		return "", nil, nil, errors.New("repair branch head unavailable")
	}
	// First publication sees the old head. A replay after a lost response may
	// see our deterministic child; the old parent must still be available.
	if current != b.BaseSHA {
		parent, err := runRepairGit(gitCtx, dir, env, nil, "rev-parse", current+"^")
		if err != nil || parent != b.BaseSHA {
			cleanup()
			return "", nil, nil, errors.New("repair branch changed before publication")
		}
	}
	if _, err := runRepairGit(gitCtx, dir, env, nil, "read-tree", b.BaseSHA); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	var index bytes.Buffer
	for _, file := range b.Files {
		if file.Operation == "delete" {
			fmt.Fprintf(&index, "0 %s\t%s\n", strings.Repeat("0", 40), file.Path)
			continue
		}
		blob, err := runRepairGit(gitCtx, dir, env, bytes.NewReader(file.Content), "hash-object", "-w", "--stdin")
		if err != nil || blob != blobSHA(file.Content) {
			cleanup()
			return "", nil, nil, errors.New("repair candidate blob mismatch")
		}
		fmt.Fprintf(&index, "%s %s\t%s\n", integrity.RegularMode, blob, file.Path)
	}
	if _, err := runRepairGit(gitCtx, dir, env, &index, "update-index", "--index-info"); err != nil {
		cleanup()
		return "", nil, nil, err
	}
	tree, err := runRepairGit(gitCtx, dir, env, nil, "write-tree")
	if err != nil || !lifecycleSHAPattern.MatchString(tree) {
		cleanup()
		return "", nil, nil, errors.New("repair candidate tree unavailable")
	}
	commit, err := runRepairGit(gitCtx, dir, env, nil, "commit-tree", tree, "-p", b.BaseSHA, "-m", marker)
	if err != nil || !lifecycleSHAPattern.MatchString(commit) {
		cleanup()
		return "", nil, nil, errors.New("repair candidate commit unavailable")
	}
	push := func() error {
		pushCtx, pushCancel := context.WithTimeout(ctx, 2*time.Minute)
		defer pushCancel()
		lease := "--force-with-lease=refs/heads/" + branch + ":" + b.BaseSHA
		_, err := runRepairGit(pushCtx, dir, env, nil, "push", "--quiet", lease, repositoryURL, commit+":refs/heads/"+branch)
		if err != nil {
			return errors.New("repair branch lease rejected or Git push unavailable")
		}
		return nil
	}
	return commit, push, cleanup, nil
}

// runRepairGit runs a Git subcommand with the supplied environment and input,
// returning trimmed stdout. args must contain a subcommand. Process failures
// return a bounded error without stderr or the underlying process error; output
// over 1024 bytes is rejected.
func runRepairGit(ctx context.Context, dir string, env []string, stdin io.Reader, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stderr = io.Discard
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("repair Git operation %s failed", args[0])
	}
	if out.Len() > 1024 {
		return "", errors.New("repair Git output exceeds bound")
	}
	return strings.TrimSpace(out.String()), nil
}
