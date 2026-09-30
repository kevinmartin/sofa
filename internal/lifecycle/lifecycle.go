// Package lifecycle makes deterministic polling and Project projection
// decisions. GitHub reads and writes remain outside this package.
package lifecycle

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Stage string

const (
	Inbox        Stage = "inbox"
	Discovery    Stage = "discovery"
	SpecReview   Stage = "spec_review"
	Backlog      Stage = "backlog"
	Ready        Stage = "ready"
	Building     Stage = "building"
	Verification Stage = "verification"
	Review       Stage = "review"
	Release      Stage = "release"
	Done         Stage = "done"
)

var Stages = []Stage{Inbox, Discovery, SpecReview, Backlog, Ready, Building, Verification, Review, Release, Done}
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Statuses maps canonical states to the trusted Project's display names.
// A display name must identify exactly one stage.
type Statuses map[Stage]string

func (m Statuses) Validate() error {
	if len(m) != len(Stages) {
		return errors.New("lifecycle requires every canonical Project status")
	}
	seen := make(map[string]bool, len(m))
	for _, stage := range Stages {
		name := m[stage]
		if name == "" || len(name) > 100 || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\x00\r\n") || seen[name] {
			return fmt.Errorf("invalid or duplicate Project status for %s", stage)
		}
		seen[name] = true
	}
	return nil
}

func (m Statuses) StageFor(name string) (Stage, bool) {
	for stage, display := range m {
		if name == display {
			return stage, true
		}
	}
	return "", false
}

// PollPlan coalesces every missed interval into one scan. The event revision
// prevents duplicate manual/event wakeups from producing repeated scans.
type PollPlan struct {
	Due    bool
	Next   time.Time
	WakeID string
	Missed int64
}

func PlanPoll(now, last time.Time, interval time.Duration, lastWakeID, wakeID string) (PollPlan, error) {
	if now.IsZero() || interval < time.Minute || interval > 24*time.Hour || len(wakeID) > 512 || strings.ContainsAny(wakeID, "\x00\r\n") {
		return PollPlan{}, errors.New("invalid poll schedule")
	}
	now = now.UTC()
	last = last.UTC()
	if !last.IsZero() && now.Before(last) {
		return PollPlan{}, errors.New("poll clock moved backward")
	}
	eventDue := wakeID != "" && wakeID != lastWakeID
	timeDue := last.IsZero() || !now.Before(last.Add(interval))
	if !timeDue && !eventDue {
		return PollPlan{
			Next:   last.Add(interval),
			WakeID: lastWakeID,
		}, nil
	}
	plan := PollPlan{
		Due:    true,
		Next:   now.Add(interval),
		WakeID: lastWakeID,
	}
	if eventDue {
		plan.WakeID = wakeID
	}
	if !last.IsZero() && !now.Before(last) {
		plan.Missed = int64(now.Sub(last) / interval)
	}
	return plan, nil
}

// SelectOpenSlots limits new work deterministically. Candidates are already
// authorized by the caller and sorted by stable issue number; this function
// never treats mere presence in a Project as admission.
func SelectOpenSlots(candidates []int, active, limit int) ([]int, error) {
	if active < 0 || limit < 1 || limit > 100 || len(candidates) > 10000 {
		return nil, errors.New("invalid WIP inputs")
	}
	if active >= limit {
		return nil, nil
	}
	selected := slices.Clone(candidates)
	slices.Sort(selected)
	for i, number := range selected {
		if number <= 0 || i > 0 && selected[i-1] == number {
			return nil, errors.New("invalid or duplicate candidate issue")
		}
	}
	return selected[:min(len(selected), limit-active)], nil
}

// DependenciesReady only accepts owner-configured dependencies. An unknown,
// canceled, or merely merged dependency is not Done and cannot authorize Ready.
func DependenciesReady(numbers []int, observed map[int]Stage) (bool, []int, error) {
	missing := make([]int, 0)
	seen := make(map[int]bool, len(numbers))
	for _, number := range numbers {
		if number <= 0 || seen[number] {
			return false, nil, errors.New("invalid or duplicate dependency")
		}
		seen[number] = true
		if observed[number] != Done {
			missing = append(missing, number)
		}
	}
	slices.Sort(missing)
	return len(missing) == 0, missing, nil
}

type GateOutcome string

const (
	GatePassed  GateOutcome = "passed"
	GateFailed  GateOutcome = "failed"
	GateBlocked GateOutcome = "blocked"
)

type GateEvidence struct {
	ID           string
	CandidateSHA string
	BaseSHA      string
	Outcome      GateOutcome
	Fixture      bool
}

// GatesPassed requires one unambiguous passing result per required gate for
// the exact candidate. Replay fixtures may be used only when explicitly
// permitted by the caller's local test, never for live readiness.
func GatesPassed(required []string, evidence []GateEvidence, candidateSHA, baseSHA string, allowFixture bool) (bool, string) {
	if !shaPattern.MatchString(candidateSHA) || !shaPattern.MatchString(baseSHA) || len(required) == 0 {
		return false, "required gate plan or candidate unavailable"
	}
	results := make(map[string]GateEvidence, len(evidence))
	for _, result := range evidence {
		if result.ID == "" || result.CandidateSHA != candidateSHA || result.BaseSHA != baseSHA || result.Fixture && !allowFixture {
			continue
		}
		if _, exists := results[result.ID]; exists {
			return false, "ambiguous gate evidence"
		}
		results[result.ID] = result
	}
	seen := make(map[string]bool, len(required))
	for _, gate := range required {
		if gate == "" || seen[gate] {
			return false, "invalid gate plan"
		}
		seen[gate] = true
		result, ok := results[gate]
		if !ok {
			return false, "required gate evidence missing or stale"
		}
		if result.Outcome != GatePassed {
			return false, "required gate did not pass"
		}
	}
	return true, ""
}

