package github

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/integrity"
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
	commit, push, cleanup, err := client.prepareRepairCommitAtURL(ctx, bundle, "sofa/task", marker, remote)
	if err != nil {
		t.Fatal(err)
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
	replay, _, replayCleanup, err := client.prepareRepairCommitAtURL(ctx, bundle, "sofa/task", marker, remote)
	if err != nil {
		t.Fatal(err)
	}
	defer replayCleanup()
	if replay != commit {
		t.Fatalf("crash replay made a different candidate: %s != %s", replay, commit)
	}
	if err := os.WriteFile(filepath.Join(work, "fixture", "a.txt"), []byte("human"), 0600); err != nil {
		t.Fatal(err)
	}
	git(work, "add", "fixture/a.txt")
	git(work, "commit", "-m", "human branch change")
	git(work, "push", "--force", "origin", "HEAD:refs/heads/sofa/task")
	if err := push(); err == nil {
		t.Fatal("old-head lease overwrote a human branch change")
	}
	if got := git(dir, "--git-dir="+remote, "rev-parse", "refs/heads/sofa/task"); got == commit {
		t.Fatal("candidate overwrote the changed branch")
	}
}
