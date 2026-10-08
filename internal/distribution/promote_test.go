package distribution

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kevinmartin/sofa/internal/github"
)

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
