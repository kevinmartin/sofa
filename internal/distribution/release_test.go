package distribution

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
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

func TestPublicationResumesInterruptedUploadWithoutReplacingAssetsOrAdvancingChannel(t *testing.T) {
	ctx := context.Background()
	api := newFake()
	p, err := Allocate(ctx, api, Config{Series: "0.1"}, newSource)
	if err != nil {
		t.Fatal(err)
	}
	api.failUpload = 2
	if _, err := Publish(ctx, api, p, fixtureBundle(t, p)); err == nil {
		t.Fatal("interrupted upload succeeded")
	}
	if api.channels["v0"] != oldSource || len(api.records) != 1 || !api.records[0].Draft || len(api.records[0].Assets) != 1 {
		t.Fatalf("interruption lost draft or changed channel: %+v", api)
	}
	resumed, err := Allocate(ctx, api, Config{Series: "0.1"}, newSource)
	if err != nil || resumed != p {
		t.Fatalf("retry allocated another release: %+v %v", resumed, err)
	}
	if _, err := Publish(ctx, api, resumed, fixtureBundle(t, p)); err != nil {
		t.Fatal(err)
	}
	if api.uploads != 3 || len(api.records) != 1 || len(api.records[0].Assets) != 2 || api.records[0].Draft || api.channels["v0"] != oldSource {
		t.Fatalf("retry replaced an asset or promoted before canary: %+v", api)
	}
	run, evidence := trustedCanary(p)
	if err := Promote(ctx, api, p, run, evidence); err != nil {
		t.Fatal(err)
	}
	if api.channels["v0"] != newSource || api.updates != 1 {
		t.Fatal("successful canary was not promoted")
	}
	if err := Promote(ctx, api, p, run, evidence); err != nil || api.updates != 1 {
		t.Fatalf("replay not idempotent: %v updates=%d", err, api.updates)
	}
}

func TestOlderReleaseRetryCannotRollbackCompetingPromotion(t *testing.T) {
	ctx := context.Background()
	api := newFake()
	p := fixturePlan()
	if _, err := Publish(ctx, api, p, fixtureBundle(t, p)); err != nil {
		t.Fatal(err)
	}
	api.channels["v0"] = otherSource
	resumed, err := Allocate(ctx, api, Config{Series: "0.1"}, newSource)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ExpectedChannelSHA != oldSource {
		t.Fatal("retry silently recaptured newer channel")
	}
	run, evidence := trustedCanary(p)
	if err := Promote(ctx, api, resumed, run, evidence); err == nil {
		t.Fatal("stale promotion accepted")
	}
	if api.channels["v0"] != otherSource || api.updates != 0 {
		t.Fatal("stale release rolled back another promotion")
	}
}

func TestPublishedAssetsAreNeverReplaced(t *testing.T) {
	api := newFake()
	p := fixturePlan()
	bundle := fixtureBundle(t, p)
	if _, err := Publish(context.Background(), api, p, bundle); err != nil {
		t.Fatal(err)
	}
	api.records[0].Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := Publish(context.Background(), api, p, bundle); err == nil {
		t.Fatal("conflicting immutable asset accepted")
	}
	if api.uploads != 2 || api.channels["v0"] != oldSource {
		t.Fatal("asset conflict caused replacement or promotion")
	}
}

func TestPublicationRecoversEmptyStarterAfterFailedUpload(t *testing.T) {
	api := newFake()
	api.failUpload = 2
	api.leaveStub = true
	p := fixturePlan()
	bundle := fixtureBundle(t, p)
	if _, err := Publish(context.Background(), api, p, bundle); err == nil {
		t.Fatal("interrupted upload succeeded")
	}
	if len(api.records[0].Assets) != 2 || api.records[0].Assets[1].State != "starter" {
		t.Fatal("failure did not leave the GitHub starter placeholder")
	}
	original := api.records[0].Assets[0]
	if _, err := Publish(context.Background(), api, p, bundle); err != nil {
		t.Fatal(err)
	}
	if api.cleanups != 1 || api.uploads != 3 || api.records[0].Assets[0] != original || api.records[0].Draft || api.channels["v0"] != oldSource {
		t.Fatal("recovery replaced uploaded bytes or advanced the major channel")
	}
}

