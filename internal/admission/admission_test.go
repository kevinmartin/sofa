package admission

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/config"
)

func fixture(t *testing.T) (config.Config, Snapshot) {
	t.Helper()
	b, err := os.ReadFile("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Decode(strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s := Snapshot{Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_fixture", Number: 1, Title: "Fix greeting", Body: "Return hello for the fixture input.", Open: true, ProjectID: c.ProjectID, ProjectPrivate: true, ProjectItemID: "PVTI_1", CurrentStatus: "Ready", StatusOptionID: "ready-option", StatusUpdatedAt: now.Add(time.Second), BaseSHA: strings.Repeat("a", 40), Complete: true}
	return c, s
}

func TestAuthorizeAndRevalidate(t *testing.T) {
	c, s := fixture(t)
	g, spec, err := Authorize(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if g.SpecDigest == "" || len(spec) == 0 || g.ProjectItemID != "PVTI_1" || g.StatusUpdatedAt.IsZero() {
		t.Fatal("incomplete grant")
	}
	s.BaseSHA = strings.Repeat("b", 40)
	if err := Revalidate(c, s, g); err != nil {
		t.Fatalf("base movement should not silently readmit: %v", err)
	}
	s.Body += " Expand scope."
	if err := Revalidate(c, s, g); err == nil {
		t.Fatal("accepted changed spec")
	}
}

func TestAuthorizePreservesConfiguredRepositoryCasing(t *testing.T) {
	c, s := fixture(t)
	s.Repository = strings.ToUpper(c.Repository)
	g, _, err := Authorize(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if g.Repository != c.Repository {
		t.Fatalf("grant repository = %q, want %q", g.Repository, c.Repository)
	}
	if err := Revalidate(c, s, g); err != nil {
		t.Fatal(err)
	}
}

func TestRejectUnauthorizedEvidence(t *testing.T) {
	for name, modify := range map[string]func(*Snapshot){
		"partial pagination":      func(s *Snapshot) { s.Complete = false },
		"foreign repo":            func(s *Snapshot) { s.Repository = "attacker/repo" },
		"reused repo name":        func(s *Snapshot) { s.RepositoryID = "R_other" },
		"closed":                  func(s *Snapshot) { s.Open = false },
		"not ready":               func(s *Snapshot) { s.CurrentStatus = "Backlog" },
		"public project":          func(s *Snapshot) { s.ProjectPrivate = false },
		"wrong project":           func(s *Snapshot) { s.ProjectID = "P_other" },
		"missing item":            func(s *Snapshot) { s.ProjectItemID = "" },
		"missing status option":   func(s *Snapshot) { s.StatusOptionID = "" },
		"missing status revision": func(s *Snapshot) { s.StatusUpdatedAt = time.Time{} },
		"edited after ready":      func(s *Snapshot) { s.IssueLastEditedAt = s.StatusUpdatedAt.Add(time.Second) },
		"edited in ready second":  func(s *Snapshot) { s.IssueLastEditedAt = s.StatusUpdatedAt },
	} {
		t.Run(name, func(t *testing.T) {
			c, s := fixture(t)
			modify(&s)
			if _, _, err := Authorize(c, s); err == nil {
				t.Fatal("accepted unauthorized input")
			}
		})
	}
}

func TestReadyOnlyRevisionFence(t *testing.T) {
	c, s := fixture(t)
	g, _, err := Authorize(c, s)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Snapshot){
		"new status revision":  func(s *Snapshot) { s.StatusUpdatedAt = s.StatusUpdatedAt.Add(time.Second) },
		"new item":             func(s *Snapshot) { s.ProjectItemID = "PVTI_2" },
		"new option":           func(s *Snapshot) { s.StatusOptionID = "ready-option-2" },
		"changed body":         func(s *Snapshot) { s.Body += " More work." },
		"edited then restored": func(s *Snapshot) { s.IssueLastEditedAt = s.StatusUpdatedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := s
			mutate(&changed)
			if err := Revalidate(c, changed, g); err == nil {
				t.Fatal("accepted changed Ready grant")
			}
		})
	}
}

func TestCanonicalSpec(t *testing.T) {
	_, a, err := CanonicalSpec("Task", "a\r\nb\r\n")
	if err != nil {
		t.Fatal(err)
	}
	_, b, _ := CanonicalSpec(" Task ", "a\nb")
	if a != b {
		t.Fatal("line endings/boundary whitespace changed digest")
	}
	_, c, _ := CanonicalSpec("Task", "a\n b")
	if b == c {
		t.Fatal("meaningful body change not bound")
	}
}
