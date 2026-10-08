package distribution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

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