func TestPublicationNeverDeletesNonemptyUploadedOrPublishedAssets(t *testing.T) {
	for _, kind := range []string{"nonempty", "digest", "uploaded", "published", "invalid ID", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			api := newFake()
			p := fixturePlan()
			body, _ := json.Marshal(p)
			asset := github.ReleaseAsset{
				ID:    999,
				Name:  BundleName,
				State: "starter",
			}
			record := github.ReleaseRecord{
				ID:     1,
				Tag:    p.Version,
				Source: p.SourceSHA,
				Body:   "sofa-release-plan:v1\n" + string(body),
				Draft:  true,
			}
			switch kind {
			case "nonempty":
				asset.Size = 1
			case "digest":
				asset.Digest = "sha256:unexpected"
			case "uploaded":
				asset.State = "uploaded"
			case "published":
				record.Draft = false
			case "invalid ID":
				asset.ID = 0
			}
			record.Assets = []github.ReleaseAsset{asset}
			if kind == "duplicate" {
				record.Assets = append(record.Assets, asset)
			}
			api.records = []github.ReleaseRecord{record}
			if _, err := Publish(context.Background(), api, p, fixtureBundle(t, p)); err == nil {
				t.Fatal("unsafe interrupted asset accepted")
			}
			if api.cleanups != 0 || api.uploads != 0 || api.updates != 0 {
				t.Fatal("unsafe interrupted asset caused a mutation")
			}
		})
	}
}

func TestPublicationFailsClosedWithoutImmutability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "published mutable"}[enabled], func(t *testing.T) {
			api := newFake()
			api.enabled = enabled
			api.immutable = false
			p := fixturePlan()
			if _, err := Publish(context.Background(), api, p, fixtureBundle(t, p)); err == nil {
				t.Fatal("nonimmutable publication accepted")
			}
			if api.channels["v0"] != oldSource {
				t.Fatal("unverified release promoted")
			}
			if !enabled && len(api.records) != 0 {
				t.Fatal("created draft while immutability disabled")
			}
		})
	}
}

func TestCanaryRejectsUnrelatedFailedOrChangedIdentity(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	mutations := map[string]func(*github.ReleaseCanaryRun, *Canary){"failure": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Conclusion = "failure" }, "wrong source": func(_ *github.ReleaseCanaryRun, e *Canary) { e.SourceSHA = oldSource }, "different caller": func(r *github.ReleaseCanaryRun, _ *Canary) { r.HeadSHA = oldSource }, "different repository": func(r *github.ReleaseCanaryRun, _ *Canary) { r.HeadRepository.ID = 10 }, "different dispatch": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Title = "Sofa release canary / " + oldSource }, "different run": func(r *github.ReleaseCanaryRun, _ *Canary) { r.ID++ }, "different workflow": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Path = ".github/workflows/other.yml" }, "queued": func(r *github.ReleaseCanaryRun, _ *Canary) { r.Status = "queued" }}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			r, e := run, evidence
			mutate(&r, &e)
			api := newFake()
			if err := Promote(context.Background(), api, p, r, e); err == nil {
				t.Fatal("untrusted canary allowed promotion")
			}
			if api.updates != 0 {
				t.Fatal("invalid evidence mutated channel")
			}
		})
	}
}

func TestVersionAllocationUsesSeriesAndIncludesUnfinishedDrafts(t *testing.T) {
	api := newFake()
	api.records = []github.ReleaseRecord{{Tag: "v0.1.3", Source: oldSource, Draft: true}, {Tag: "v1.1.90", Source: otherSource}, {Tag: "v0.2.90", Source: otherSource}}
	p, err := Allocate(context.Background(), api, Config{Series: "0.1"}, newSource)
	if err != nil || p.Version != "v0.1.4" {
		t.Fatalf("allocation=%+v %v", p, err)
	}
	if _, err := Allocate(context.Background(), api, Config{Series: "0.01"}, newSource); err == nil {
		t.Fatal("noncanonical series accepted")
	}
}

