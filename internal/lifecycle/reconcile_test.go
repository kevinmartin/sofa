package lifecycle

import (
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/state"
)

func scanFixture(t *testing.T) ScanInput {
	t.Helper()
	approved := strings.Repeat("a", 64)
	_, sourceDigest, err := admission.CanonicalSpec("Idea", "Investigate behavior")
	if err != nil {
		t.Fatal(err)
	}
	configDigest := strings.Repeat("b", 64)
	baseSHA := strings.Repeat("c", 40)
	when := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	ledger := state.Empty()
	issues := make([]admission.Snapshot, 0, 2)
	evidence := make(map[string]DeliveryEvidence)
	authority := make(map[string]bool)
	for number := 1; number <= 2; number++ {
		issueID := "I_" + string(rune('0'+number))
		itemID := "PVTI_" + string(rune('0'+number))
		head := strings.Repeat(string(rune('0'+number)), 40)
		admissionRecord := state.Admission{
			Repository:      "owner/repo",
			Issue:           int64(number),
			SpecDigest:      approved,
			ConfigDigest:    configDigest,
			BaseSHA:         baseSHA,
			ProjectID:       "P_1",
			ProjectItemID:   itemID,
			StatusOptionID:  "ready-option",
			StatusUpdatedAt: when,
		}
		id := state.AttemptID(admissionRecord)
		url := "https://github.com/owner/repo/pull/" + string(rune('0'+number))
		ledger.Attempts[id] = state.Attempt{
			ID:        id,
			Admission: admissionRecord,
			Phase:     state.Draft,
			Dispatch:  "claimed",
			Limits: state.Limits{
				RuntimeSeconds: 600,
			},
			Publication: &state.Publication{
				Branch:          "sofa/issue-" + string(rune('0'+number)),
				ExpectedHead:    baseSHA,
				CandidateDigest: approved,
				HeadSHA:         head,
				PRNumber:        int64(number),
				PRURL:           url,
			},
			CreatedAt: when,
			UpdatedAt: when,
		}
		ledger.Specs[issueID] = state.SpecRecord{
			Repository:       "owner/repo",
			IssueID:          issueID,
			Issue:            int64(number),
			ProjectID:        "P_1",
			ProjectItemID:    itemID,
			SourceDigest:     sourceDigest,
			SpecDigest:       approved,
			CommentID:        int64(number),
			CommentAuthorID:  "U_1",
			CommentCreatedAt: when.Add(-4 * time.Minute),
			CommentUpdatedAt: when.Add(-4 * time.Minute),
			ReviewOptionID:   "review-option",
			ReviewUpdatedAt:  when.Add(-3 * time.Minute),
			ApprovedDigest:   approved,
			BacklogOptionID:  "backlog-option",
			BacklogUpdatedAt: when.Add(-2 * time.Minute),
		}
		ledger.Projections[issueID] = state.BoardProjection{
			Repository:    "owner/repo",
			IssueID:       issueID,
			ProjectID:     "P_1",
			ProjectItemID: itemID,
			Stage:         "verification",
			OptionID:      "verification-option",
			UpdatedAt:     when.Add(2 * time.Minute),
		}
		issues = append(issues, admission.Snapshot{
			Repository:      "owner/repo",
			RepositoryID:    "R_1",
			IssueID:         issueID,
			Number:          number,
			Title:           "Idea",
			Body:            "Investigate behavior",
			Open:            true,
			ProjectID:       "P_1",
			ProjectPrivate:  true,
			ProjectItemID:   itemID,
			CurrentStatus:   "Verification",
			StatusOptionID:  "verification-option",
			StatusUpdatedAt: when.Add(2 * time.Minute),
			Complete:        true,
		})
		evidence[issueID] = DeliveryEvidence{
			PRNumber:      int64(number),
			PRURL:         url,
			HeadSHA:       head,
			BaseSHA:       baseSHA,
			RequiredGates: []string{"quality"},
			Gates: []GateEvidence{{
				ID:           "quality",
				CandidateSHA: head,
				BaseSHA:      baseSHA,
				Outcome:      GatePassed,
			}},
		}
		authority[issueID] = true
	}
	if err := ledger.Validate(); err != nil {
		t.Fatalf("invalid scan fixture ledger: %v", err)
	}
	return ScanInput{
		Repository: "owner/repo",
		ProjectID:  "P_1",
		Statuses: Statuses{
			Inbox:        "Inbox",
			Discovery:    "Discovery",
			SpecReview:   "Spec Review",
			Backlog:      "Backlog",
			Ready:        "Ready",
			Building:     "Building",
			Verification: "Verification",
			Review:       "Review",
			Release:      "Release",
			Done:         "Done",
		},
		Issues:    issues,
		Ledger:    ledger,
		Authority: authority,
		Evidence:  evidence,
	}
}

