package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/state"
)

const compatibilityConfig = `schema_version: 1
repository: owner/consumer
repository_id: R_fixture
project_id: P_fixture
owner_id: U_fixture
ready_status: Ready
allowed_paths:
  - fixture/greeting.go
profile:
  agent: copilot
  secret_env: SOFA_MODEL_TOKEN
limits:
  attempt_seconds: 600
  repair_attempts: 2
  infra_retries: 1
  max_agent_turns: 5
  max_files: 5
  max_file_bytes: 131072
  max_total_bytes: 524288
checks:
  - id: go-test
    argv: [go, test, ./...]
    timeout_seconds: 120
lifecycle:
  statuses:
    inbox: Inbox
    discovery: Discovery
    spec_review: Spec Review
    backlog: Backlog
    ready: Ready
    building: Building
    verification: Verification
    review: Review
    release: Release
    done: Done
`

type compatibilityFixture struct {
	SchemaVersion int             `json:"schema_version"`
	ConfigYAML    string          `json:"config_yaml"`
	Ledger        json.RawMessage `json:"ledger"`
}

func newDistributionCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "distribution",
		Short: "Exercise deterministic CLI distribution compatibility",
		Args:  cobra.NoArgs,
	}
	var input, output string
	fixture := &cobra.Command{
		Use:   "compatibility-fixture",
		Short: "Round-trip synthetic configuration and durable state across CLI versions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var artifact compatibilityFixture
			var err error
			if input == "" {
				artifact, err = newCompatibilityFixture()
			} else {
				artifact, err = readCompatibilityFixture(input)
			}
			if err != nil {
				return err
			}
			artifact, err = normalizeCompatibilityFixture(artifact)
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(artifact)
			if err != nil {
				return errors.New("encode compatibility fixture")
			}
			encoded = append(encoded, '\n')
			if output != "" {
				if err := os.WriteFile(output, encoded, 0600); err != nil {
					return errors.New("write compatibility fixture")
				}
				return nil
			}
			_, err = cmd.OutOrStdout().Write(encoded)
			return err
		},
	}
	fixture.Flags().StringVar(&input, "input", "", "Prior-version fixture file to decode and round-trip")
	fixture.Flags().StringVar(&output, "output", "", "Write the verified fixture to this file instead of stdout")
	cmd.AddCommand(fixture)
	return cmd
}

//go:embed testdata/compatibility-v0.json
var compatibilityBaseline []byte

func newCompatibilityFixture() (compatibilityFixture, error) {
	return decodeCompatibilityFixture(compatibilityBaseline)
}

func readCompatibilityFixture(path string) (compatibilityFixture, error) {
	file, err := os.Open(path)
	if err != nil {
		return compatibilityFixture{}, errors.New("read compatibility fixture")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return compatibilityFixture{}, errors.New("read compatibility fixture")
	}
	return decodeCompatibilityFixture(data)
}

func decodeCompatibilityFixture(data []byte) (compatibilityFixture, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var fixture compatibilityFixture
	if err := decoder.Decode(&fixture); err != nil {
		return compatibilityFixture{}, errors.New("invalid compatibility fixture")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return compatibilityFixture{}, errors.New("invalid compatibility fixture")
	}
	return fixture, nil
}

// normalizeCompatibilityFixture decodes and re-encodes the ledger so a CLI
// cannot pass an upgrade/rollback handoff by merely copying opaque JSON.
func normalizeCompatibilityFixture(fixture compatibilityFixture) (compatibilityFixture, error) {
	if fixture.SchemaVersion != 1 || len(fixture.ConfigYAML) == 0 || len(fixture.ConfigYAML) > config.MaxBytes {
		return compatibilityFixture{}, errors.New("unsupported compatibility fixture")
	}
	c, err := config.Decode(strings.NewReader(fixture.ConfigYAML))
	if err != nil {
		return compatibilityFixture{}, errors.New("incompatible fixture configuration")
	}
	expected, err := config.Decode(strings.NewReader(compatibilityConfig))
	if err != nil || !reflect.DeepEqual(c, expected) {
		return compatibilityFixture{}, errors.New("fixture configuration policy changed")
	}
	configDigest, err := c.Digest()
	if err != nil {
		return compatibilityFixture{}, errors.New("fixture configuration digest unavailable")
	}
	ledger, err := state.Decode(fixture.Ledger)
	if err != nil || len(ledger.Attempts) != 1 {
		return compatibilityFixture{}, errors.New("incompatible fixture ledger")
	}
	for _, attempt := range ledger.Attempts {
		if err := checkCompatibilityAttempt(attempt, c, configDigest); err != nil {
			return compatibilityFixture{}, err
		}
	}
	encoded, err := state.Encode(ledger)
	if err != nil {
		return compatibilityFixture{}, errors.New("re-encode compatibility ledger")
	}
	fixture.Ledger = encoded
	return fixture, nil
}

func checkCompatibilityAttempt(attempt state.Attempt, c config.Config, configDigest string) error {
	when := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	expectedAdmission := state.Admission{
		Repository:      c.Repository,
		Issue:           7,
		ConfigDigest:    configDigest,
		SpecDigest:      strings.Repeat("a", 64),
		BaseSHA:         strings.Repeat("b", 40),
		ProjectID:       c.ProjectID,
		ProjectItemID:   "item-fixture-7",
		StatusOptionID:  "ready-fixture",
		StatusUpdatedAt: when,
	}
	admission := attempt.Admission
	admission.StatusUpdatedAt = admission.StatusUpdatedAt.UTC()
	if admission != expectedAdmission {
		return errors.New("fixture lost admitted scope or owner")
	}
	expectedOwner := state.Owner{
		RunID:      "fixture-run-42",
		RunAttempt: 2,
	}
	if attempt.Owner == nil || *attempt.Owner != expectedOwner {
		return errors.New("fixture lost admitted scope or owner")
	}
	if attempt.Phase != state.Validating || attempt.Generation != 3 || attempt.Dispatch != "claimed" {
		return errors.New("fixture lost admitted scope or owner")
	}
	expectedLimits := state.Limits{
		ModelCalls:            5,
		Repairs:               2,
		InfrastructureRetries: 1,
		RuntimeSeconds:        600,
	}
	expectedCounts := state.Counters{
		ModelCalls:            2,
		Repairs:               1,
		InfrastructureRetries: 1,
		RuntimeSeconds:        120,
	}
	if attempt.Limits != expectedLimits || attempt.Counts != expectedCounts {
		return errors.New("fixture changed cumulative work budget")
	}
	if attempt.Checkpoint == nil {
		return errors.New("fixture lost checkpoint evidence or ownership fence")
	}
	expectedCheckpoint := state.Checkpoint{
		Version:      state.Version,
		Phase:        state.Validating,
		ArtifactID:   "fixture-artifact-7",
		Digest:       strings.Repeat("c", 64),
		CandidateSHA: strings.Repeat("d", 40),
		Generation:   3,
		Producer:     expectedOwner,
		AcceptedAt:   when.Add(time.Minute),
		ExpiresAt:    when.Add(time.Hour),
	}
	checkpoint := *attempt.Checkpoint
	checkpoint.AcceptedAt = checkpoint.AcceptedAt.UTC()
	checkpoint.ExpiresAt = checkpoint.ExpiresAt.UTC()
	if checkpoint != expectedCheckpoint {
		return errors.New("fixture lost checkpoint evidence or ownership fence")
	}
	return nil
}