func TestReproducibleBundleAndMetadataValidation(t *testing.T) {
	p := fixturePlan()
	first := fixtureBundle(t, p)
	second := fixtureBundle(t, p)
	if !bytes.Equal(first, second) {
		t.Fatal("rebuilt bundle differs")
	}
	if err := ValidateBundle(p, first); err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), first...)
	corrupted[len(corrupted)-8] ^= 0x80
	if err := ValidateBundle(p, corrupted); err == nil {
		t.Fatal("corrupted gzip checksum accepted before publication")
	}
	changed := p
	changed.SourceSHA = otherSource
	if err := ValidateBundle(changed, first); err == nil {
		t.Fatal("wrong source metadata accepted")
	}
	bad, err := Bundle(p, map[string][]byte{"sofa": []byte("not ELF"), "sofa-test": elfFixture()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBundle(p, bad); err == nil {
		t.Fatal("nonbinary fixture accepted")
	}
	var metadata Metadata
	if json.Unmarshal(metadataBytes(p), &metadata) != nil || metadata != MetadataFor(p) {
		t.Fatal("standalone metadata differs")
	}
}

type canaryFake struct {
	runs       []github.ReleaseCanaryRun
	pages      map[int][]github.ReleaseCanaryRun
	dispatches int
	run        github.ReleaseCanaryRun
	err        error
}

func (f *canaryFake) RepositoryInfo(context.Context, string) (github.RepositoryInfo, error) {
	return github.RepositoryInfo{FullName: DisposableRepository, DefaultBranch: "main"}, nil
}
func (f *canaryFake) Reference(context.Context, string, string) (string, bool, error) {
	return otherSource, true, nil
}
func (f *canaryFake) DispatchReleaseCanary(_ context.Context, _, _, _, _, _, _, _ string) error {
	f.dispatches++
	f.runs = []github.ReleaseCanaryRun{f.run}
	return nil
}
func (f *canaryFake) ReleaseCanaryRuns(_ context.Context, _ string, page int, since time.Time) ([]github.ReleaseCanaryRun, error) {
	if f.pages != nil {
		return f.pages[page], f.err
	}
	var recent []github.ReleaseCanaryRun
	for _, run := range f.runs {
		if run.CreatedAt.IsZero() || !run.CreatedAt.Before(since) {
			recent = append(recent, run)
		}
	}
	start := (page - 1) * 100
	if start >= len(recent) {
		return nil, f.err
	}
	end := start + 100
	if end > len(recent) {
		end = len(recent)
	}
	return recent[start:end], f.err
}
func (f *canaryFake) ReleaseCanaryRun(context.Context, string, int64) (github.ReleaseCanaryRun, error) {
	return f.run, f.err
}

func TestCanaryRetryReusesExactSuccessfulDispatchAndDoesNotTrustNeighbors(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	neighbor := run
	neighbor.ID = 99
	neighbor.Title = "another run"
	api := &canaryFake{runs: []github.ReleaseCanaryRun{neighbor, run}, run: run}
	got, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got != evidence || api.dispatches != 0 {
		t.Fatalf("canary retry=%+v err=%v dispatches=%d", got, err, api.dispatches)
	}
	api = &canaryFake{run: run}
	got, err = RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got.RunID != run.ID || api.dispatches != 1 {
		t.Fatalf("canary dispatch=%+v %v count=%d", got, err, api.dispatches)
	}
	duplicate := run
	duplicate.ID++
	api = &canaryFake{runs: []github.ReleaseCanaryRun{run, duplicate}, run: run}
	if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil {
		t.Fatal("duplicate correlation accepted")
	}
}

func TestCanaryRejectsDuplicateCorrelationAcrossDiscoveryPages(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	first := make([]github.ReleaseCanaryRun, 100)
	first[0] = run
	for i := 1; i < len(first); i++ {
		first[i].ID = int64(1000 + i)
		first[i].Title = "neighbor"
	}
	duplicate := run
	duplicate.ID++
	api := &canaryFake{pages: map[int][]github.ReleaseCanaryRun{1: first, 2: {duplicate}}}
	if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil {
		t.Fatal("second-page duplicate correlation accepted")
	}
	if api.dispatches != 0 {
		t.Fatal("ambiguous discovery dispatched another run")
	}
}

func TestCanaryDiscoveryExcludesMoreThanOneThousandHistoricRuns(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	history := make([]github.ReleaseCanaryRun, 1100)
	for i := range history {
		history[i] = run
		history[i].ID = int64(1000 + i)
		// Even the same title outside the attempt window is historical, not
		// evidence for this current release attempt.
		history[i].CreatedAt = time.Now().UTC().Add(-48 * time.Hour)
	}
	api := &canaryFake{runs: append(history, run), run: run}
	got, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond)
	if err != nil || got != evidence || api.dispatches != 0 {
		t.Fatalf("historic lifetime bound interfered: %+v %v dispatches=%d", got, err, api.dispatches)
	}
}

