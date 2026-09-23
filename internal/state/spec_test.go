package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"testing"
)

func TestSaveSpecPreservesLedgerAndIsImmutable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	g := GitStore{Directory: dir}
	if err := g.CompareAndSwap(ctx, "", Empty()); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"title":"Fixture","body":"Do a bounded change"}`)
	h := sha256.Sum256(content)
	digest := hex.EncodeToString(h[:])
	if err := g.SaveSpec(ctx, "I_test", digest, content); err != nil {
		t.Fatal(err)
	}
	first, err := g.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.State.Attempts) != 0 || first.Revision == "" {
		t.Fatal("spec write lost ledger")
	}
	path, err := SpecPath("I_test", digest)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := g.command(ctx, nil, "cat-file", "blob", first.Revision+":"+path)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatal("spec snapshot missing or changed")
	}
	if err := g.CompareAndSwap(ctx, first.Revision, first.State); err != nil {
		t.Fatal(err)
	}
	second, err := g.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = g.command(ctx, nil, "cat-file", "blob", second.Revision+":"+path)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatal("ledger update discarded specification")
	}
	if err := g.SaveSpec(ctx, "I_test", digest, content); err != nil {
		t.Fatal(err)
	}
	third, err := g.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if third.Revision != second.Revision {
		t.Fatal("repeat snapshot created duplicate state commit")
	}
	if err := g.SaveSpec(ctx, "I_test", digest, []byte("changed")); err == nil {
		t.Fatal("accepted changed content under digest")
	}
	// Opaque IDs may contain path syntax. Their hash must keep the Git path
	// within specs/ while preserving the snapshot.
	unsafeNamePath, err := SpecPath("../escape", digest)
	if err != nil || unsafeNamePath == path {
		t.Fatal("opaque issue ID did not receive a distinct safe path")
	}
	if err := g.SaveSpec(ctx, "../escape", digest, content); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/escape"); !os.IsNotExist(err) {
		t.Fatal("unsafe path wrote outside Git state")
	}
}
