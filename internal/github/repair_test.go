package github

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

func TestRepairPushUsesExactOldHeadLeaseAndDeterministicCommit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	git(dir, "init", "--bare", remote)
	git(work, "init")
	git(work, "config", "user.name", "Fixture")
	git(work, "config", "user.email", "fixture@example.invalid")
	git(work, "config", "commit.gpgsign", "false")
	git(work, "checkout", "-b", "sofa/task")
	if err := os.Mkdir(filepath.Join(work, "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "fixture", "a.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "fixture/a.txt")
	git(work, "commit", "-m", "initial")
	old := git(work, "rev-parse", "HEAD")
	git(work, "remote", "add", "origin", remote)
	git(work, "push", "origin", "HEAD:refs/heads/sofa/task")
	bundle := integrity.Bundle{
		Version:    integrity.Version,
		Repository: "owner/fixture",
		AttemptID:  strings.Repeat("a", 64),
		Generation: 2,
		BaseSHA:    old,
		Files: []integrity.File{{
			Path:         "fixture/a.txt",
			Operation:    "update",
			Mode:         integrity.RegularMode,
			BeforeSHA256: integrity.Hash([]byte("old")),
			Content:      []byte("new"),
		}},
	}
	if err := integrity.Seal(&bundle); err != nil {
		t.Fatal(err)
	}
	client, err := New("fixture-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	marker := "sofa-repair=test; feedback=review-1; generation=2; candidate=" + bundle.CandidateDigest
	commit, alreadyPushed, push, cleanup, err := client.prepareRepairCommitAtURL(ctx, bundle, "sofa/task", marker, remote)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyPushed {
		t.Fatal("fresh repair was already pushed")
	}
	defer cleanup()
	if err := push(); err != nil {
		t.Fatalf("exact-old-head push failed: %v", err)
	}
	if got := git(dir, "--git-dir="+remote, "rev-parse", "refs/heads/sofa/task"); got != commit {
		t.Fatalf("branch head %s, want candidate %s", got, commit)
	}
	if got := git(dir, "--git-dir="+remote, "rev-parse", commit+"^"); got != old {
		t.Fatalf("candidate parent %s, want old head %s", got, old)
	}
	replay, alreadyPushed, _, replayCleanup, err := client.prepareRepairCommitAtURL(ctx, bundle, "sofa/task", marker, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer replayCleanup()
	if replay != commit {
		t.Fatalf("crash replay made a different candidate: %s != %s", replay, commit)
	}
	if !alreadyPushed {
		t.Fatal("replay did not recognize the prepared commit at the remote branch")
	}
	if err := os.WriteFile(filepath.Join(work, "fixture", "a.txt"), []byte("human"), 0600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "fixture/a.txt")
	git(work, "commit", "-m", "human branch change")
	git(work, "push", "--force", "origin", "HEAD:refs/heads/sofa/task")
	if _, _, _, cleanupChanged, err := client.prepareRepairCommitAtURL(ctx, bundle, "sofa/task", marker, remote); err == nil {
		cleanupChanged()
		t.Fatal("changed branch was accepted as the prepared repair")
	}
	if err := push(); err == nil {
		t.Fatal("old-head lease overwrote a human branch change")
	}
	if got := git(dir, "--git-dir="+remote, "rev-parse", "refs/heads/sofa/task"); got == commit {
		t.Fatal("candidate overwrote the changed branch")
	}
}

func TestAwaitRepairHeadAllowsOnlyBoundedOldHeadLag(t *testing.T) {
	previous := state.Publication{
		PRNumber: 7,
		PRURL:    "https://github.com/owner/repo/pull/7",
		Branch:   "sofa/task",
		HeadSHA:  strings.Repeat("a", 40),
	}
	candidate := strings.Repeat("b", 40)
	pull := PullSnapshot{
		Number:         previous.PRNumber,
		URL:            previous.PRURL,
		HeadSHA:        previous.HeadSHA,
		HeadRef:        previous.Branch,
		HeadRepository: "owner/repo",
		BaseRef:        "main",
		BaseRepository: "owner/repo",
		State:          "open",
	}
	t.Run("stale then current", func(t *testing.T) {
		pull := pull
		reads := 0
		got, err := awaitRepairHead(context.Background(), func(context.Context) (PullSnapshot, error) {
			reads++
			if reads == 2 {
				pull.HeadSHA = candidate
			}
			return pull, nil
		}, "owner/repo", previous, "main", candidate)
		if err != nil || got.HeadSHA != candidate || reads != 2 {
			t.Fatalf("delayed PR head was not accepted: %+v, %v, reads=%d", got, err, reads)
		}
	})
	t.Run("different head", func(t *testing.T) {
		changed := pull
		changed.HeadSHA = strings.Repeat("c", 40)
		_, err := awaitRepairHead(context.Background(), func(context.Context) (PullSnapshot, error) { return changed, nil }, "owner/repo", previous, "main", candidate)
		if err == nil {
			t.Fatal("unrelated PR head was accepted")
		}
	})
	t.Run("different PR identity", func(t *testing.T) {
		changed := pull
		changed.Number++
		_, err := awaitRepairHead(context.Background(), func(context.Context) (PullSnapshot, error) { return changed, nil }, "owner/repo", previous, "main", candidate)
		if err == nil {
			t.Fatal("unrelated PR identity was accepted")
		}
	})
	t.Run("transient read", func(t *testing.T) {
		current := pull
		current.HeadSHA = candidate
		reads := 0
		got, err := awaitRepairHead(context.Background(), func(context.Context) (PullSnapshot, error) {
			reads++
			if reads == 1 {
				return PullSnapshot{}, errors.New("transient read")
			}
			return current, nil
		}, "owner/repo", previous, "main", candidate)
		if err != nil || got.HeadSHA != candidate || reads != 2 {
			t.Fatalf("transient PR read was not retried: %+v, %v, reads=%d", got, err, reads)
		}
	})
}

func TestVerifyPreparedRepairRejectsChangedRemoteIdentity(t *testing.T) {
	repo := "owner/fixture"
	old := strings.Repeat("a", 40)
	candidate := strings.Repeat("b", 40)
	base := strings.Repeat("c", 40)
	attemptID := strings.Repeat("d", 64)
	digest := strings.Repeat("e", 64)
	previous := state.Publication{Branch: "sofa/task", HeadSHA: old, PRNumber: 7, PRURL: "https://github.com/owner/fixture/pull/7"}
	repair := state.RepairIntent{FeedbackID: "review-7-8-abcd", PRBaseSHA: base, PRHeadSHA: old, PRNumber: 7, CandidateSHA: candidate, CandidateDigest: digest}
	marker := "sofa-repair=" + attemptID + "; feedback=review-7-8-abcd; generation=2; candidate=" + digest
	for _, tc := range []struct {
		name, prHead, refHead, message string
		draft                          bool
		good                           bool
	}{
		{"exact draft", candidate, candidate, marker, true, true},
		{"exact ready for review", candidate, candidate, marker, false, true},
		{"changed PR", strings.Repeat("f", 40), candidate, marker, true, false},
		{"changed branch", candidate, strings.Repeat("f", 40), marker, true, false},
		{"changed marker", candidate, candidate, "unrelated", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := New("fixture-token", roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/repos/owner/fixture/pulls/7":
					return jsonResponse(200, map[string]any{"number": 7, "html_url": previous.PRURL, "state": "open", "draft": tc.draft, "updated_at": time.Now().UTC(), "head": map[string]any{"sha": tc.prHead, "ref": previous.Branch, "repo": map[string]any{"full_name": repo}}, "base": map[string]any{"sha": base, "ref": "main", "repo": map[string]any{"full_name": repo}}}), nil
				case "/repos/owner/fixture/git/ref/heads/sofa/task":
					return jsonResponse(200, map[string]any{"object": map[string]any{"sha": tc.refHead}}), nil
				case "/repos/owner/fixture/git/commits/" + candidate:
					return jsonResponse(200, map[string]any{"sha": candidate, "message": tc.message, "parents": []any{map[string]any{"sha": old}}}), nil
				}
				return jsonResponse(404, map[string]any{"message": "unexpected endpoint"}), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.VerifyPreparedRepair(context.Background(), repo, previous, repair, "main", attemptID, 2)
			if tc.good && (err != nil || got.HeadSHA != candidate) {
				t.Fatalf("exact prepared repair rejected: %+v, %v", got, err)
			}
			if !tc.good && err == nil {
				t.Fatalf("changed remote identity accepted: %+v", got)
			}
		})
	}
}