func TestScanKeepsPRResultsAndResourcesSeparate(t *testing.T) {
	input := scanFixture(t)
	first := input.Evidence["I_1"]
	first.Gates = nil // First PR's evidence is missing.
	input.Evidence["I_1"] = first
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason == "" || effects[1].Kind != Move || effects[1].To != Review {
		t.Fatalf("one PR's failure interfered with the other: %+v", effects)
	}
	if fence := effects[1].PRFence; fence == nil || fence.Number != 2 || fence.URL != input.Evidence["I_2"].PRURL || fence.HeadSHA != input.Evidence["I_2"].HeadSHA || fence.BaseSHA != input.Evidence["I_2"].BaseSHA {
		t.Fatalf("move lost its exact PR identity: %+v", fence)
	}
	input.Evidence["I_1"] = input.Evidence["I_2"] // Swapped PR identity cannot borrow success.
	effects, err = Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if effects[0].Kind != Hold || effects[0].BlockedReason != "current PR identity unavailable" || effects[1].Kind != Move {
		t.Fatalf("cross-PR evidence was accepted or contaminated: %+v", effects)
	}
}

func readyScanFixture(t *testing.T) ScanInput {
	t.Helper()
	input := scanFixture(t)
	issue := input.Issues[0]
	issue.CurrentStatus = "Ready"
	issue.StatusOptionID = "ready-option"
	input.Issues[0] = issue
	prior := input.Ledger.Projections[issue.IssueID]
	prior.Stage = "ready"
	prior.OptionID = issue.StatusOptionID
	prior.UpdatedAt = issue.StatusUpdatedAt
	input.Ledger.Projections[issue.IssueID] = prior
	return input
}

func TestScanLeavesApprovedReadyWithoutAttemptEligibleForDelivery(t *testing.T) {
	input := readyScanFixture(t)
	for id, attempt := range input.Ledger.Attempts {
		if attempt.Admission.Issue == 1 {
			delete(input.Ledger.Attempts, id)
		}
	}
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason != "" || effects[1].Kind != Move || effects[1].To != Review {
		t.Fatalf("approved Ready item was held before admission or affected another issue: %+v", effects)
	}

	input.Authority["I_1"] = false
	effects, err = Scan(input)
	if err != nil || effects[0].BlockedReason != "approved scope unavailable or changed" || effects[1].Kind != Move {
		t.Fatalf("unapproved Ready item became eligible: %+v, %v", effects, err)
	}
	input.Authority["I_1"] = true
	input.Issues[0].Body = "Changed acceptance criteria"
	effects, err = Scan(input)
	if err != nil || effects[0].BlockedReason != "source idea changed after approval" || effects[1].Kind != Move {
		t.Fatalf("changed source became eligible: %+v, %v", effects, err)
	}
}

func TestScanLeavesOnlyExactOwnerlessReadyRecoveryEligible(t *testing.T) {
	input := readyScanFixture(t)
	var attemptID string
	for id, attempt := range input.Ledger.Attempts {
		if attempt.Admission.Issue == 1 {
			attemptID = id
			attempt.Phase = state.Pending
			attempt.Dispatch = "pending"
			attempt.Publication = nil
			input.Ledger.Attempts[id] = attempt
			input.Issues[0].StatusUpdatedAt = attempt.Admission.StatusUpdatedAt
			prior := input.Ledger.Projections["I_1"]
			prior.UpdatedAt = attempt.Admission.StatusUpdatedAt
			input.Ledger.Projections["I_1"] = prior
		}
	}
	if attemptID == "" {
		t.Fatal("Ready attempt missing")
	}
	effects, err := Scan(input)
	if err != nil || len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason != "" || effects[1].Kind != Move {
		t.Fatalf("ownerless Pending recovery was held or affected another issue: %+v, %v", effects, err)
	}

	input.Issues[0].StatusUpdatedAt = input.Issues[0].StatusUpdatedAt.Add(time.Minute)
	prior := input.Ledger.Projections["I_1"]
	prior.UpdatedAt = input.Issues[0].StatusUpdatedAt
	input.Ledger.Projections["I_1"] = prior
	effects, err = Scan(input)
	if err != nil || effects[0].BlockedReason != "approved scope unavailable or changed" || effects[1].Kind != Move {
		t.Fatalf("changed Ready revision reused pending attempt: %+v, %v", effects, err)
	}
}

func TestScanRetriesPendingMoveOnlyWithCurrentEvidence(t *testing.T) {
	input := scanFixture(t)
	prior := input.Ledger.Projections["I_1"]
	prior.PendingStage = "review"
	prior.PendingOptionID = "review-option"
	prior.PendingFromOptionID = prior.OptionID
	prior.PendingFromUpdatedAt = prior.UpdatedAt
	input.Ledger.Projections["I_1"] = prior
	effects, err := Scan(input)
	if err != nil || effects[0].Kind != Move || effects[0].To != Review || !effects[0].RetryIntent {
		t.Fatalf("current pending move did not retry: %+v, %v", effects, err)
	}
	first := input.Evidence["I_1"]
	first.Gates = nil
	input.Evidence["I_1"] = first
	effects, err = Scan(input)
	if err != nil || effects[0].Kind != Hold || effects[0].RetryIntent || !effects[0].CancelIntent || effects[0].BlockedReason == "" || effects[1].Kind != Move {
		t.Fatalf("stale evidence retried or affected another item: %+v, %v", effects, err)
	}
}

