package lifecycle

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlanPollCoalescesMissedTicksAndDuplicateWakeups(t *testing.T) {
	base := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		now        time.Time
		last       time.Time
		lastWakeID string
		wakeID     string
		wantDue    bool
		wantMissed int64
	}{
		{
			name:    "first poll",
			now:     base,
			wantDue: true,
		},
		{
			name: "idle",
			now:  base.Add(5 * time.Minute),
			last: base,
		},
		{
			name:       "one due window",
			now:        base.Add(10 * time.Minute),
			last:       base,
			wantDue:    true,
			wantMissed: 1,
		},
		{
			name:       "hours coalesce",
			now:        base.Add(4 * time.Hour),
			last:       base,
			wantDue:    true,
			wantMissed: 24,
		},
		{
			name:       "fresh event",
			now:        base.Add(time.Minute),
			last:       base,
			lastWakeID: "old",
			wakeID:     "new",
			wantDue:    true,
		},
		{
			name:       "duplicate event",
			now:        base.Add(time.Minute),
			last:       base,
			lastWakeID: "same",
			wakeID:     "same",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PlanPoll(tc.now, tc.last, 10*time.Minute, tc.lastWakeID, tc.wakeID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Due != tc.wantDue || got.Missed != tc.wantMissed {
				t.Fatalf("got %+v, want due=%t missed=%d", got, tc.wantDue, tc.wantMissed)
			}
			if got.Due && got.Next != tc.now.Add(10*time.Minute) {
				t.Fatalf("next tick did not coalesce from observation: %v", got.Next)
			}
		})
	}
}

func TestGatesAndProjectionFailClosed(t *testing.T) {
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	base := Projection{
		Current:       Verification,
		LastProjected: Verification,
		Authorized:    true,
		CandidateSHA:  sha,
		PRHeadSHA:     sha,
		PRBaseSHA:     other,
		PRExists:      true,
		RequiredGates: []string{"quality", "security"},
		GateEvidence: []GateEvidence{
			{
				ID:           "quality",
				CandidateSHA: sha,
				BaseSHA:      other,
				Outcome:      GatePassed,
			},
			{
				ID:           "security",
				CandidateSHA: sha,
				BaseSHA:      other,
				Outcome:      GatePassed,
			},
		},
	}
	if got := ProjectDecision(base); got.MoveTo != Review {
		t.Fatalf("complete exact-head evidence did not advance: %+v", got)
	}
	for name, mutate := range map[string]func(*Projection){
		"missing gate":        func(p *Projection) { p.GateEvidence = p.GateEvidence[:1] },
		"stale gate":          func(p *Projection) { p.GateEvidence[1].CandidateSHA = other },
		"stale base":          func(p *Projection) { p.GateEvidence[1].BaseSHA = sha },
		"failed gate":         func(p *Projection) { p.GateEvidence[1].Outcome = GateFailed },
		"fixture in live run": func(p *Projection) { p.GateEvidence[1].Fixture = true },
		"changed PR":          func(p *Projection) { p.PRHeadSHA = other },
		"manual board edit":   func(p *Projection) { p.Current = Ready },
	} {
		t.Run(name, func(t *testing.T) {
			input := base
			input.GateEvidence = append([]GateEvidence(nil), base.GateEvidence...)
			mutate(&input)
			if got := ProjectDecision(input); got.MoveTo != "" {
				t.Fatalf("untrusted or stale evidence advanced Project: %+v", got)
			}
		})
	}
	base.GateEvidence[1].Fixture = true
	base.AllowFixture = true
	if got := ProjectDecision(base); got.MoveTo != Review {
		t.Fatalf("explicit replay fixture did not advance: %+v", got)
	}
}

func TestGatesPassedIgnoresDuplicateUnrelatedEvidence(t *testing.T) {
	candidateSHA := strings.Repeat("a", 40)
	baseSHA := strings.Repeat("b", 40)
	quality := GateEvidence{
		ID:           "quality",
		CandidateSHA: candidateSHA,
		BaseSHA:      baseSHA,
		Outcome:      GatePassed,
	}
	unrelated := GateEvidence{
		ID:           "telemetry",
		CandidateSHA: candidateSHA,
		BaseSHA:      baseSHA,
		Outcome:      GateFailed,
	}
	evidence := []GateEvidence{quality, unrelated, unrelated}
	if passed, reason := GatesPassed([]string{"quality"}, evidence, candidateSHA, baseSHA, false); !passed || reason != "" {
		t.Fatalf("unrelated duplicate blocked required gate: passed=%t reason=%q", passed, reason)
	}
	evidence = append(evidence, quality)
	if passed, reason := GatesPassed([]string{"quality"}, evidence, candidateSHA, baseSHA, false); passed || reason != "ambiguous gate evidence" {
		t.Fatalf("duplicate required gate was accepted: passed=%t reason=%q", passed, reason)
	}
}

