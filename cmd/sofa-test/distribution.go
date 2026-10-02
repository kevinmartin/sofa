package main

import (
	"bytes"
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

func newCompatibilityFixture() (compatibilityFixture, error) {
	c, err := config.Decode(strings.NewReader(compatibilityConfig))
	if err != nil {
		return compatibilityFixture{}, err
	}
	configDigest, err := c.Digest()
	if err != nil {
		return compatibilityFixture{}, err
	}
	when := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	owner := state.Owner{
		RunID:      "fixture-run-42",
		RunAttempt: 2,
	}
	admission := state.Admission{
		Repository:      c.Repository,
		Issue:           7,
		SpecDigest:      strings.Repeat("a", 64),
		ConfigDigest:    configDigest,
		BaseSHA:         strings.Repeat("b", 40),
		ProjectID:       c.ProjectID,
		ProjectItemID:   "item-fixture-7",
		StatusOptionID:  "ready-fixture",
		StatusUpdatedAt: when,
	}
	attempt := state.Attempt{
		ID:         state.AttemptID(admission),
		Admission:  admission,
		Phase:      state.Validating,
		Generation: 3,
		Owner:      &owner,
		Dispatch:   "claimed",
		Limits: state.Limits{
			ModelCalls:            5,
			Repairs:               2,
			InfrastructureRetries: 1,
			RuntimeSeconds:        600,
		},
		Counts: state.Counters{
			ModelCalls:            2,
			Repairs:               1,
			InfrastructureRetries: 1,
			RuntimeSeconds:        120,
		},
		Checkpoint: &state.Checkpoint{
			Version:      state.Version,
			Phase:        state.Validating,
			ArtifactID:   "fixture-artifact-7",
			Digest:       strings.Repeat("c", 64),
			CandidateSHA: strings.Repeat("d", 40),
			Producer:     owner,
			Generation:   3,
			AcceptedAt:   when.Add(time.Minute),
			ExpiresAt:    when.Add(time.Hour),
		},
		CreatedAt: when,
		UpdatedAt: when.Add(2 * time.Minute),
	}
	ledger := state.Empty()
	ledger.Attempts[attempt.ID] = attempt
	encoded, err := state.Encode(ledger)
	if err != nil {
		return compatibilityFixture{}, err
	}
	return compatibilityFixture{
		SchemaVersion: 1,
		ConfigYAML:    compatibilityConfig,
		Ledger:        encoded,
	}, nil
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
	when := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	for _, attempt := range ledger.Attempts {
		if attempt.Admission.Repository != c.Repository || attempt.Admission.Issue != 7 || attempt.Admission.ConfigDigest != configDigest || attempt.Admission.SpecDigest != strings.Repeat("a", 64) || attempt.Admission.BaseSHA != strings.Repeat("b", 40) || attempt.Admission.ProjectID != c.ProjectID || attempt.Admission.ProjectItemID != "item-fixture-7" || attempt.Admission.StatusOptionID != "ready-fixture" || !attempt.Admission.StatusUpdatedAt.Equal(when) || attempt.Phase != state.Validating || attempt.Generation != 3 || attempt.Dispatch != "claimed" || attempt.Owner == nil || attempt.Owner.RunID != "fixture-run-42" || attempt.Owner.RunAttempt != 2 {
			return compatibilityFixture{}, errors.New("fixture lost admitted scope or owner")
		}
		if attempt.Limits.ModelCalls != 5 || attempt.Limits.Repairs != 2 || attempt.Limits.InfrastructureRetries != 1 || attempt.Limits.RuntimeSeconds != 600 || attempt.Counts.ModelCalls != 2 || attempt.Counts.Repairs != 1 || attempt.Counts.InfrastructureRetries != 1 || attempt.Counts.RuntimeSeconds != 120 {
			return compatibilityFixture{}, errors.New("fixture changed cumulative work budget")
		}
		if attempt.Checkpoint == nil || attempt.Checkpoint.Version != state.Version || attempt.Checkpoint.Phase != state.Validating || attempt.Checkpoint.ArtifactID != "fixture-artifact-7" || attempt.Checkpoint.Digest != strings.Repeat("c", 64) || attempt.Checkpoint.CandidateSHA != strings.Repeat("d", 40) || attempt.Checkpoint.Generation != 3 || attempt.Checkpoint.Producer != *attempt.Owner || !attempt.Checkpoint.AcceptedAt.Equal(when.Add(time.Minute)) || !attempt.Checkpoint.ExpiresAt.Equal(when.Add(time.Hour)) {
			return compatibilityFixture{}, errors.New("fixture lost checkpoint evidence or ownership fence")
		}
	}
	encoded, err := state.Encode(ledger)
	if err != nil {
		return compatibilityFixture{}, errors.New("re-encode compatibility ledger")
	}
	fixture.Ledger = encoded
	return fixture, nil
}
