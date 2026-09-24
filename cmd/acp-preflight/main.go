// acp-preflight checks Copilot's ACP startup without sending a model prompt.
// It deliberately reports only protocol phases, numeric RPC codes, and fixed
// startup categories: child output and remote error messages are untrusted.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxLine  = 2 << 20
	maxLines = 1000
)

type config struct {
	command   string
	args      []string
	workspace string
	token     string
	timeout   time.Duration
}

type result struct {
	phase   string
	detail  string
	stderr  int64
	classes []string
	ok      bool
}

func (r result) String() string {
	s := fmt.Sprintf("sofa ACP preflight: %s: %s; stderr bytes %d", r.phase, r.detail, r.stderr)
	if len(r.classes) != 0 {
		s += "; child " + strings.Join(r.classes, ", ")
	}
	return s
}

func main() {
	token := os.Getenv("SOFA_MODEL_TOKEN")
	if token == "" {
		fmt.Println("sofa ACP preflight: missing model credential")
		os.Exit(1)
	}
	workspace := os.Getenv("SOFA_WORKSPACE")
	if workspace == "" {
		workspace = "/workspace"
	}
	c := config{workspace: workspace, token: token, timeout: 30 * time.Second}
	if entry := os.Getenv("SOFA_COPILOT_ENTRY"); entry != "" {
		c.command, c.args = "/usr/local/bin/node", []string{entry, "--acp", "--stdio"}
	} else {
		c.command = os.Getenv("SOFA_COPILOT_PATH")
		if c.command == "" {
			c.command = "/copilot/copilot"
		}
		c.args = []string{"--acp", "--stdio"}
	}
	r := run(c)
	fmt.Println(r.String())
	if !r.ok {
		os.Exit(1)
	}
}

func run(c config) result {
	phase := "initialize"
	if c.token == "" {
		return result{phase: phase, detail: "missing model credential"}
	}
	home, err := os.MkdirTemp("/tmp", "sofa-agent-home-")
	if err != nil {
		return result{phase: phase, detail: "temporary home unavailable"}
	}
	defer os.RemoveAll(home)
	cmd := exec.Command(c.command, c.args...)
	cmd.Dir = c.workspace
	cmd.Env = []string{
		"PATH=/toolkit:/copilot:/usr/local/bin:/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home,
		"GITHUB_TOKEN=" + c.token,
	}
	stderr := &stderrSummary{classes: make(map[string]bool)}
	responses := make(chan response, maxLines+1)
	stdout := &lineReader{responses: responses}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result{phase: phase, detail: "process start failed"}
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return result{phase: phase, detail: processStartCategory(err)}
	}
	closed := make(chan error, 1)
	go func() { closed <- cmd.Wait() }()
	finish := func(detail string, ok bool) result {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		<-closed
		count, classes := stderr.snapshot()
		return result{phase: phase, detail: detail, stderr: count, classes: classes, ok: ok}
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
	return result{phase: phase, detail: detail, stderr: count, classes: classes}
}

func send(w io.Writer, id int, method string, params any) error {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

type response struct {
	id       int
	result   json.RawMessage
	rpcError *struct {
		Code *int `json:"code"`
	}
	detail string
}

type lineReader struct {
	mu        sync.Mutex
	buf       []byte
	lines     int
	failed    bool
	responses chan<- response
}

func (r *lineReader) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(p)
	for len(p) > 0 && !r.failed {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			r.appendPart(p)
			break
		}
		r.appendPart(p[:i])
		if !r.failed {
			r.line()
		}
		p = p[i+1:]
	}
	return n, nil
}

func (r *lineReader) appendPart(p []byte) {
	if len(r.buf)+len(p) > maxLine {
		r.fail("protocol limit")
		return
	}
	r.buf = append(r.buf, p...)
}

func (r *lineReader) line() {
	r.lines++
	if r.lines > maxLines {
		r.fail("protocol limit")
		return
	}
	line := bytes.TrimSuffix(r.buf, []byte{'\r'})
	var wire struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code *int `json:"code"`
		} `json:"error"`
	}
	item := response{id: wire.ID}
	if json.Unmarshal(line, &wire) != nil {
		item = response{detail: "invalid JSON"}
	} else {
		item.id, item.result, item.rpcError = wire.ID, wire.Result, wire.Error
	}
	r.responses <- item
	r.buf = r.buf[:0]
}

func (r *lineReader) fail(detail string) {
	r.responses <- response{detail: detail}
	r.failed = true
	r.buf = nil
}

func (r *lineReader) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) > 0 && !r.failed {
		r.line()
	}
}

var osCodes = []string{"ENOSPC", "EACCES", "EPERM", "EROFS", "ENOMEM", "ENOENT"}
var startupCategories = map[string]string{
	"Failed to extract bundled package":        "package extraction failed",
	"ERR_SYSTEM_ERROR":                         "Node system error",
	"Cannot find module":                       "module unavailable",
	"ERR_DLOPEN_FAILED":                        "native module unavailable",
	"cannot open shared object file":           "shared library unavailable",
	"failed to map segment from shared object": "shared library mapping failed",
	"Operation not permitted":                  "operation not permitted",
	"invalid ELF":                              "invalid executable format",
	"GLIBC_":                                   "glibc incompatible",
}

type stderrSummary struct {
	mu      sync.Mutex
	bytes   int64
	tail    string
	classes map[string]bool
}

func (s *stderrSummary) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bytes += int64(len(p))
	chunk := s.tail + string(p)
	for _, code := range osCodes {
		if strings.Contains(chunk, code) {
			s.classes[code] = true
		}
	}
	for pattern, category := range startupCategories {
		if strings.Contains(chunk, pattern) {
			s.classes[category] = true
		}
	}
	const overlap = 64
	if len(chunk) > overlap {
		s.tail = chunk[len(chunk)-overlap:]
	} else {
		s.tail = chunk
	}
	return len(p), nil
}

func (s *stderrSummary) snapshot() (int64, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	classes := make([]string, 0, len(s.classes))
	for category := range s.classes {
		classes = append(classes, category)
	}
	sort.Strings(classes)
	return s.bytes, classes
}

func processStartCategory(err error) string {
	for _, code := range osCodes {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, os.ErrPermission) {
		return "EACCES"
	}
	return "process start failed"
}

func exitDetail(err error) string {
	if err == nil {
		return "connection closed (exit 0)"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() >= 0 {
			return fmt.Sprintf("connection closed (exit %d)", exit.ExitCode())
		}
		return "connection closed (exit signal)"
	}
	return "connection closed (exit unknown)"
}
