package agent

import (
	"os"
)

// CopilotCommand resolves the same ACP entrypoint for preflight and execution.
// The pinned package exposes only file tools to the worker. Overrides are
// supplied by trusted runner configuration, never issue text; test peers use
// the minimal ACP flags instead of Copilot-specific tool controls.
func CopilotCommand() (string, []string) {
	if entry := os.Getenv("SOFA_COPILOT_ENTRY"); entry != "" {
		return "/usr/local/bin/node", []string{entry, "--acp", "--stdio"}
	}
	if executable := os.Getenv("SOFA_COPILOT_PATH"); executable != "" {
		return executable, []string{"--acp", "--stdio"}
	}
	return "/usr/local/bin/node", []string{"/copilot-package/package/index.js", "--acp", "--stdio", "--available-tools=view,edit,apply_patch,grep,glob", "--disable-builtin-mcps"}
}
