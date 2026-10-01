package state

import "strings"

// CurrentAttemptForIssue selects delivery evidence only for the latest
// Backlog-approved revision of this immutable issue. A superseded PR remains
// in history but cannot be borrowed for the revised specification.
// Missing approval or an absent attempt returns false without error. A current
// attempt with mismatched approval evidence, or multiple matches, returns ErrConflict.
func CurrentAttemptForIssue(s State, issueID string) (Attempt, bool, error) {
	record, ok := s.Specs[issueID]
	if !ok || record.ApprovedDigest == "" {
		return Attempt{}, false, nil
	}
	var current Attempt
	found := false
	for _, attempt := range s.Attempts {
		if !attempt.SupersededAt.IsZero() || !strings.EqualFold(attempt.Admission.Repository, record.Repository) || attempt.Admission.Issue != record.Issue {
			continue
		}
		if attempt.Admission.ProjectID != record.ProjectID || attempt.Admission.ProjectItemID != record.ProjectItemID || attempt.Admission.SpecDigest != record.ApprovedDigest || attempt.SpecRevision != record.Revision || !attempt.Admission.StatusUpdatedAt.After(record.BacklogUpdatedAt) {
			return Attempt{}, false, ErrConflict
		}
		if found {
			return Attempt{}, false, ErrConflict
		}
		current = attempt
		found = true
	}
	return current, found, nil
}
