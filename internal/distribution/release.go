// Package distribution owns version allocation, publication recovery and promotion.
// It never opens factory state or resets consumer ownership/budgets.
package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/kevinmartin/sofa/internal/github"
)

const (
	Repository           = "kevinmartin/sofa"
	DisposableRepository = "kevinmartin/sofa-disposable"
	BundleName           = "sofa-linux-amd64.tar.gz"
	MetadataName         = "release.json"
)

var (
	versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	seriesPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	shaPattern     = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

type Config struct {
	Series string `json:"series" validate:"required"`
}

func (c Config) Validate() error {
	if validator.New().Struct(c) != nil || !seriesPattern.MatchString(c.Series) {
		return errors.New("invalid release series")
	}
	major := strings.Split(c.Series, ".")[0]
	if major != "0" && major != "1" {
		return errors.New("release automation supports only v0 and v1")
	}
	return nil
}

// VersionMajor validates the current supported release automation generations.
// Supporting a new stable major requires a reviewed activation path first.
func VersionMajor(version string) (string, error) {
	parts := versionPattern.FindStringSubmatch(version)
	if parts == nil {
		return "", errors.New("invalid release version")
	}
	if parts[1] != "0" && parts[1] != "1" {
		return "", errors.New("release automation supports only v0 and v1")
	}
	return "v" + parts[1], nil
}

type Plan struct {
	Version            string `json:"version" validate:"required"`
	SourceSHA          string `json:"source_sha" validate:"required"`
	Channel            string `json:"channel" validate:"required"`
	ExpectedChannelSHA string `json:"expected_channel_sha"`
}

type Metadata struct {
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
	SourceSHA     string `json:"source_sha"`
	Platform      string `json:"platform"`
}

func (p Plan) Validate() error {
	if validator.New().Struct(p) != nil || !versionPattern.MatchString(p.Version) || !shaPattern.MatchString(p.SourceSHA) || (p.ExpectedChannelSHA != "" && !shaPattern.MatchString(p.ExpectedChannelSHA)) {
		return errors.New("invalid release plan")
	}
	major, err := VersionMajor(p.Version)
	if err != nil {
		return err
	}
	if p.Channel != major {
		return errors.New("release plan major mismatch")
	}
	return nil
}

func MetadataFor(p Plan) Metadata {
	return Metadata{
		SchemaVersion: 1,
		Version:       p.Version,
		SourceSHA:     p.SourceSHA,
		Platform:      "linux/amd64",
	}
}

// RollbackPlan is an explicit, read-only owner operation. It checks the requested
// immutable release and current channel without altering any consumer state.
// The caller must first exercise the target CLI against current compatibility
// fixtures; its fresh disposable canary is still required by Promote.
func RollbackPlan(ctx context.Context, api API, version, expected string) (Plan, error) {
	parts := versionPattern.FindStringSubmatch(version)
	if parts == nil || !shaPattern.MatchString(expected) {
		return Plan{}, errors.New("invalid rollback release or expected channel")
	}
	if _, err := VersionMajor(version); err != nil {
		return Plan{}, err
	}
	record, found, err := api.ReleaseRecordByTag(ctx, Repository, version)
	if err != nil {
		return Plan{}, err
	}
	if !found || record.Tag != version || record.Draft || record.Prerelease || !record.Immutable || !shaPattern.MatchString(record.Source) {
		return Plan{}, errors.New("rollback target is not an immutable exact release")
	}
	source, found, err := api.ReleaseTag(ctx, Repository, version)
	if err != nil {
		return Plan{}, err
	}
	if !found || source != record.Source {
		return Plan{}, errors.New("rollback exact release source differs")
	}
	channel := "v" + parts[1]
	current, found, err := api.ReleaseTag(ctx, Repository, channel)
	if err != nil {
		return Plan{}, err
	}
	if !found || current != expected {
		return Plan{}, errors.New("rollback channel changed before planning")
	}
	return Plan{
		Version:            version,
		SourceSHA:          source,
		Channel:            channel,
		ExpectedChannelSHA: expected,
	}, nil
}

type API interface {
	ReleasesPage(context.Context, string, int) ([]github.ReleaseRecord, error)
	ReleaseRecordByTag(context.Context, string, string) (github.ReleaseRecord, bool, error)
	CreateDraftRelease(context.Context, string, string, string, string) (github.ReleaseRecord, error)
	PublishDraftRelease(context.Context, string, int64) (github.ReleaseRecord, error)
	UploadReleaseAsset(context.Context, string, int64, string, []byte) (github.ReleaseAsset, error)
	DeleteEmptyDraftUpload(context.Context, string, github.ReleaseRecord, github.ReleaseAsset) error
	ImmutableReleasesEnabled(context.Context, string) (bool, error)
	ReleaseTag(context.Context, string, string) (string, bool, error)
	UpdateReleaseChannel(context.Context, string, string, string, string) error
}

// Allocate is read-only. The whole release workflow is serialized, including
// allocation and interrupted draft recovery, so retries cannot race versions.
func Allocate(ctx context.Context, api API, config Config, source string) (Plan, error) {
	if err := config.Validate(); err != nil {
		return Plan{}, err
	}
	if !shaPattern.MatchString(source) {
		return Plan{}, errors.New("invalid release series or source")
	}
	series := strings.Split(config.Series, ".")
	channel := "v" + series[0]
	prior, _, err := api.ReleaseTag(ctx, Repository, channel)
	if err != nil {
		return Plan{}, err
	}
	maxPatch := -1
	existing := ""
	var existingPlan Plan
	finished := false
	for page := 1; page <= 100; page++ {
		records, err := api.ReleasesPage(ctx, Repository, page)
		if err != nil {
			return Plan{}, err
		}
		for _, record := range records {
			parts := versionPattern.FindStringSubmatch(record.Tag)
			if parts == nil {
				continue
			}
			if record.Source == source {
				if parts[1] != series[0] || parts[2] != series[1] {
					return Plan{}, errors.New("source already released in another series")
				}
				if existing != "" && existing != record.Tag {
					return Plan{}, errors.New("duplicate release source identity")
				}
				existing = record.Tag
				if !strings.HasPrefix(record.Body, "sofa-release-plan:v1\n") || json.Unmarshal([]byte(strings.TrimPrefix(record.Body, "sofa-release-plan:v1\n")), &existingPlan) != nil || existingPlan.Validate() != nil || existingPlan.Version != record.Tag || existingPlan.SourceSHA != source {
					return Plan{}, errors.New("existing release has no recoverable promotion plan")
				}
			}
			if parts[1] != series[0] || parts[2] != series[1] {
				continue
			}
			patch, err := strconv.Atoi(parts[3])
			if err != nil || patch > 1_000_000 {
				return Plan{}, errors.New("release patch exceeds bound")
			}
			if patch > maxPatch {
				maxPatch = patch
			}
		}
		if len(records) < 100 {
			finished = true
			break
		}
	}
	if !finished || maxPatch >= 1_000_000 {
		return Plan{}, errors.New("release history exceeds allocation bound")
	}
	if existing != "" {
		return existingPlan, nil
	}
	existing = fmt.Sprintf("v%s.%d", config.Series, maxPatch+1)
	return Plan{
		Version:            existing,
		SourceSHA:          source,
		Channel:            channel,
		ExpectedChannelSHA: prior,
	}, nil
}

func metadataBytes(p Plan) []byte {
	data, _ := json.MarshalIndent(MetadataFor(p), "", "  ")
	return append(data, '\n')
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Publish resumes an existing draft without replacing uploaded assets. Only
// an empty GitHub starter placeholder can be removed after a guarded recheck.
// A conflicting draft/upload or a nonimmutable published release fails closed and is never
// promoted. Digest comparison here protects retries; consumers verify GitHub's
// signed immutable release independently, with no maintained checksum pins.
func Publish(ctx context.Context, api API, p Plan, bundle []byte) (github.ReleaseRecord, error) {
	if err := p.Validate(); err != nil {
		return github.ReleaseRecord{}, err
	}
	if len(bundle) == 0 || len(bundle) > 128<<20 {
		return github.ReleaseRecord{}, errors.New("release bundle invalid")
	}
	if err := ValidateBundle(p, bundle); err != nil {
		return github.ReleaseRecord{}, err
	}
	enabled, err := api.ImmutableReleasesEnabled(ctx, Repository)
	if err != nil {
		return github.ReleaseRecord{}, err
	}
	if !enabled {
		return github.ReleaseRecord{}, errors.New("enable Sofa immutable releases before publication")
	}
	record, found, err := api.ReleaseRecordByTag(ctx, Repository, p.Version)
	if err != nil {
		return github.ReleaseRecord{}, err
	}
	if !found {
		tagSource, tagFound, err := api.ReleaseTag(ctx, Repository, p.Version)
		if err != nil {
			return github.ReleaseRecord{}, err
		}
		if tagFound && tagSource != p.SourceSHA {
			return github.ReleaseRecord{}, errors.New("exact release tag already belongs to another source")
		}
		planData, _ := json.Marshal(p)
		record, err = api.CreateDraftRelease(ctx, Repository, p.Version, p.SourceSHA, "sofa-release-plan:v1\n"+string(planData))
		if err != nil {
			return github.ReleaseRecord{}, err
		}
	}
	if record.ID < 1 || record.Tag != p.Version || record.Source != p.SourceSHA || record.Prerelease {
		return github.ReleaseRecord{}, errors.New("release source identity mismatch")
	}
	var reserved Plan
	if !strings.HasPrefix(record.Body, "sofa-release-plan:v1\n") || json.Unmarshal([]byte(strings.TrimPrefix(record.Body, "sofa-release-plan:v1\n")), &reserved) != nil || reserved != p {
		return github.ReleaseRecord{}, errors.New("release reservation differs from plan")
	}
	assets := []struct {
		Name    string
		Content []byte
	}{
		{
			Name:    BundleName,
			Content: bundle,
		},
		{
			Name:    MetadataName,
			Content: metadataBytes(p),
		},
	}
	for _, asset := range assets {
		var match *github.ReleaseAsset
		for _, existing := range record.Assets {
			if existing.Name != asset.Name {
				continue
			}
			if match != nil {
				return github.ReleaseRecord{}, errors.New("existing release asset differs; refuse replacement")
			}
			matched := existing
			match = &matched
		}
		if match != nil {
			if record.Draft && match.ID > 0 && match.State == "starter" && match.Size == 0 && match.Digest == "" {
				// GitHub can leave this empty placeholder after an upstream 502.
				// The API rechecks the reserved draft and exact placeholder before
				// deletion; uploaded bytes are never replaced.
				if err := api.DeleteEmptyDraftUpload(ctx, Repository, record, *match); err != nil {
					return github.ReleaseRecord{}, err
				}
			} else {
				if match.State != "uploaded" || match.Size != int64(len(asset.Content)) || match.Digest != digest(asset.Content) {
					return github.ReleaseRecord{}, errors.New("existing release asset differs; refuse replacement")
				}
				continue
			}
		}
		if !record.Draft {
			return github.ReleaseRecord{}, errors.New("published release asset missing")
		}
		uploaded, err := api.UploadReleaseAsset(ctx, Repository, record.ID, asset.Name, asset.Content)
		if err != nil {
			return github.ReleaseRecord{}, err
		}
		if uploaded.Name != asset.Name || uploaded.Digest != digest(asset.Content) || uploaded.Size != int64(len(asset.Content)) || uploaded.State != "uploaded" {
			return github.ReleaseRecord{}, errors.New("uploaded release asset identity mismatch")
		}
	}
	if record.Draft {
		record, err = api.PublishDraftRelease(ctx, Repository, record.ID)
		if err != nil {
			return github.ReleaseRecord{}, err
		}
	}
	if record.Tag != p.Version || record.Source != p.SourceSHA || record.Draft || record.Prerelease || !record.Immutable {
		return github.ReleaseRecord{}, errors.New("published release is not immutable or has changed identity")
	}
	source, found, err := api.ReleaseTag(ctx, Repository, p.Version)
	if err != nil {
		return github.ReleaseRecord{}, err
	}
	if !found || source != p.SourceSHA {
		return github.ReleaseRecord{}, errors.New("published exact release tag changed")
	}
	return record, nil
}

type Canary struct {
	ReleaseVersion    string `json:"release_version"`
	SourceSHA         string `json:"source_sha"`
	Correlation       string `json:"correlation"`
	CallerSHA         string `json:"caller_sha"`
	ReleaseRunID      string `json:"release_run_id"`
	ReleaseRunAttempt string `json:"release_run_attempt"`
	RunID             int64  `json:"run_id"`
}

func ValidateCanary(run github.ReleaseCanaryRun, p Plan, evidence Canary) error {
	if err := p.Validate(); err != nil {
		return err
	}
	correlation, err := Correlation(p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt)
	if err != nil || correlation != evidence.Correlation {
		return errors.New("release canary correlation does not bind this release")
	}
	if run.CreatedAt.IsZero() || run.CreatedAt.After(time.Now().UTC().Add(time.Minute)) {
		return errors.New("release canary creation time invalid")
	}
	if evidence.ReleaseVersion != p.Version || evidence.SourceSHA != p.SourceSHA || !shaPattern.MatchString(evidence.Correlation) || !shaPattern.MatchString(evidence.CallerSHA) || evidence.RunID < 1 || run.ID != evidence.RunID || run.Title != "Sofa release canary / "+evidence.Correlation || run.Event != "workflow_dispatch" || run.Path != ".github/workflows/sofa-release-canary.yml" || run.HeadSHA != evidence.CallerSHA || run.Repository.ID < 1 || run.Repository.ID != run.HeadRepository.ID || run.Repository.FullName != DisposableRepository || run.HeadRepository.FullName != DisposableRepository || run.Status != "completed" || run.Conclusion != "success" {
		return errors.New("release canary has no matching successful trusted run")
	}
	return nil
}

// Promote accepts only a published immutable exact release and independently
// checked canary evidence. It never edits factory ledgers or consumer state.
func Promote(ctx context.Context, api API, p Plan, run github.ReleaseCanaryRun, evidence Canary) error {
	if err := ValidateCanary(run, p, evidence); err != nil {
		return err
	}
	record, found, err := api.ReleaseRecordByTag(ctx, Repository, p.Version)
	if err != nil {
		return err
	}
	if !found || record.Tag != p.Version || record.Source != p.SourceSHA || record.Draft || record.Prerelease || !record.Immutable {
		return errors.New("release cannot be promoted")
	}
	source, found, err := api.ReleaseTag(ctx, Repository, p.Version)
	if err != nil {
		return err
	}
	if !found || source != p.SourceSHA {
		return errors.New("release tag does not match source")
	}
	return api.UpdateReleaseChannel(ctx, Repository, p.Channel, p.ExpectedChannelSHA, p.SourceSHA)
}

// Correlation binds a dispatch to the exact release/source and Actions attempt.
func Correlation(p Plan, runID, attempt string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if id, err := strconv.ParseInt(runID, 10, 64); err != nil || id < 1 {
		return "", errors.New("release run identity invalid")
	}
	if n, err := strconv.Atoi(attempt); err != nil || n < 1 {
		return "", errors.New("release attempt identity invalid")
	}
	sum := sha256.Sum256([]byte(p.Version + "\n" + p.SourceSHA + "\n" + runID + "\n" + attempt))
	return hex.EncodeToString(sum[:20]), nil
}

type CanaryAPI interface {
	RepositoryInfo(context.Context, string) (github.RepositoryInfo, error)
	Reference(context.Context, string, string) (string, bool, error)
	DispatchReleaseCanary(context.Context, string, string, string, string, string, string, string) error
	ReleaseCanaryRuns(context.Context, string, int, time.Time) ([]github.ReleaseCanaryRun, error)
	ReleaseCanaryRun(context.Context, string, int64) (github.ReleaseCanaryRun, error)
}

// RunCanary correlates before and after dispatch, so retrying an interrupted
// polling operation finds its existing run rather than triggering a new one.
func RunCanary(ctx context.Context, api CanaryAPI, p Plan, releaseRunID, releaseRunAttempt string, poll time.Duration) (Canary, error) {
	// An Actions attempt cannot outlive this window. Its ten-minute observer
	// retries therefore find the same correlation without scanning lifetime
	// history. A rerun has a distinct attempt and correlation.
	since := time.Now().UTC().Add(-24 * time.Hour)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Canary{}, err
	}
	if err := p.Validate(); err != nil {
		return Canary{}, err
	}
	correlation, err := Correlation(p, releaseRunID, releaseRunAttempt)
	if err != nil || poll <= 0 {
		return Canary{}, errors.New("invalid release canary correlation")
	}
	info, err := api.RepositoryInfo(ctx, DisposableRepository)
	if err != nil {
		return Canary{}, err
	}
	if info.FullName != DisposableRepository || info.DefaultBranch == "" || info.Archived || info.Disabled {
		return Canary{}, errors.New("release canary repository unavailable")
	}
	caller, found, err := api.Reference(ctx, DisposableRepository, info.DefaultBranch)
	if err != nil {
		return Canary{}, err
	}
	if !found || !shaPattern.MatchString(caller) {
		return Canary{}, errors.New("release canary caller unavailable")
	}
	evidence := Canary{
		ReleaseVersion:    p.Version,
		SourceSHA:         p.SourceSHA,
		Correlation:       correlation,
		CallerSHA:         caller,
		ReleaseRunID:      releaseRunID,
		ReleaseRunAttempt: releaseRunAttempt,
	}
	find := func() (github.ReleaseCanaryRun, bool, error) {
		var match github.ReleaseCanaryRun
		for page := 1; page <= 10; page++ {
			runs, err := api.ReleaseCanaryRuns(ctx, DisposableRepository, page, since)
			if err != nil {
				return github.ReleaseCanaryRun{}, false, err
			}
			for _, run := range runs {
				if run.Title != "Sofa release canary / "+correlation {
					continue
				}
				if run.CreatedAt.IsZero() || run.CreatedAt.Before(since) || run.CreatedAt.After(time.Now().UTC().Add(time.Minute)) {
					return github.ReleaseCanaryRun{}, false, errors.New("release canary creation time invalid")
				}
				if match.ID != 0 && match.ID != run.ID {
					return github.ReleaseCanaryRun{}, false, errors.New("duplicate release canary correlation")
				}
				match = run
			}
			if len(runs) < 100 {
				return match, match.ID > 0, nil
			}
		}
		return github.ReleaseCanaryRun{}, false, errors.New("release canary discovery exceeds bound")
	}
	run, found, err := find()
	if err != nil {
		return Canary{}, err
	}
	if !found {
		if err := api.DispatchReleaseCanary(ctx, DisposableRepository, info.DefaultBranch, p.Version, p.SourceSHA, correlation, releaseRunID, releaseRunAttempt); err != nil {
			return Canary{}, err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return Canary{}, err
		}
		if found {
			if run.CreatedAt.Before(since) {
				return Canary{}, errors.New("release canary creation time outside attempt window")
			}
			evidence.RunID = run.ID
			if run.Status == "completed" {
				return evidence, ValidateCanary(run, p, evidence)
			}
			// Fail on wrong identity before trusting a queued run or polling it.
			candidate := run
			candidate.Status = "completed"
			candidate.Conclusion = "success"
			if err := ValidateCanary(candidate, p, evidence); err != nil {
				return Canary{}, err
			}
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Canary{}, ctx.Err()
		case <-timer.C:
		}
		if found {
			run, err = api.ReleaseCanaryRun(ctx, DisposableRepository, run.ID)
		} else {
			run, found, err = find()
		}
		if err != nil {
			return Canary{}, err
		}
	}
}
