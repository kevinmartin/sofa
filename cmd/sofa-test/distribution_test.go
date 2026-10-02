package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/state"
)

func TestCompatibilityFixtureSurvivesFileHandoff(t *testing.T) {
	first := filepath.Join(t.TempDir(), "before.json")
	second := filepath.Join(t.TempDir(), "after.json")
	for _, args := range [][]string{
		{"distribution", "compatibility-fixture", "--output", first},
		{"distribution", "compatibility-fixture", "--input", first, "--output", second},
	} {
		cmd := newRootCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("compatibility fixture %v: %v", args, err)
		}
	}
	before, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("round-trip changed fixture bytes across CLI invocations")
	}
	var fixture compatibilityFixture
	if err := json.Unmarshal(after, &fixture); err != nil {
		t.Fatal(err)
	}
	ledger, err := state.Decode(fixture.Ledger)
	if err != nil || len(ledger.Attempts) != 1 {
		t.Fatalf("round-trip lost valid durable state: %v", err)
	}
}

func TestCompatibilityFixtureReencodesLedgerInsteadOfEchoingInput(t *testing.T) {
	fixture, err := newCompatibilityFixture()
	if err != nil {
		t.Fatal(err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, fixture.Ledger, "", "  "); err != nil {
		t.Fatal(err)
	}
	fixture.Ledger = indented.Bytes()
	input := filepath.Join(t.TempDir(), "input.json")
	output := filepath.Join(t.TempDir(), "output.json")
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"distribution", "compatibility-fixture", "--input", input, "--output", output})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip compatibilityFixture
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(roundTrip.Ledger, fixture.Ledger) || bytes.Contains(roundTrip.Ledger, []byte("\n")) {
		t.Fatal("ledger was copied without a decode/encode round-trip")
	}
}

func TestCompatibilityFixtureRejectsDroppedCounterAndUnknownConfig(t *testing.T) {
	original, err := newCompatibilityFixture()
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := state.Decode(original.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	for id, attempt := range ledger.Attempts {
		attempt.Counts.ModelCalls = 0
		ledger.Attempts[id] = attempt
	}
	dropped, err := state.Encode(ledger)
	if err != nil {
		t.Fatal(err)
	}
	checkpointChanged, err := state.Decode(original.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	for id, attempt := range checkpointChanged.Attempts {
		copy := *attempt.Checkpoint
		copy.CandidateSHA = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
		attempt.Checkpoint = &copy
		checkpointChanged.Attempts[id] = attempt
	}
	changedCheckpoint, err := state.Encode(checkpointChanged)
	if err != nil {
		t.Fatal(err)
	}
	changedOwner, err := state.Decode(original.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	for id, attempt := range changedOwner.Attempts {
		owner := *attempt.Owner
		owner.RunAttempt++
		attempt.Owner = &owner
		checkpoint := *attempt.Checkpoint
		checkpoint.Producer = owner
		attempt.Checkpoint = &checkpoint
		changedOwner.Attempts[id] = attempt
	}
	ownerChanged, err := state.Encode(changedOwner)
	if err != nil {
		t.Fatal(err)
	}
	changedBudget, err := state.Decode(original.Ledger)
	if err != nil {
		t.Fatal(err)
	}
	for id, attempt := range changedBudget.Attempts {
		attempt.Limits.ModelCalls++
		changedBudget.Attempts[id] = attempt
	}
	budgetChanged, err := state.Encode(changedBudget)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		fixture compatibilityFixture
	}{
		{
			name: "dropped cumulative counter",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    original.ConfigYAML,
				Ledger:        dropped,
			},
		},
		{
			name: "unknown configuration policy",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    original.ConfigYAML + "unsupported_policy: true\n",
				Ledger:        original.Ledger,
			},
		},
		{
			name: "changed candidate checkpoint",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    original.ConfigYAML,
				Ledger:        changedCheckpoint,
			},
		},
		{
			name: "changed attempt owner",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    original.ConfigYAML,
				Ledger:        ownerChanged,
			},
		},
		{
			name: "expanded model budget",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    original.ConfigYAML,
				Ledger:        budgetChanged,
			},
		},
		{
			name: "changed allowed path policy",
			fixture: compatibilityFixture{
				SchemaVersion: original.SchemaVersion,
				ConfigYAML:    strings.Replace(original.ConfigYAML, "fixture/greeting.go", "fixture/other.go", 1),
				Ledger:        original.Ledger,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "bad.json")
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := newRootCommand()
			cmd.SetArgs([]string{"distribution", "compatibility-fixture", "--input", path})
			if err := cmd.Execute(); err == nil {
				t.Fatal("incompatible fixture was accepted")
			}
		})
	}
}
