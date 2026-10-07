package distribution

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/github"
)

func TestCanaryRejectsUnrelatedFailedOrChangedIdentity(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	mutations := map[string]func(*github.ReleaseCanaryRun, *Canary){"failure": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Conclusion = "failure" }, "wrong source": func(_ *github.ReleaseCanaryRun, e *Canary) { e.SourceSHA = oldSource }, "different caller": func(r *github.ReleaseCanaryRun, _ *Canary) { r.HeadSHA = oldSource }, "different repository": func(r *github.ReleaseCanaryRun, _ *Canary) { r.HeadRepository.ID = 10 }, "different dispatch": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Title = "Sofa release canary / " + oldSource }, "different run": func(r *github.ReleaseCanaryRun, _ *Canary) { r.ID++ }, "different workflow": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Path = ".github/workflows/other.yml" }, "queued": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Status = "queued" }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r, e := run, evidence
			mutate(&r, &e)
			api := newFake()
			if err := Promote(context.Background(), api, p, r, e); err == nil {
				t.Fatal("untrusted canary allowed promotion")
			}
			if api.updates != 0 {
				t.Fatal("invalid evidence mutated channel")
			}
		})
	}
}

type canaryFake struct {
	runs       []github.ReleaseCanaryRun
	pages      map[int][]github.ReleaseCanaryRun
	dispatches int
	run        github.ReleaseCanaryRun
	err        error
}

func (f *canaryFake) RepositoryInfo(context.Context, string) (github.RepositoryInfo, error) {
	return github.RepositoryInfo{FullName: DisposableRepository, DefaultBranch: "main"}, nil
}

func (f *canaryFake) Reference(context.Context, string, string) (string, bool, error) {
	return otherSource, true, nil
}

func (f *canaryFake) DispatchReleaseCanary(_ context.Context, _, _, _, _, _, _, _ string) error {
	f.dispatches++
	f.runs = []github.ReleaseCanaryRun{f.run}
	return nil
}

func (f *canaryFake) ReleaseCanaryRuns(_ context.Context, _ string, page int, since time.Time) ([]github.ReleaseCanaryRun, error) {
	if f.pages != nil {
		return f.pages[page], f.err
	}
	var recent []github.ReleaseCanaryRun
	for _, run := range f.runs {
		if run.CreatedAt.IsZero() || !run.CreatedAt.Before(since) {
			recent = append(recent, run)
		}
	}
	start := (page - 1) * 100
	if start >= len(recent) {
		return nil, f.err
	}
	end := start + 100
	if end > len(recent) {
		end = len(recent)
	}
	return recent[start:end], f.err
}

func (f *canaryFake) ReleaseCanaryRun(context.Context, string, int64) (github.ReleaseCanaryRun, error) {
	return f.run, f.err
}

func TestCanaryRetryReusesExactSuccessfulDispatchAndDoesNotTrustNeighbors(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	neighbor := run
	neighbor.ID = 99
	neighbor.Title = "another run"
	api := &canaryFake{runs: []github.ReleaseCanaryRun{neighbor, run}, run: run}
	got, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got != evidence || api.dispatches != 0 {
		t.Fatalf("canary retry=%+v err=%v dispatches=%d", got, err, api.dispatches)
	}
	api = &canaryFake{run: run}
	got, err = RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got.RunID != run.ID || api.dispatches != 1 {
		t.Fatalf("canary dispatch=%+v %v count=%d", got, err, api.dispatches)
	}
	duplicate := run
	duplicate.ID++
	api = &canaryFake{runs: []github.ReleaseCanaryRun{run, duplicate}, run: run}
	if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil {
		t.Fatal("duplicate correlation accepted")
	}
}

func TestCanaryRejectsDuplicateCorrelationAcrossDiscoveryPages(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	first := make([]github.ReleaseCanaryRun, 100)
	first[0] = run
	for i := 1; i < len(first); i++ {
		first[i].ID = int64(1000 + i)
		first[i].Title = "neighbor"
	}
	duplicate := run
	duplicate.ID++
	api := &canaryFake{pages: map[int][]github.ReleaseCanaryRun{1: first, 2: {duplicate}}}
	if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil {
		t.Fatal("second-page duplicate correlation accepted")
	}
	if api.dispatches != 0 {
		t.Fatal("ambiguous discovery dispatched another run")
	}
}

func TestCanaryDiscoveryExcludesMoreThanOneThousandHistoricRuns(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	history := make([]github.ReleaseCanaryRun, 1100)
	for i := range history {
		history[i] = run
		history[i].ID = int64(1000 + i)
		// Even the same title outside the attempt window is historical, not
		// evidence for this current release attempt.
		history[i].CreatedAt = time.Now().UTC().Add(-48 * time.Hour)
	}
	api := &canaryFake{runs: append(history, run), run: run}
	got, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got != evidence || api.dispatches != 0 {
		t.Fatalf("historic lifetime bound interfered: %+v %v dispatches=%d", got, err, api.dispatches)
	}
}

func TestCanaryRejectsMissingOrFutureCreationTime(t *testing.T) {
	for _, created := range []time.Time{{}, time.Now().UTC().Add(time.Hour)} {
		p := fixturePlan()
		run, evidence := trustedCanary(p)
		run.CreatedAt = created
		api := &canaryFake{runs: []github.ReleaseCanaryRun{run}, run: run}
		if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil || api.dispatches != 0 {
			t.Fatal("invalid creation time authorized or retriggered a canary")
		}
	}
}

func TestCanaryCancellationDoesNotBecomeSuccess(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	run.Status = "queued"
	run.Conclusion = ""
	api := &canaryFake{runs: []github.ReleaseCanaryRun{run}, run: run}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunCanary(ctx, api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled canary=%v", err)
	}
}

func TestPromotionRequiresFreshCanary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		created time.Time
		allowed bool
	}{
		{name: "recent successful run", created: time.Now().UTC().Add(-time.Hour), allowed: true},
		{name: "stale successful run", created: time.Now().UTC().Add(-25 * time.Hour)},
		{name: "missing creation time"},
		{name: "future creation time", created: time.Now().UTC().Add(2 * time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fixturePlan()
			api := newFake()
			if _, err := Publish(t.Context(), api, p, fixtureBundle(t, p)); err != nil {
				t.Fatal(err)
			}
			run, evidence := trustedCanary(p)
			run.CreatedAt = tc.created
			err := Promote(t.Context(), api, p, run, evidence)
			if tc.allowed {
				if err != nil || api.updates != 1 || api.channels[p.Channel] != p.SourceSHA {
					t.Fatalf("fresh evidence did not promote exact source: updates=%d err=%v", api.updates, err)
				}
				return
			}
			if err == nil || api.updates != 0 || api.channels[p.Channel] != p.ExpectedChannelSHA {
				t.Fatalf("invalid creation time authorized promotion: updates=%d err=%v", api.updates, err)
			}
		})
	}
}
