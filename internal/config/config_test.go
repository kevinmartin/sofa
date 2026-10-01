package config

import (
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/consumer/.sofa.yml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStrictConfiguration(t *testing.T) {
	text := fixture(t)
	c, err := Decode(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Allows("fixture/greeting.go") || c.Allows("fixture/main.go") || c.Allows("fixture/../.git/config") {
		t.Fatal("allowlist boundary")
	}
	if c.Lifecycle == nil || c.Lifecycle.EffectivePollMinutes() != 10 || c.Lifecycle.EffectiveDiscoveryWIP() != 2 || c.Lifecycle.EffectiveDeliveryWIP() != 1 {
		t.Fatal("lifecycle defaults or mapping unavailable")
	}
	blockedField, nextField := c.Lifecycle.EffectiveAdviceFields()
	if blockedField != "Blocked reason" || nextField != "Next action" {
		t.Fatalf("default advice fields = %q, %q", blockedField, nextField)
	}
	d1, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Decode(strings.NewReader("# harmless comment\n" + text))
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := c2.Digest()
	if d1 != d2 {
		t.Fatal("formatting altered semantic digest")
	}
	cases := map[string]string{
		"unknown":                  text + "unrecognized: true\n",
		"duplicate":                text + "schema_version: 1\n",
		"multidoc":                 text + "---\n{}\n",
		"schema":                   strings.Replace(text, "schema_version: 1", "schema_version: 2", 1),
		"alias":                    strings.Replace(text, "Ready", "&ready Ready", 1),
		"privileged auth":          strings.Replace(text, "SOFA_MODEL_TOKEN", "SOFA_PUBLISH_TOKEN", 1),
		"unbounded":                strings.Replace(text, "max_agent_turns: 1", "max_agent_turns: 0", 1),
		"path escape":              strings.Replace(text, "- fixture/greeting.go", "- ../", 1),
		"oversized":                strings.Repeat(" ", MaxBytes+1),
		"missing lifecycle stage":  strings.Replace(text, "    spec_review: Spec Review\n", "", 1),
		"duplicate lifecycle name": strings.Replace(text, "    backlog: Backlog", "    backlog: Discovery", 1),
		"ready status mismatch":    strings.Replace(text, "    ready: Ready", "    ready: Other", 1),
		"invalid poll interval":    strings.Replace(text, "poll_minutes: 10", "poll_minutes: 1", 1),
		"unbounded discovery":      strings.Replace(text, "discovery_wip: 2", "discovery_wip: 21", 1),
		"untrusted release check":  strings.Replace(text, "required_checks: []", "required_checks: [{name: smoke, app_id: 0}]", 1),
		"unpaired advice field":    strings.Replace(text, "  delivery_wip: 1", "  delivery_wip: 1\n  blocked_reason_field: Blocked reason", 1),
		"duplicate advice field":   strings.Replace(text, "  delivery_wip: 1", "  delivery_wip: 1\n  blocked_reason_field: Advice\n  next_action_field: Advice", 1),
		"status advice field":      strings.Replace(text, "  delivery_wip: 1", "  delivery_wip: 1\n  blocked_reason_field: Status\n  next_action_field: Next action", 1),
		"owner field collides":     strings.Replace(text, "  delivery_wip: 1", "  delivery_wip: 1\n  dependencies_field: Blocked reason", 1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(text)); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestConfiguredProjectAdviceFields(t *testing.T) {
	text := strings.Replace(fixture(t), "  delivery_wip: 1", "  delivery_wip: 1\n  blocked_reason_field: Hold\n  next_action_field: Follow up", 1)
	c, err := Decode(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	blocked, next := c.Lifecycle.EffectiveAdviceFields()
	if blocked != "Hold" || next != "Follow up" {
		t.Fatalf("configured advice fields = %q, %q", blocked, next)
	}
}

func TestPortablePaths(t *testing.T) {
	for _, p := range []string{"../x", "/tmp/x", "x/../y", "x//y", "x\\y", ".GIT/config", ".git:stream", "a\n.go", "x/*", "a/./b", "a./b", "a/ b"} {
		if SafePath(p) {
			t.Errorf("accepted unsafe path %q", p)
		}
	}
}
