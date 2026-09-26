package config

import (
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"go.yaml.in/yaml/v3"
)

// Compile with a real Draft 2020-12 validator so unsupported keywords, broken
// references, and editor-only schema regressions fail alongside runtime tests.
func TestConfigurationSchema(t *testing.T) {
	schema, err := jsonschema.Compile("../../schemas/sofa.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	text := fixture(t)
	replace := func(old, new string) string {
		t.Helper()
		if !strings.Contains(text, old) {
			t.Fatalf("fixture no longer contains %q", old)
		}
		return strings.Replace(text, old, new, 1)
	}
	cases := []struct {
		name         string
		text         string
		schemaValid  bool
		runtimeValid bool
	}{
		{
			name:         "consumer example",
			text:         text,
			schemaValid:  true,
			runtimeValid: true,
		},
		{
			name:         "omitted retry limits default to zero",
			text:         strings.ReplaceAll(replace("  repair_attempts: 0\n", ""), "  infra_retries: 1\n", ""),
			schemaValid:  true,
			runtimeValid: true,
		},
		{
			name:         "recipe may be omitted",
			text:         strings.Split(text, "recipe:\n")[0],
			schemaValid:  true,
			runtimeValid: true,
		},
		{
			name:         "recipe may be null",
			text:         strings.Split(text, "recipe:\n")[0] + "recipe: null\n",
			schemaValid:  true,
			runtimeValid: true,
		},
		{
			name:         "directory allowlist",
			text:         replace("  - fixture/greeting.go", "  - fixture/"),
			schemaValid:  true,
			runtimeValid: true,
		},
		{
			name: "unknown top level field",
			text: text + "unknown: true\n",
		},
		{
			name: "missing repository",
			text: replace("repository: kevinmartin/sofa-disposable\n", ""),
		},
		{
			name: "unsupported version",
			text: replace("schema_version: 1", "schema_version: 2"),
		},
		{
			name: "invalid repository",
			text: replace("repository: kevinmartin/sofa-disposable", "repository: ../sofa"),
		},
		{
			name: "ID with whitespace",
			text: replace("project_id: REPLACE_PROJECT_NODE_ID", "project_id: project id"),
		},
		{
			name: "oversized ID",
			text: replace("project_id: REPLACE_PROJECT_NODE_ID", "project_id: "+strings.Repeat("x", 129)),
		},
		{
			name: "empty Ready status",
			text: replace("ready_status: Ready", "ready_status: ''"),
		},
		{
			name: "unknown profile field",
			text: replace("  agent: copilot", "  agent: copilot\n  unknown: true"),
		},
		{
			name: "unsupported agent",
			text: replace("agent: copilot", "agent: other"),
		},
		{
			name: "privileged credential",
			text: replace("SOFA_MODEL_TOKEN", "SOFA_PUBLISH_TOKEN"),
		},
		{
			name: "invalid credential name",
			text: replace("SOFA_MODEL_TOKEN", "model-token"),
		},
		{
			name: "unknown limits field",
			text: replace("limits:\n", "limits:\n  unknown: true\n"),
		},
		{
			name: "missing attempt limit",
			text: replace("  attempt_seconds: 600\n", ""),
		},
		{
			name: "attempt above limit",
			text: replace("attempt_seconds: 600", "attempt_seconds: 21601"),
		},
		{
			name: "negative retry limit",
			text: replace("infra_retries: 1", "infra_retries: -1"),
		},
		{
			name: "repair above limit",
			text: replace("repair_attempts: 0", "repair_attempts: 11"),
		},
		{
			name: "zero prompt limit",
			text: replace("max_agent_turns: 1", "max_agent_turns: 0"),
		},
		{
			name: "file count above limit",
			text: replace("max_files: 10", "max_files: 101"),
		},
		{
			name: "file bytes above limit",
			text: replace("max_file_bytes: 131072", "max_file_bytes: 1048577"),
		},
		{
			name: "total bytes above limit",
			text: replace("max_total_bytes: 524288", "max_total_bytes: 10485761"),
		},
		{
			name: "unknown check field",
			text: replace("  - id: go-test", "  - unknown: true\n    id: go-test"),
		},
		{
			name: "invalid check ID",
			text: replace("id: go-test", "id: go.test"),
		},
		{
			name: "empty command",
			text: replace("argv: [go, test, ./...]", "argv: []"),
		},
		{
			name: "empty executable",
			text: replace("argv: [go, test, ./...]", "argv: ['', test]"),
		},
		{
			name: "oversized argument",
			text: replace("argv: [go, test, ./...]", "argv: [go, '"+strings.Repeat("x", 4097)+"']"),
		},
		{
			name: "unbounded check timeout",
			text: replace("timeout_seconds: 120", "timeout_seconds: 0"),
		},
		{
			name: "unknown recipe field",
			text: replace("recipe:\n", "recipe:\n  unknown: true\n"),
		},
		{
			name: "unsupported recipe",
			text: replace("kind: gofmt", "kind: shell"),
		},
		{
			name: "non Go recipe file",
			text: strings.ReplaceAll(text, "fixture/greeting.go", "fixture/greeting.txt"),
		},
		// Standard JSON Schema does not compare sibling values or project
		// fields out of array entries. These must remain runtime rejections.
		{
			name:        "runtime total bytes relation",
			text:        replace("max_total_bytes: 524288", "max_total_bytes: 1"),
			schemaValid: true,
		},
		{
			name:        "runtime check timeout relation",
			text:        replace("timeout_seconds: 120", "timeout_seconds: 601"),
			schemaValid: true,
		},
		{
			name:        "runtime unique check IDs",
			text:        replace("checks:\n", "checks:\n  - id: go-test\n    argv: [go, vet, ./...]\n    timeout_seconds: 120\n"),
			schemaValid: true,
		},
		{
			name:        "runtime recipe allowlist",
			text:        replace("  - fixture/greeting.go", "  - fixture/other.go"),
			schemaValid: true,
		},
		{
			name:        "runtime recipe count relation",
			text:        strings.ReplaceAll(replace("max_files: 10", "max_files: 1"), "    - fixture/greeting.go", "    - fixture/greeting.go\n    - fixture/greeting.go"),
			schemaValid: true,
		},
		{
			name:        "runtime UTF-8 byte length",
			text:        replace("ready_status: Ready", "ready_status: "+strings.Repeat("é", 51)),
			schemaValid: true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var value any
			if err := yaml.Unmarshal([]byte(test.text), &value); err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(value); (err == nil) != test.schemaValid {
				t.Errorf("schema validity = %v, want %v: %v", err == nil, test.schemaValid, err)
			}
			if _, err := Decode(strings.NewReader(test.text)); (err == nil) != test.runtimeValid {
				t.Errorf("runtime validity = %v, want %v: %v", err == nil, test.runtimeValid, err)
			}
		})
	}
}

