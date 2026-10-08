package distribution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kevinmartin/sofa/internal/github"
)

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
