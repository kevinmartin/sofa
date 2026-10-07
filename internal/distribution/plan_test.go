package distribution

import (
	"context"
	"testing"

	"github.com/kevinmartin/sofa/internal/github"
)

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
