package distribution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/github"
)

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
