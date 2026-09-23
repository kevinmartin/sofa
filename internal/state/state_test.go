package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
var testOwner = Owner{"42", 1}

func admission() Admission {
	return Admission{"owner/consumer", 7, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 40), "approval-1", "ready-1", "actor-1", "project-1"}
}
func limits() Limits { return Limits{2, 2, 2, 2700} }
func engine() Engine { return Engine{Store: &MemoryStore{}, Now: func() time.Time { return testNow }} }
func admitted(t *testing.T, e Engine) Attempt {
	t.Helper()
	a, created, err := e.Admit(context.Background(), admission(), limits())
	if err != nil || !created {
		t.Fatalf("admit: %v %v", created, err)
	}
	return a
}
func claimed(t *testing.T, e Engine, id string) Fence {
	t.Helper()
	f, err := e.Claim(context.Background(), id, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func proof(owner Owner) RunProof { return RunProof{owner, "completed", "cancelled", testNow} }
func snapshot(t *testing.T, e Engine, id string) Attempt {
	t.Helper()
	s, err := e.Store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s.State.Attempts[id]
}

func TestAdmissionCrashAndDedup(t *testing.T) {
	e := engine()
	a := admitted(t, e)
	// Crash after commit and before dispatch: reconstruct the engine from the
	// same durable store. Pending intent is sufficient to redeliver, not re-admit.
	restarted := Engine{Store: e.Store, Now: e.Now}
	if got := snapshot(t, restarted, a.ID); got.Dispatch != "pending" || got.Phase != Pending {
		t.Fatalf("lost dispatch intent: %+v", got)
	}
	duplicate, created, err := restarted.Admit(context.Background(), admission(), limits())
	if err != nil || created || duplicate.ID != a.ID {
		t.Fatalf("dedup: %v %v", created, err)
	}
	for _, mutation := range []func(*Admission){func(a *Admission) { a.BaseSHA = strings.Repeat("d", 40) }, func(a *Admission) { a.ConfigDigest = strings.Repeat("e", 64) }} {
		changed := admission()
		mutation(&changed)
		if AttemptID(changed) != a.ID {
			t.Fatal("mutable snapshot changed identity")
		}
		if _, _, err := e.Admit(context.Background(), changed, limits()); !errors.Is(err, ErrAdmissionChanged) {
			t.Fatalf("changed snapshot admitted: %v", err)
		}
	}
	newAuthority := admission()
	newAuthority.ReadyEventID = "ready-2"
	if _, _, err := e.Admit(context.Background(), newAuthority, limits()); !errors.Is(err, ErrAdmissionChanged) {
		t.Fatalf("silent supersession: %v", err)
	}
	if err := e.MarkDispatched(context.Background(), a.ID); err != nil {
		t.Fatal(err)
	}
	f := claimed(t, e, a.ID)
	if _, err := e.Claim(context.Background(), a.ID, f.Owner); !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate same-run execution: %v", err)
	}
}

func TestConcurrentClaimsAndStaleCAS(t *testing.T) {
	e := engine()
	a := admitted(t, e)
	var won atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.Claim(context.Background(), a.ID, Owner{"run", i + 1})
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrClaimed) {
				t.Errorf("claim: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatalf("won=%d", won.Load())
	}
	s, err := e.Store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.CompareAndSwap(context.Background(), s.Revision, s.State); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.CompareAndSwap(context.Background(), s.Revision, s.State); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	delete(s.State.Attempts, a.ID)
	if len(snapshotState(t, e.Store).Attempts) != 1 {
		t.Fatal("mutable alias leaked into store")
	}
}

func snapshotState(t *testing.T, s Store) State {
	t.Helper()
	v, err := s.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v.State
}

func TestRecoveryRequiresTerminalOwnerAndFencesOldRun(t *testing.T) {
	e := engine()
	a := admitted(t, e)
	old := claimed(t, e, a.ID)
	if err := e.Charge(context.Background(), old, Counters{ModelCalls: 1, RuntimeSeconds: 40}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []RunProof{{testOwner, "in_progress", "", testNow}, {testOwner, "unknown", "", testNow}, {testOwner, "completed", "", testNow}, {testOwner, "completed", "cancelled", testNow.Add(-time.Hour)}} {
		if err := e.Recover(context.Background(), a.ID, p); !errors.Is(err, ErrActive) {
			t.Fatalf("unproven run reclaimed: %v", err)
		}
	}
	wrong := proof(Owner{"42", 2})
	if err := e.Recover(context.Background(), a.ID, wrong); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong run attempt: %v", err)
	}
	if err := e.Recover(context.Background(), a.ID, proof(testOwner)); err != nil {
		t.Fatal(err)
	}
	if err := e.AssertOwner(context.Background(), old); !errors.Is(err, ErrStale) {
		t.Fatal("stale owner accepted")
	}
	fresh, err := e.Claim(context.Background(), a.ID, Owner{"42", 2})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation <= old.Generation {
		t.Fatal("generation not fenced")
	}
	if err := e.Charge(context.Background(), old, Counters{ModelCalls: 1}); !errors.Is(err, ErrStale) {
		t.Fatalf("old owner charged: %v", err)
	}
	if err := e.Charge(context.Background(), fresh, Counters{ModelCalls: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.Charge(context.Background(), fresh, Counters{ModelCalls: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("model limit reset: %v", err)
	}
	got := snapshot(t, e, a.ID)
	if got.Counts.ModelCalls != 2 || got.Counts.InfrastructureRetries != 1 || got.Counts.RuntimeSeconds != 40 {
		t.Fatalf("counter loss: %+v", got.Counts)
	}
	if err := e.Recover(context.Background(), a.ID, proof(fresh.Owner)); err != nil {
		t.Fatal(err)
	}
	fresh, err = e.Claim(context.Background(), a.ID, Owner{"42", 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(context.Background(), a.ID, proof(fresh.Owner)); !errors.Is(err, ErrLimit) {
		t.Fatalf("retry limit reset: %v", err)
	}
}

func TestCheckpointRecoveryExpiryAndProvenance(t *testing.T) {
	e := engine()
	a := admitted(t, e)
	f := claimed(t, e, a.ID)
	cp := Checkpoint{Version, Executing, "artifact-1", strings.Repeat("d", 64), strings.Repeat("e", 40), f.Owner, f.Generation, testNow, testNow.Add(time.Hour)}
	bad := cp
	bad.Generation++
	if err := e.SaveCheckpoint(context.Background(), f, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong producer accepted: %v", err)
	}
	if err := e.SaveCheckpoint(context.Background(), f, cp); err != nil {
		t.Fatal(err)
	}
	if err := e.Recover(context.Background(), a.ID, proof(f.Owner)); err != nil {
		t.Fatal(err)
	}
	f, err := e.Claim(context.Background(), a.ID, Owner{"42", 2})
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, e, a.ID)
	if got.Checkpoint == nil || got.Phase != Executing {
		t.Fatalf("checkpoint implied passing stage: %+v", got)
	}
	e.Now = func() time.Time { return testNow.Add(2 * time.Hour) }
	p := proof(f.Owner)
	p.ObservedAt = e.now()
	if err := e.Recover(context.Background(), a.ID, p); err != nil {
		t.Fatal(err)
	}
	if snapshot(t, e, a.ID).Checkpoint != nil {
		t.Fatal("expired artifact remained usable")
	}
	if _, err := e.Claim(context.Background(), a.ID, Owner{"42", 3}); err != nil {
		t.Fatal(err)
	}
	if snapshot(t, e, a.ID).Phase != Executing {
		t.Fatal("missing checkpoint implied completion")
	}
}

func TestPublicationCrashAcknowledgementIsIdempotent(t *testing.T) {
	e := engine()
	a := admitted(t, e)
	f := claimed(t, e, a.ID)
	intent := Publication{Branch: "sofa/issue-7", ExpectedHead: a.Admission.BaseSHA, CandidateDigest: strings.Repeat("e", 64)}
	if err := e.BeginPublication(context.Background(), f, intent); !errors.Is(err, ErrInvalid) {
		t.Fatal("publication before validation")
	}
	if err := e.Advance(context.Background(), f, Validating); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginPublication(context.Background(), f, intent); err != nil {
		t.Fatal(err)
	}
	// External publisher created PR, then died before its acknowledgement.
	if err := e.Recover(context.Background(), a.ID, proof(f.Owner)); err != nil {
		t.Fatal(err)
	}
	f, err := e.Claim(context.Background(), a.ID, Owner{"42", 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot(t, e, a.ID); got.Publication == nil || got.Publication.Branch != intent.Branch {
		t.Fatal("lost publication intent")
	}
	if err := e.Advance(context.Background(), f, Validating); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginPublication(context.Background(), f, intent); err != nil {
		t.Fatal(err)
	}
	observed := intent
	observed.PRNumber = 9
	observed.PRURL = "https://github.com/owner/consumer/pull/9"
	observed.HeadSHA = strings.Repeat("f", 40)
	if err := e.MarkPublished(context.Background(), f, observed); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkPublished(context.Background(), f, observed); err != nil {
		t.Fatal(err)
	}
	other := observed
	other.PRNumber = 10
	if err := e.MarkPublished(context.Background(), f, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("second PR accepted: %v", err)
	}
	if _, err := e.Claim(context.Background(), a.ID, Owner{"42", 3}); !errors.Is(err, ErrClaimed) {
		t.Fatalf("completed work claimed: %v", err)
	}
	if err := e.AssertOwner(context.Background(), f); !errors.Is(err, ErrClaimed) {
		t.Fatal("published attempt permits another external write")
	}
}

func TestFailureClassificationAndObservations(t *testing.T) {
	for _, kind := range []string{"authentication", "quota"} {
		t.Run(kind, func(t *testing.T) {
			e := engine()
			a := admitted(t, e)
			f := claimed(t, e, a.ID)
			if err := e.Fail(context.Background(), f, kind); err != nil {
				t.Fatal(err)
			}
			got := snapshot(t, e, a.ID)
			want := Blocked
			if kind == "quota" {
				want = Deferred
			}
			if got.Phase != want {
				t.Fatalf("phase %s", got.Phase)
			}
			if err := e.AssertOwner(context.Background(), f); !errors.Is(err, ErrClaimed) {
				t.Fatal("failed owner may write")
			}
			err := e.Recover(context.Background(), a.ID, proof(f.Owner))
			if kind == "authentication" && !errors.Is(err, ErrClaimed) {
				t.Fatal("auth auto-retried")
			}
			if kind == "quota" && err != nil {
				t.Fatal(err)
			}
		})
	}
	e := engine()
	a := admitted(t, e)
	o := Observation{Version, "event-1", a.ID, "execution", "success", a.Admission.BaseSHA, "artifact:123", 0, 10, testNow}
	if err := e.Observe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if err := e.Observe(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	o.Outcome = "failure"
	if err := e.Observe(context.Background(), o); !errors.Is(err, ErrConflict) {
		t.Fatal("observation rewritten")
	}
	if got := len(snapshotState(t, e.Store).Observations); got != 1 {
		t.Fatalf("duplicate observations %d", got)
	}
}

func TestGitStoreOrphanCASAndNoHooks(t *testing.T) {
	for _, bare := range []bool{false, true} {
		t.Run(map[bool]string{false: "checkout", true: "bare"}[bare], func(t *testing.T) {
			dir := t.TempDir()
			args := []string{"init", "-q"}
			if bare {
				args = append(args, "--bare")
			}
			args = append(args, dir)
			if b, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				t.Fatalf("init: %s %v", b, err)
			}
			gitDir := dir
			if !bare {
				gitDir = filepath.Join(dir, ".git")
			}
			// A failing hook and hostile inherited Git environment must not affect plumbing.
			if err := os.WriteFile(filepath.Join(gitDir, "hooks", "reference-transaction"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
			t.Setenv("GIT_CONFIG_VALUE_0", filepath.Join(gitDir, "hooks"))
			s := GitStore{dir}
			e := Engine{Store: s, Now: func() time.Time { return testNow }}
			a := admitted(t, e)
			first, err := s.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			line, err := s.command(context.Background(), nil, "rev-list", "--parents", "-1", first.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Fields(string(line))) != 1 {
				t.Fatal("state root inherited source history")
			}
			claimed(t, e, a.ID)
			second, err := s.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			line, err = s.command(context.Background(), nil, "rev-list", "--parents", "-1", second.Revision)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Fields(string(line))
			if len(parts) != 2 || parts[1] != first.Revision {
				t.Fatalf("wrong parent: %s", line)
			}
			if err := s.CompareAndSwap(context.Background(), first.Revision, first.State); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale ref accepted: %v", err)
			}
		})
	}
}

func TestDecodeRejectsSchemaDriftAndTrailingContent(t *testing.T) {
	for _, data := range []string{`{"version":2,"attempts":{},"observations":[]}`, `{"version":1,"attempts":{},"observations":[],"secret":"no"}`, `{"version":1,"attempts":{},"observations":[]} {}`} {
		if _, err := Decode([]byte(data)); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %s", data)
		}
	}
}

func TestGitStoreConcurrentClaims(t *testing.T) {
	dir := t.TempDir()
	if b, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("init: %s %v", b, err)
	}
	e := Engine{Store: GitStore{dir}, Now: func() time.Time { return testNow }}
	a := admitted(t, e)
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.Claim(context.Background(), a.ID, Owner{"claiming-run", i})
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrClaimed) {
				t.Errorf("concurrent Git claim: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("Git claim winners: %d", winners.Load())
	}
}
