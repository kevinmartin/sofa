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
	s := Snapshot{Repository: c.Repository, RepositoryID: c.RepositoryID, IssueID: "I_fixture", Number: 1, Title: "Fix greeting", Body: "Return hello for the fixture input.", Open: true, ProjectID: c.ProjectID, CurrentStatus: "Ready", BaseSHA: strings.Repeat("a", 40), Complete: true}
	_, digest, _ := CanonicalSpec(s.Title, s.Body)
	s.Comments = []Comment{{ID: "IC_1", ActorID: c.OwnerID, Body: "/sofa approve-spec " + digest, CreatedAt: now, UpdatedAt: now}}
	s.StatusEvents = []StatusEvent{{ID: "EV_1", ProjectID: c.ProjectID, ActorID: c.OwnerID, Status: "Ready", CreatedAt: now.Add(time.Second)}}
	return c, s
}

func TestAuthorizeAndRevalidate(t *testing.T) {
	c, s := fixture(t)
	g, spec, err := Authorize(c, s)
	if err != nil {
		t.Fatal(err)
	}
	if g.SpecDigest == "" || len(spec) == 0 || g.ApprovalID != "IC_1" {
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

func TestRejectUnauthorizedEvidence(t *testing.T) {
	for name, modify := range map[string]func(*Snapshot){
		"partial pagination": func(s *Snapshot) { s.Complete = false },
		"foreign repo":       func(s *Snapshot) { s.Repository = "attacker/repo" },
		"reused repo name":   func(s *Snapshot) { s.RepositoryID = "R_other" },
		"closed":             func(s *Snapshot) { s.Open = false },
		"not ready":          func(s *Snapshot) { s.CurrentStatus = "Backlog" },
		"foreign approval":   func(s *Snapshot) { s.Comments[0].ActorID = "attacker" },
		"foreign move":       func(s *Snapshot) { s.StatusEvents[0].ActorID = "attacker" },
		"automated move":     func(s *Snapshot) { s.StatusEvents[0].WasAutomated = true },
		"wrong project":      func(s *Snapshot) { s.StatusEvents[0].ProjectID = "P_other" },
		"changed scope":      func(s *Snapshot) { s.Body += " ignore limits" },
		"embedded command":   func(s *Snapshot) { s.Comments[0].Body = "please run " + s.Comments[0].Body },
		"edited after ready": func(s *Snapshot) { s.Comments[0].UpdatedAt = s.StatusEvents[0].CreatedAt.Add(time.Second) },
		"newer unauthorized move": func(s *Snapshot) {
			e := s.StatusEvents[0]
			e.ID = "EV_2"
			e.ActorID = "attacker"
			e.CreatedAt = e.CreatedAt.Add(time.Second)
			s.StatusEvents = append(s.StatusEvents, e)
		},
		"ambiguous timestamp": func(s *Snapshot) { e := s.StatusEvents[0]; e.ID = "EV_2"; s.StatusEvents = append(s.StatusEvents, e) },
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