func TestSchemaPortablePathRules(t *testing.T) {
	schema, err := jsonschema.Compile("../../schemas/sofa.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := yaml.Unmarshal([]byte(fixture(t)), &value); err != nil {
		t.Fatal(err)
	}
	delete(value, "recipe")
	for _, path := range []string{
		"fixture/greeting.go", "fixture/", ".github/workflows/check.yml", "space inside/a.go",
		"../x", "/tmp/x", "x/../y", "x//y", "x\\y", ".GIT/config", ".git:stream",
		"a\n.go", "x/*", "a/./b", "a./b", "a/ b", "a/b ", "a/\u00a0b", "a/b\u3000", "/", "x//",
		strings.Repeat("a", 512), strings.Repeat("a", 512) + "/", strings.Repeat("a", 513),
	} {
		t.Run(path, func(t *testing.T) {
			value["allowed_paths"] = []any{path}
			want := SafePath(strings.TrimSuffix(path, "/"))
			if err := schema.Validate(value); (err == nil) != want {
				t.Errorf("schema path validity = %v, runtime = %v: %v", err == nil, want, err)
			}
		})
	}
}

func TestConsumerEditorSchemaAssociation(t *testing.T) {
	text := fixture(t)
	line := strings.SplitN(text, "\n", 2)[0]
	const prefix = "# yaml-language-server: $schema="
	if !strings.HasPrefix(line, prefix) {
		t.Fatal("consumer example is missing its editor schema association")
	}
	if _, err := os.Stat("../../examples/consumer/" + strings.TrimPrefix(line, prefix)); err != nil {
		t.Fatalf("consumer editor schema does not resolve locally: %v", err)
	}
}