func TestScanHoldsPendingBuildingMoveAfterPRChanges(t *testing.T) {
	input := scanFixture(t)
	input.Issues[0].CurrentStatus = "Building"
	input.Issues[0].StatusOptionID = "building-option"
	prior := input.Ledger.Projections["I_1"]
	prior.Stage = "building"
	prior.OptionID = "building-option"
	prior.PendingStage = "verification"
	prior.PendingOptionID = "verification-option"
	prior.PendingFromOptionID = prior.OptionID
	prior.PendingFromUpdatedAt = prior.UpdatedAt
	input.Ledger.Projections["I_1"] = prior
	changed := input.Evidence["I_1"]
	changed.HeadSHA = strings.Repeat("d", 40)
	input.Evidence["I_1"] = changed

	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if effects[0].Kind != Hold || effects[0].BlockedReason != "pending transition no longer justified" || !effects[0].Conflict || !effects[0].CancelIntent || effects[1].Kind != Move {
		t.Fatalf("changed PR retried a stale move or blocked another item: %+v", effects)
	}
}

func TestScanDefersManualBoardChangeAndChangedBase(t *testing.T) {
	input := scanFixture(t)
	input.Issues[0].CurrentStatus = "Building"
	input.Issues[0].StatusOptionID = "building-option"
	input.Issues[0].StatusUpdatedAt = input.Issues[0].StatusUpdatedAt.Add(time.Minute)
	second := input.Evidence["I_2"]
	second.BaseSHA = strings.Repeat("d", 40)
	input.Evidence["I_2"] = second
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if effects[0].Kind != Observe || effects[1].Kind != Hold || effects[1].BlockedReason == "" {
		t.Fatalf("manual or stale base outcome was not deferred: %+v", effects)
	}
}

func TestScanRejectsChangedSourceEvenWhenPRGatesPassed(t *testing.T) {
	input := scanFixture(t)
	input.Issues[0].Body = "New acceptance requirement"
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if effects[0].Kind != Hold || effects[0].BlockedReason != "source idea changed after approval" || effects[1].Kind != Move {
		t.Fatalf("changed source borrowed stale approval or blocked other PR: %+v", effects)
	}
}

func TestScanHoldsAmbiguousAttemptWithoutBorrowingAnotherPR(t *testing.T) {
	input := scanFixture(t)
	var original state.Attempt
	for _, attempt := range input.Ledger.Attempts {
		if attempt.Admission.Issue == 1 {
			original = attempt
		}
	}
	if original.ID == "" {
		t.Fatal("fixture issue attempt missing")
	}
	duplicate := original
	duplicate.Admission.StatusOptionID = "new-ready-option"
	duplicate.ID = state.AttemptID(duplicate.Admission)
	input.Ledger.Attempts[duplicate.ID] = duplicate
	if err := input.Ledger.Validate(); err != nil {
		t.Fatalf("fixture invalid: %v", err)
	}
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason != "ambiguous attempts for Project item" || effects[1].Kind != Move || effects[1].To != Review {
		t.Fatalf("ambiguous attempt contaminated another item: %+v", effects)
	}
}

func TestScanReportsTerminalAttemptWithoutLeakingFailureOrBorrowingOtherPR(t *testing.T) {
	for _, phase := range []state.Phase{state.Blocked, state.Deferred} {
		t.Run(string(phase), func(t *testing.T) {
			input := scanFixture(t)
			for id, attempt := range input.Ledger.Attempts {
				if attempt.Admission.Issue != 1 {
					continue
				}
				attempt.Phase = phase
				attempt.Failure = "TOP_SECRET untrusted provider failure"
				input.Ledger.Attempts[id] = attempt
			}
			effects, err := Scan(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason != string(phase) || effects[0].NextAction != "inspect bounded delivery outcome" || effects[1].Kind != Move || effects[1].To != Review {
				t.Fatalf("terminal result crossed issue boundary: %+v", effects)
			}
			if strings.Contains(effects[0].BlockedReason+effects[0].NextAction, "TOP_SECRET") {
				t.Fatalf("raw failure escaped terminal hold: %+v", effects[0])
			}
		})
	}
}

func TestScanDoesNotPresentSupersededAttemptAsCurrentTerminalOutcome(t *testing.T) {
	input := scanFixture(t)
	for id, attempt := range input.Ledger.Attempts {
		if attempt.Admission.Issue != 1 {
			continue
		}
		attempt.Phase = state.Blocked
		attempt.Failure = "human-change"
		attempt.SupersededAt = attempt.UpdatedAt.Add(time.Minute)
		attempt.SupersededSourceDigest = strings.Repeat("e", 64)
		input.Ledger.Attempts[id] = attempt
	}
	effects, err := Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Kind != Hold || effects[0].BlockedReason != "delivery attempt unavailable" || effects[1].Kind != Move || effects[1].To != Review {
		t.Fatalf("superseded history impersonated a current attempt: %+v", effects)
	}
}
