// Package distribution owns version allocation, publication recovery and promotion.
// It never opens factory state or resets consumer ownership/budgets.
package distribution

import (
	"context"
	"errors"
	"regexp"
	"strings"

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