func TestCanaryRejectsMissingOrFutureCreationTime(t *testing.T) {
	for _, created := range []time.Time{{}, time.Now().UTC().Add(time.Hour)} {
		p := fixturePlan()
		run, evidence := trustedCanary(p)
		run.CreatedAt = created
		api := &canaryFake{runs: []github.ReleaseCanaryRun{run}, run: run}
		if _, err := RunCanary(context.Background(), api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); err == nil || api.dispatches != 0 {
			t.Fatal("invalid creation time authorized or retriggered a canary")
		}
	}
}

func TestUnsupportedStableMajorFailsBeforeAPI(t *testing.T) {
	p := fixturePlan()
	p.Version, p.Channel = "v2.0.0", "v2"
	if err := p.Validate(); err == nil {
		t.Fatal("unsupported release plan accepted")
	}
	if _, err := Allocate(context.Background(), nil, Config{Series: "2.0"}, newSource); err == nil {
		t.Fatal("unsupported allocation reached API")
	}
	if _, err := RollbackPlan(context.Background(), nil, p.Version, oldSource); err == nil {
		t.Fatal("unsupported rollback reached API")
	}
}

func TestExplicitRollbackRequiresCurrentRefAndFreshSuccessfulCanary(t *testing.T) {
	ctx := context.Background()
	api := newFake()
	older := Plan{Version: "v0.1.0", SourceSHA: oldSource, Channel: "v0", ExpectedChannelSHA: ""}
	body, _ := json.Marshal(older)
	api.records = []github.ReleaseRecord{{ID: 1, Tag: older.Version, Source: older.SourceSHA, Body: "sofa-release-plan:v1\n" + string(body), Immutable: true}}
	api.channels[older.Version] = oldSource
	api.channels["v0"] = newSource
	if _, err := RollbackPlan(ctx, api, older.Version, otherSource); err == nil {
		t.Fatal("rollback accepted stale expected ref")
	}
	p, err := RollbackPlan(ctx, api, older.Version, newSource)
	if err != nil {
		t.Fatal(err)
	}
	if p.ExpectedChannelSHA != newSource || p.SourceSHA != oldSource || api.updates != 0 {
		t.Fatal("rollback planning altered channel or dropped explicit guard")
	}
	run, evidence := trustedCanary(p)
	failed := run
	failed.Conclusion = "failure"
	if err := Promote(ctx, api, p, failed, evidence); err == nil {
		t.Fatal("failed rollback compatibility canary accepted")
	}
	if api.channels["v0"] != newSource {
		t.Fatal("failed canary changed rollback channel")
	}
	if err := Promote(ctx, api, p, run, evidence); err != nil {
		t.Fatal(err)
	}
	if api.channels["v0"] != oldSource || api.updates != 1 {
		t.Fatal("explicit qualified rollback did not move channel")
	}
}

func TestCanaryCancellationDoesNotBecomeSuccess(t *testing.T) {
	p := fixturePlan()
	run, evidence := trustedCanary(p)
	run.Status = "queued"
	run.Conclusion = ""
	api := &canaryFake{runs: []github.ReleaseCanaryRun{run}, run: run}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunCanary(ctx, api, p, evidence.ReleaseRunID, evidence.ReleaseRunAttempt, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled canary=%v", err)
	}
}
