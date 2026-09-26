// acp-preflight checks Copilot's ACP startup without sending a model prompt.
// It deliberately reports only protocol phases, numeric RPC codes, and fixed
// startup categories: child output and remote error messages are untrusted.
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if err := executePreflightCommand(newRootCommand(run), os.Args[1:]); err != nil {
		if !errors.Is(err, errPreflightFailed) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
