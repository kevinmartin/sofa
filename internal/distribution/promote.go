package distribution

import (
	"context"
	"errors"

	"github.com/kevinmartin/sofa/internal/github"
)

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