type Projection struct {
	Current         Stage
	LastProjected   Stage
	Authorized      bool
	Published       bool
	CandidateSHA    string
	PRHeadSHA       string
	PRBaseSHA       string
	PRExists        bool
	PRClosed        bool
	PRMerged        bool
	RequiredGates   []string
	GateEvidence    []GateEvidence
	ReleaseRequired bool
	ReleasePassed   bool
	ReleaseSHA      string
	MergedSHA       string
	TerminalOutcome string // blocked, deferred, cancelled, or superseded
	AllowFixture    bool   // local replay only
}

type Decision struct {
	MoveTo        Stage
	BlockedReason string
	NextAction    string
	Conflict      bool
}

// ProjectDecision only proposes the next adjacent system-owned transition.
// Owner-controlled Product states are never written by this projection.
// LastProjected is the last observed board state held in the ledger; a manual
// move away from it requires a new observation, not an automatic fight.
func ProjectDecision(p Projection) Decision {
	if p.Current != p.LastProjected && p.LastProjected != "" {
		return Decision{
			Conflict:   true,
			NextAction: "inspect manual Project status change",
		}
	}
	if p.TerminalOutcome != "" {
		return Decision{
			BlockedReason: p.TerminalOutcome,
			NextAction:    "inspect terminal task outcome",
		}
	}
	if !p.Authorized {
		return Decision{
			BlockedReason: "authorization unavailable",
			NextAction:    "restore owner approval",
		}
	}
	if p.PRExists && p.PRClosed && !p.PRMerged {
		return Decision{
			BlockedReason: "closed without merge",
			NextAction:    "inspect closed PR",
		}
	}
	switch p.Current {
	case Ready:
		// Milestone-01 publication revalidates current Ready. Moving it while
		// the worker runs would revoke the active grant mid-attempt.
		if p.Published && p.PRExists && p.CandidateSHA != "" && p.PRHeadSHA == p.CandidateSHA {
			return Decision{
				MoveTo: Building,
			}
		}
	case Building:
		if p.PRExists && p.CandidateSHA != "" && p.PRHeadSHA == p.CandidateSHA {
			return Decision{
				MoveTo: Verification,
			}
		}
	case Verification:
		if !p.PRExists || p.PRHeadSHA == "" || p.PRHeadSHA != p.CandidateSHA {
			return Decision{
				BlockedReason: "candidate changed or unavailable",
				NextAction:    "revalidate exact PR head",
			}
		}
		passed, reason := GatesPassed(p.RequiredGates, p.GateEvidence, p.CandidateSHA, p.PRBaseSHA, p.AllowFixture)
		if passed {
			return Decision{
				MoveTo: Review,
			}
		}
		return Decision{
			BlockedReason: reason,
			NextAction:    "wait for current required gate evidence",
		}
	case Review:
		if !p.PRExists || p.PRHeadSHA != p.CandidateSHA {
			return Decision{
				BlockedReason: "candidate changed or unavailable",
				NextAction:    "revalidate exact PR head",
			}
		}
		passed, reason := GatesPassed(p.RequiredGates, p.GateEvidence, p.CandidateSHA, p.PRBaseSHA, p.AllowFixture)
		if !passed {
			return Decision{
				BlockedReason: reason,
				NextAction:    "restore current required gate evidence",
			}
		}
		if p.PRMerged && shaPattern.MatchString(p.MergedSHA) {
			return Decision{
				MoveTo: Release,
			}
		}
	case Release:
		if !p.PRExists || p.PRHeadSHA != p.CandidateSHA {
			return Decision{
				BlockedReason: "candidate changed or unavailable",
				NextAction:    "revalidate exact PR head",
			}
		}
		passed, reason := GatesPassed(p.RequiredGates, p.GateEvidence, p.CandidateSHA, p.PRBaseSHA, p.AllowFixture)
		if !passed {
			return Decision{
				BlockedReason: reason,
				NextAction:    "restore current required gate evidence",
			}
		}
		if !shaPattern.MatchString(p.MergedSHA) {
			return Decision{
				BlockedReason: "merge identity unavailable",
				NextAction:    "observe merged commit",
			}
		}
		if !p.ReleaseRequired || p.ReleasePassed && p.ReleaseSHA == p.MergedSHA {
			return Decision{
				MoveTo: Done,
			}
		}
		return Decision{
			BlockedReason: "release verification missing or failed",
			NextAction:    "observe release checks for merged commit",
		}
	}
	return Decision{}
}

// ChangedBacklog returns changed/stale approved items only; callers may then
// propose re-review. Polling must not rewrite unchanged specifications.
func ChangedBacklog(approved map[int]string, current map[int]string) []int {
	changed := make([]int, 0)
	for issue, digest := range approved {
		if current[issue] != digest {
			changed = append(changed, issue)
		}
	}
	slices.Sort(changed)
	return changed
}
