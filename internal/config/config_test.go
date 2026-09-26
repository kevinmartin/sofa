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
		"unknown":         text + "unrecognized: true\n",
		"duplicate":       text + "schema_version: 1\n",
		"multidoc":        text + "---\n{}\n",
		"schema":          strings.Replace(text, "schema_version: 1", "schema_version: 2", 1),
		"alias":           strings.Replace(text, "Ready", "&ready Ready", 1),
		"privileged auth": strings.Replace(text, "SOFA_MODEL_TOKEN", "SOFA_PUBLISH_TOKEN", 1),
		"unbounded":       strings.Replace(text, "max_agent_turns: 1", "max_agent_turns: 0", 1),
		"path escape":     strings.Replace(text, "- fixture/greeting.go", "- ../", 1),
		"oversized":       strings.Repeat(" ", MaxBytes+1),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(text)); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
}

func TestPortablePaths(t *testing.T) {
	for _, p := range []string{"../x", "/tmp/x", "x/../y", "x//y", "x\\y", ".GIT/config", ".git:stream", "a\n.go", "x/*", "a/./b", "a./b", "a/ b"} {
		if SafePath(p) {
			t.Errorf("accepted unsafe path %q", p)
		}
	}
}