func TestLifecycleTransitionsAndTerminalOutcomes(t *testing.T) {
	sha := strings.Repeat("a", 40)
	baseSHA := strings.Repeat("c", 40)
	verified := func(stage Stage) Projection {
		return Projection{
			Current:       stage,
			Authorized:    true,
			PRExists:      true,
			CandidateSHA:  sha,
			PRHeadSHA:     sha,
			PRBaseSHA:     baseSHA,
			RequiredGates: []string{"quality"},
			GateEvidence: []GateEvidence{{
				ID:           "quality",
				CandidateSHA: sha,
				BaseSHA:      baseSHA,
				Outcome:      GatePassed,
			}},
		}
	}
	reviewMerged := verified(Review)
	reviewMerged.PRMerged = true
	reviewMerged.MergedSHA = sha
	release := verified(Release)
	release.PRMerged = true
	release.MergedSHA = sha
	releaseOpen := release
	releaseOpen.PRMerged = false
	releaseRequired := release
	releaseRequired.ReleaseRequired = true
	releaseWrong := releaseRequired
	releaseWrong.ReleasePassed = true
	releaseWrong.ReleaseSHA = strings.Repeat("b", 40)
	releasePassed := releaseRequired
	releasePassed.ReleasePassed = true
	releasePassed.ReleaseSHA = sha
	for _, tc := range []struct {
		name    string
		input   Projection
		want    Stage
		blocked bool
	}{
		{
			name: "ready remains until publication",
			input: Projection{
				Current:    Ready,
				Authorized: true,
			},
		},
		{
			name: "ready to building",
			input: Projection{
				Current:      Ready,
				Authorized:   true,
				Published:    true,
				PRExists:     true,
				CandidateSHA: sha,
				PRHeadSHA:    sha,
			},
			want: Building,
		},
		{
			name: "building to verification",
			input: Projection{
				Current:      Building,
				Authorized:   true,
				PRExists:     true,
				CandidateSHA: sha,
				PRHeadSHA:    sha,
			},
			want: Verification,
		},
		{
			name:  "review to release",
			input: reviewMerged,
			want:  Release,
		},
		{
			name:    "open PR in release cannot finish",
			input:   releaseOpen,
			blocked: true,
		},
		{
			name:  "release to done without configured gate",
			input: release,
			want:  Done,
		},
		{
			name:    "release waits for gate",
			input:   releaseRequired,
			blocked: true,
		},
		{
			name:    "release rejects other commit",
			input:   releaseWrong,
			blocked: true,
		},
		{
			name:  "release exact commit",
			input: releasePassed,
			want:  Done,
		},
		{
			name: "manual skip to review without gates",
			input: Projection{
				Current:      Review,
				Authorized:   true,
				PRExists:     true,
				PRMerged:     true,
				CandidateSHA: sha,
				PRHeadSHA:    sha,
				PRBaseSHA:    baseSHA,
				MergedSHA:    sha,
			},
			blocked: true,
		},
		{
			name: "manual skip to release without gates",
			input: Projection{
				Current:      Release,
				Authorized:   true,
				PRExists:     true,
				CandidateSHA: sha,
				PRHeadSHA:    sha,
				PRBaseSHA:    baseSHA,
				MergedSHA:    sha,
			},
			blocked: true,
		},
		{
			name: "closed without merge",
			input: Projection{
				Current:    Review,
				Authorized: true,
				PRExists:   true,
				PRClosed:   true,
			},
			blocked: true,
		},
		{
			name: "cancelled",
			input: Projection{
				Current:         Building,
				Authorized:      true,
				TerminalOutcome: "cancelled",
			},
			blocked: true,
		},
		{
			name: "superseded",
			input: Projection{
				Current:         Verification,
				Authorized:      true,
				TerminalOutcome: "superseded",
			},
			blocked: true,
		},
		{
			name: "deferred",
			input: Projection{
				Current:         Discovery,
				Authorized:      true,
				TerminalOutcome: "deferred",
			},
			blocked: true,
		},
		{
			name: "blocked",
			input: Projection{
				Current:         SpecReview,
				Authorized:      true,
				TerminalOutcome: "blocked",
			},
			blocked: true,
		},
		{
			name: "inbox cannot auto-admit",
			input: Projection{
				Current: Inbox,
			},
			blocked: true,
		},
		{
			name: "discovery cannot auto-approve",
			input: Projection{
				Current:    Discovery,
				Authorized: true,
			},
		},
		{
			name: "spec review waits for owner",
			input: Projection{
				Current:    SpecReview,
				Authorized: true,
			},
		},
		{
			name: "backlog waits for owner",
			input: Projection{
				Current:    Backlog,
				Authorized: true,
			},
		},
		{
			name:  "review cannot auto-merge",
			input: verified(Review),
		},
		{
			name: "done remains terminal",
			input: Projection{
				Current:    Done,
				Authorized: true,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectDecision(tc.input)
			if got.MoveTo != tc.want || (got.BlockedReason != "") != tc.blocked {
				t.Fatalf("got %+v, want stage %s blocked=%t", got, tc.want, tc.blocked)
			}
		})
	}
}

func TestWIPDependenciesAndChangedBacklog(t *testing.T) {
	selected, err := SelectOpenSlots([]int{8, 3, 5}, 1, 2)
	if err != nil || !reflect.DeepEqual(selected, []int{3}) {
		t.Fatalf("WIP selection = %v, %v", selected, err)
	}
	if selected, err = SelectOpenSlots([]int{3}, 2, 2); err != nil || len(selected) != 0 {
		t.Fatalf("exhausted WIP selection = %v, %v", selected, err)
	}
	ready, missing, err := DependenciesReady([]int{3, 2}, map[int]Stage{2: Done, 3: Release})
	if err != nil || ready || !reflect.DeepEqual(missing, []int{3}) {
		t.Fatalf("dependencies = %t, %v, %v", ready, missing, err)
	}
	changed := ChangedBacklog(map[int]string{4: "old", 3: "same"}, map[int]string{4: "new", 3: "same"})
	if !reflect.DeepEqual(changed, []int{4}) {
		t.Fatalf("backlog changed = %v", changed)
	}
}
