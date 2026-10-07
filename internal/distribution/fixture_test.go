package distribution

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/github"
)

var oldSource = strings.Repeat("a", 40)

var newSource = strings.Repeat("b", 40)

var otherSource = strings.Repeat("c", 40)

type releaseFake struct {
	channels   map[string]string
	records    []github.ReleaseRecord
	enabled    bool
	immutable  bool
	uploads    int
	failUpload int
	updates    int
	cleanups   int
	leaveStub  bool
}

func newFake() *releaseFake {
	return &releaseFake{channels: map[string]string{"v0": oldSource}, enabled: true, immutable: true}
}

func (f *releaseFake) ReleasesPage(_ context.Context, _ string, page int) ([]github.ReleaseRecord, error) {
	if page != 1 {
		return nil, nil
	}
	return f.records, nil
}

func (f *releaseFake) ReleaseRecordByTag(_ context.Context, _ string, tag string) (github.ReleaseRecord, bool, error) {
	for _, r := range f.records {
		if r.Tag == tag {
			return r, true, nil
		}
	}
	return github.ReleaseRecord{}, false, nil
}

func (f *releaseFake) CreateDraftRelease(_ context.Context, _ string, tag, source, body string) (github.ReleaseRecord, error) {
	r := github.ReleaseRecord{ID: int64(len(f.records) + 1), Tag: tag, Source: source, Body: body, Draft: true}
	f.records = append(f.records, r)
	return r, nil
}

func (f *releaseFake) PublishDraftRelease(_ context.Context, _ string, id int64) (github.ReleaseRecord, error) {
	for i := range f.records {
		if f.records[i].ID == id {
			f.records[i].Draft = false
			f.records[i].Immutable = f.immutable
			f.channels[f.records[i].Tag] = f.records[i].Source
			return f.records[i], nil
		}
	}
	return github.ReleaseRecord{}, errors.New("missing draft")
}

func (f *releaseFake) UploadReleaseAsset(_ context.Context, _ string, id int64, name string, data []byte) (github.ReleaseAsset, error) {
	f.uploads++
	if f.uploads == f.failUpload {
		if f.leaveStub {
			for i := range f.records {
				if f.records[i].ID == id {
					f.records[i].Assets = append(f.records[i].Assets, github.ReleaseAsset{
						ID:    999,
						Name:  name,
						State: "starter",
					})
				}
			}
		}
		return github.ReleaseAsset{}, errors.New("interrupted upload")
	}
	a := github.ReleaseAsset{ID: int64(f.uploads), Name: name, Size: int64(len(data)), Digest: digest(data), State: "uploaded"}
	for i := range f.records {
		if f.records[i].ID == id {
			f.records[i].Assets = append(f.records[i].Assets, a)
			return a, nil
		}
	}
	return github.ReleaseAsset{}, errors.New("missing draft")
}

func (f *releaseFake) DeleteEmptyDraftUpload(_ context.Context, _ string, record github.ReleaseRecord, asset github.ReleaseAsset) error {
	f.cleanups++
	for i := range f.records {
		if f.records[i].ID == record.ID {
			for j, existing := range f.records[i].Assets {
				if existing == asset {
					f.records[i].Assets = append(f.records[i].Assets[:j], f.records[i].Assets[j+1:]...)
					return nil
				}
			}
		}
	}
	return errors.New("missing starter")
}

func (f *releaseFake) ImmutableReleasesEnabled(context.Context, string) (bool, error) {
	return f.enabled, nil
}

func (f *releaseFake) ReleaseTag(_ context.Context, _ string, tag string) (string, bool, error) {
	s, found := f.channels[tag]
	return s, found, nil
}

func (f *releaseFake) UpdateReleaseChannel(_ context.Context, _ string, tag, expected, source string) error {
	if f.channels[tag] == source {
		return nil
	}
	if f.channels[tag] != expected {
		return errors.New("channel moved")
	}
	f.channels[tag] = source
	f.updates++
	return nil
}

func fixturePlan() Plan {
	return Plan{Version: "v0.1.0", SourceSHA: newSource, Channel: "v0", ExpectedChannelSHA: oldSource}
}

func elfFixture() []byte {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 2)
	binary.LittleEndian.PutUint16(data[18:], 62)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint16(data[52:], 64)
	return data
}

func fixtureBundle(t *testing.T, p Plan) []byte {
	t.Helper()
	data, err := Bundle(p, map[string][]byte{"sofa": elfFixture(), "sofa-test": elfFixture()})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func trustedCanary(p Plan) (github.ReleaseCanaryRun, Canary) {
	correlation, _ := Correlation(p, "100", "1")
	e := Canary{
		ReleaseVersion:    p.Version,
		SourceSHA:         p.SourceSHA,
		Correlation:       correlation,
		CallerSHA:         otherSource,
		RunID:             55,
		ReleaseRunID:      "100",
		ReleaseRunAttempt: "1",
	}
	r := github.ReleaseCanaryRun{
		ID:         e.RunID,
		Title:      "Sofa release canary / " + e.Correlation,
		Event:      "workflow_dispatch",
		Path:       ".github/workflows/sofa-release-canary.yml",
		HeadSHA:    e.CallerSHA,
		Status:     "completed",
		Conclusion: "success",
		CreatedAt:  time.Now().UTC().Add(-time.Minute),
	}
	r.Repository.ID = 9
	r.Repository.FullName = DisposableRepository
	r.HeadRepository = r.Repository
	return r, e
}
