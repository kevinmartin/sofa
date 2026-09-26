package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/kevinmartin/sofa/internal/admission"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/integrity"
	"github.com/kevinmartin/sofa/internal/state"
)

// Manifest is emitted by the privileged admission job, then treated as input
// data by each later stage. Publication checks it against live source and state.
type Manifest struct {
	Version            int                `json:"version" validate:"eq=1"`
	Grant              admission.Grant    `json:"grant" validate:"required"`
	Fence              state.Fence        `json:"fence" validate:"required"`
	CanonicalSpec      json.RawMessage    `json:"canonical_spec" validate:"required"`
	Recovery           *state.Publication `json:"recovery_publication,omitempty"`
	RecoveryCheckpoint *state.Checkpoint  `json:"recovery_checkpoint,omitempty"`
	RecoverySource     *state.Owner       `json:"recovery_source,omitempty"`
}

var artifactValidator = validator.New(validator.WithRequiredStructEnabled())

// Static field rules live on the decoded types. Values tied to the current
// configuration and canonical specification are checked as pairs below.
type manifestBinding struct {
	Repository           string `validate:"eqfield=ExpectedRepository"`
	ExpectedRepository   string `validate:"-"`
	RepositoryID         string `validate:"eqfield=ExpectedRepositoryID"`
	ExpectedRepositoryID string `validate:"-"`
	ProjectID            string `validate:"eqfield=ExpectedProjectID"`
	ExpectedProjectID    string `validate:"-"`
	OwnerID              string `validate:"eqfield=ExpectedOwnerID"`
	ExpectedOwnerID      string `validate:"-"`
	ConfigDigest         string `validate:"eqfield=ExpectedConfigDigest"`
	ExpectedConfigDigest string `validate:"-"`
	SpecDigest           string `validate:"eqfield=ExpectedSpecDigest"`
	ExpectedSpecDigest   string `validate:"-"`
}

func readConfig(name string) (config.Config, error) {
	f, err := os.Open(name)
	if err != nil {
		return config.Config{}, errors.New("cannot read configuration")
	}
	defer f.Close()
	return config.Decode(f)
}

func writeJSON(name string, value any) error {
	if name == "" {
		return errors.New("output path required")
	}
	// Raw canonical spec bytes are part of the admitted digest. Reformatting
	// nested JSON while serializing a manifest would silently change identity.
	b, err := json.Marshal(value)
	if err != nil {
		return errors.New("cannot encode output")
	}
	b = append(b, '\n')
	if err := os.WriteFile(name, b, 0600); err != nil {
		return errors.New("cannot write output")
	}
	return nil
}

func readJSON(name string, max int64, value any) error {
	f, err := os.Open(name)
	if err != nil {
		return errors.New("cannot read input artifact")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return errors.New("input artifact unavailable or oversized")
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return errors.New("invalid input artifact")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing input artifact data")
	}
	return nil
}

func readManifest(name string, c config.Config) (Manifest, error) {
	var m Manifest
	if err := readJSON(name, 128<<10, &m); err != nil {
		return m, err
	}
	digest, err := c.Digest()
	if err != nil {
		return m, err
	}
	h := sha256.Sum256(m.CanonicalSpec)
	if err := artifactValidator.Struct(m); err != nil {
		return Manifest{}, errors.New("manifest identity does not match configuration")
	}
	binding := manifestBinding{
		Repository:           m.Grant.Repository,
		ExpectedRepository:   c.Repository,
		RepositoryID:         m.Grant.RepositoryID,
		ExpectedRepositoryID: c.RepositoryID,
		ProjectID:            m.Grant.ProjectID,
		ExpectedProjectID:    c.ProjectID,
		OwnerID:              m.Grant.OwnerID,
		ExpectedOwnerID:      c.OwnerID,
		ConfigDigest:         m.Grant.ConfigDigest,
		ExpectedConfigDigest: digest,
		SpecDigest:           m.Grant.SpecDigest,
		ExpectedSpecDigest:   hex.EncodeToString(h[:]),
	}
	if err := artifactValidator.Struct(binding); err != nil {
		return Manifest{}, errors.New("manifest identity does not match configuration")
	}
	a := ledgerAdmission(m.Grant)
	if err := a.Validate(); err != nil || state.AttemptID(a) != m.Fence.AttemptID {
		return Manifest{}, errors.New("manifest admission identity invalid")
	}
	if (m.Recovery == nil && m.RecoveryCheckpoint == nil) != (m.RecoverySource == nil) || (m.Recovery != nil && m.RecoveryCheckpoint != nil) {
		return Manifest{}, errors.New("incomplete candidate recovery manifest")
	}
	return m, nil
}

func ledgerAdmission(g admission.Grant) state.Admission {
	return state.Admission{
		Repository:      strings.ToLower(g.Repository),
		Issue:           int64(g.IssueNumber),
		SpecDigest:      g.SpecDigest,
		ConfigDigest:    g.ConfigDigest,
		BaseSHA:         g.BaseSHA,
		ProjectID:       g.ProjectID,
		ProjectItemID:   g.ProjectItemID,
		StatusOptionID:  g.StatusOptionID,
		StatusUpdatedAt: g.StatusUpdatedAt,
	}
}

func readBundle(name string) (integrity.Bundle, error) {
	f, err := os.Open(name)
	if err != nil {
		return integrity.Bundle{}, errors.New("cannot read candidate bundle")
	}
	defer f.Close()
	return integrity.Decode(f, integrity.MaxEncodedBytes)
}

func bundlePolicy(c config.Config, forbidden ...[]byte) integrity.Policy {
	return integrity.Policy{
		AllowedPaths:    c.AllowedPaths,
		MaxFiles:        c.Limits.MaxFiles,
		MaxFileBytes:    c.Limits.MaxFileBytes,
		MaxTotalBytes:   c.Limits.MaxTotalBytes,
		ForbiddenValues: forbidden,
	}
}

func bundleExpected(m Manifest, b integrity.Bundle) integrity.Expected {
	generation := uint64(m.Fence.Generation)
	if m.Recovery != nil && b.Generation < generation && b.CandidateDigest == m.Recovery.CandidateDigest {
		generation = b.Generation
	}
	if m.RecoveryCheckpoint != nil && b.Generation == uint64(m.RecoveryCheckpoint.Generation) && b.Generation < generation && b.CandidateDigest == m.RecoveryCheckpoint.CandidateSHA {
		generation = b.Generation
	}
	return integrity.Expected{
		Repository:      m.Grant.Repository,
		AttemptID:       m.Fence.AttemptID,
		Generation:      generation,
		BaseSHA:         m.Grant.BaseSHA,
		CandidateDigest: b.CandidateDigest,
	}
}
