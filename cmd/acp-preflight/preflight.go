package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type config struct {
	command   string
	args      []string
	workspace string
	token     string
	timeout   time.Duration
}

func run(c config) result {
	phase := "initialize"
	if c.token == "" {
		return result{
			phase:  phase,
			detail: "missing model credential",
		}
	}
	home, err := os.MkdirTemp("/tmp", "sofa-agent-home-")
	if err != nil {
		return result{
			phase:  phase,
			detail: "temporary home unavailable",
		}
	}
	defer os.RemoveAll(home)
	cmd := exec.Command(c.command, c.args...)
	cmd.WaitDelay = 2 * time.Second // A descendant may retain the child's output pipes.
	cmd.Dir = c.workspace
	cmd.Env = []string{
		"PATH=/toolkit:/copilot:/usr/local/bin:/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"GITHUB_TOKEN=" + c.token,
	}
	stderr := &stderrSummary{
		classes: make(map[string]bool),
	}
	responses := make(chan response, maxLines+1)
	stdout := &lineReader{
		responses: responses,
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result{
			phase:  phase,
			detail: "process start failed",
		}
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return result{
			phase:  phase,
			detail: processStartCategory(err),
		}
	}
	closed := make(chan error, 1)
	go func() { closed <- cmd.Wait() }()
	finish := func(detail string, ok bool) result {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		<-closed
		count, classes := stderr.snapshot()
		return result{
			phase:   phase,
			detail:  detail,
			stderr:  count,
			classes: classes,
			ok:      ok,
		}
	}
	if err := send(stdin, 1, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientInfo":         map[string]string{"name": "sofa-preflight", "version": "prototype"},
		"clientCapabilities": map[string]any{"fs": map[string]bool{"readTextFile": true, "writeTextFile": true}},
	}); err != nil {
		return finish("connection closed", false)
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	handle := func(item response) (string, bool, bool) {
		if item.detail != "" {
			return item.detail, true, false
		}
		id := 1
		if phase == "session/new" {
			id = 2
		}
		if item.id != id {
			return "", false, false
		}
		if item.rpcError != nil {
			code := "unknown"
			if item.rpcError.Code != nil {
				code = fmt.Sprint(*item.rpcError.Code)
			}
			return "RPC code " + code, true, false
		}
		if len(item.result) == 0 || bytes.Equal(item.result, []byte("null")) {
			return "invalid response", true, false
		}
		if phase == "initialize" {
			var init struct {
				ProtocolVersion int `json:"protocolVersion"`
			}
			if json.Unmarshal(item.result, &init) != nil {
				return "invalid response", true, false
			}
			if init.ProtocolVersion != 1 {
				return "protocol version mismatch", true, false
			}
			phase = "session/new"
			if err := send(stdin, 2, "session/new", map[string]any{"cwd": c.workspace, "mcpServers": []any{}}); err != nil {
				return "connection closed", true, false
			}
			return "", false, false
		}
		var session struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(item.result, &session) != nil || session.SessionID == "" {
			return "invalid response", true, false
		}
		return "accepted without a model prompt", true, true
	}
	for {
		select {
		case item := <-responses:
			if detail, done, ok := handle(item); done {
				return finish(detail, ok)
			}
		case waitErr := <-closed:
			// cmd.Wait has finished copying stdout and stderr. Drain responses
			// produced just before exit before classifying a closed connection.
			stdout.flush()
			for len(responses) > 0 {
				if detail, done, ok := handle(<-responses); done {
					r := closedResult(phase, detail, stderr)
					r.ok = ok
					return r
				}
			}
			_ = stdin.Close()
			return closedResult(phase, exitDetail(waitErr), stderr)
		case <-timer.C:
			return finish("timeout", false)
		}
	}
}

func closedResult(phase, detail string, stderr *stderrSummary) result {
	count, classes := stderr.snapshot()
	return result{
		phase:   phase,
		detail:  detail,
		stderr:  count,
		classes: classes,
	}
}
