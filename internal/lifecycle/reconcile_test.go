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
	input.Evidence["I_1"] = input.Evidence["I_2"] // Swapped PR identity cannot borrow success.
	effects, err = Scan(input)
	if err != nil {
		t.Fatal(err)
	}
	if effects[0].Kind != Hold || effects[0].BlockedReason != "current PR identity unavailable" || effects[1].Kind != Move {
		t.Fatalf("cross-PR evidence was accepted or contaminated: %+v", effects)
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
