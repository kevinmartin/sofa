package agent

import (
	"reflect"
	"testing"
)

func TestCopilotCommandUsesPinnedEntrypointAndTrustedOverrides(t *testing.T) {
	t.Setenv("SOFA_COPILOT_ENTRY", "")
	t.Setenv("SOFA_COPILOT_PATH", "")
	command, args := CopilotCommand()
	if command != "/usr/local/bin/node" || !reflect.DeepEqual(args, []string{"/copilot-package/package/index.js", "--acp", "--stdio"}) {
		t.Fatalf("default entrypoint differs from the checked package: %q %v", command, args)
	}
	t.Setenv("SOFA_COPILOT_PATH", "/trusted/copilot")
	command, args = CopilotCommand()
	if command != "/trusted/copilot" || !reflect.DeepEqual(args, []string{"--acp", "--stdio"}) {
		t.Fatalf("executable override ignored: %q %v", command, args)
	}
	t.Setenv("SOFA_COPILOT_ENTRY", "/trusted/package/index.js")
	command, args = CopilotCommand()
	if command != "/usr/local/bin/node" || !reflect.DeepEqual(args, []string{"/trusted/package/index.js", "--acp", "--stdio"}) {
		t.Fatalf("package override ignored: %q %v", command, args)
	}
}
