package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

// TestACPHelper is a real subprocess speaking ACP JSON-RPC over stdio. It does
// not invoke any provider and reports no fabricated internal model usage.
func TestACPHelper(t *testing.T) {
	if os.Getenv("SOFA_ACP_HELPER") != "1" {
		return
	}
	mode := os.Getenv("SOFA_ACP_MODE")
	if mode == "child" {
		for {
			time.Sleep(time.Second)
		}
	}
	if os.Getenv("SOFA_SECRET_SENTINEL") != "" {
		os.Exit(91)
	}
	scanner := bufio.NewScanner(os.Stdin)
	write := func(v any) {
		if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
			os.Exit(92)
		}
	}
	for scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(93)
		}
		reply := func(result any) { write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}) }
		switch message.Method {
		case "initialize":
			if mode == "malformed" {
				fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":`+string(message.ID)+`,"result":{"protocolVersion":"bad"}}`)
				continue
			}
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{}})
		case "session/new":
			if mode == "auth" {
				write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32000, "message": "SECRET_SHOULD_NEVER_APPEAR"}})
				continue
			}
			reply(map[string]any{"sessionId": "unit-session"})
		case "session/prompt":
			if mode == "quota" {
				write(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32603, "message": "SECRET_SHOULD_NEVER_APPEAR", "data": map[string]any{"code": "quota_exceeded"}}})
				continue
			}
			if mode == "timeout" {
				child := exec.Command(os.Args[0], "-test.run=^TestACPHelper$")
				child.Env = []string{"SOFA_ACP_HELPER=1", "SOFA_ACP_MODE=child"}
				if child.Start() != nil {
					os.Exit(94)
				}
				if os.WriteFile(os.Getenv("SOFA_PID_PATH"), []byte(strconv.Itoa(child.Process.Pid)), 0600) != nil {
					os.Exit(95)
				}
				continue
			}
			write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "unit-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "SECRET_SHOULD_NEVER_APPEAR"}}}})
			write(map[string]any{"jsonrpc": "2.0", "id": "permission-1", "method": "session/request_permission", "params": map[string]any{"sessionId": "unit-session", "toolCall": map[string]any{"toolCallId": "tool-1", "kind": "execute", "title": "run dangerous command"}, "options": []any{map[string]any{"optionId": "yes", "name": "Yes", "kind": "allow_once"}}}})
			if !scanner.Scan() || !strings.Contains(scanner.Text(), `"cancelled"`) {
				os.Exit(96)
			}
			reply(map[string]any{"stopReason": "end_turn"})
		case "session/cancel":
			// Keep the hung process alive: the caller must enforce termination.
		}
	}
	os.Exit(0)
}

func config(t *testing.T, mode string) Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return Config{Command: exe, Args: []string{"-test.run=^TestACPHelper$"}, Dir: dir, Env: []string{"SOFA_ACP_HELPER=1", "SOFA_ACP_MODE=" + mode}, Timeout: 3 * time.Second, AllowedPaths: []string{"hello.go"}}
}

func TestNegotiationStreamingAndPermissionDenial(t *testing.T) {
	t.Setenv("SOFA_SECRET_SENTINEL", "host-credential")
	got, err := Run(context.Background(), config(t, "ok"), "make a fixture change")
	if err != nil {
		t.Fatal(err)
	}
	if got.PromptRequests != 1 || got.ModelCalls != nil || got.Updates != 1 || got.PermissionRequests != 1 || got.PermissionDenials != 1 || got.PermissionExecuteDenials != 1 || got.StopReason != "end_turn" || len(got.SessionID) != 64 {
		t.Fatalf("unexpected metadata: %+v", got)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("raw agent content retained")
	}
}

func TestSessionUpdateCountsOnlyFixedToolCategories(t *testing.T) {
	c := &client{session: "session"}
	sensitive := "SECRET_SHOULD_NEVER_APPEAR"
	for _, kind := range []acp.ToolKind{acp.ToolKindRead, acp.ToolKindEdit, acp.ToolKindExecute, acp.ToolKind("SECRET_KIND_SHOULD_NEVER_APPEAR")} {
		if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: c.session, Update: acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{Kind: kind, Title: sensitive, RawInput: sensitive}}}); err != nil {
			t.Fatal(err)
		}
	}
	failed := acp.ToolCallStatusFailed
	if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: c.session, Update: acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{Status: &failed, Title: &sensitive}}}); err != nil {
		t.Fatal(err)
	}
	if c.updates != 5 || c.toolReads != 1 || c.toolEdits != 1 || c.toolExecutes != 1 || c.toolOthers != 1 || c.toolFailedUpdates != 1 {
		t.Fatalf("unexpected fixed counters: %+v", c)
	}
	if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: c.session, Update: acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{Kind: acp.ToolKindRead, Status: failed}, ToolCallUpdate: &acp.SessionToolCallUpdate{Status: &failed}}}); err != nil || c.updates != 6 || c.toolFailedUpdates != 2 {
		t.Fatal("failed status counted more than once per notification")
	}
	if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: "other"}); !errors.Is(err, ErrProtocol) || c.updates != 6 {
		t.Fatal("cross-session update changed counters")
	}
	c.updates = MaxObservationCount
	if err := c.SessionUpdate(context.Background(), acp.SessionNotification{SessionId: c.session, Update: acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{Kind: acp.ToolKindRead}}}); err != nil || c.updates != MaxObservationCount || c.toolReads != 2 {
		t.Fatal("untrusted update counter exceeded cap")
	}
}
func TestProtocolAuthenticationAndQuotaFailures(t *testing.T) {
	for _, tc := range []struct {
		mode  string
		want  error
		turns int
	}{{"malformed", ErrProtocol, 0}, {"auth", ErrAuthentication, 0}, {"quota", ErrQuota, 1}} {
		t.Run(tc.mode, func(t *testing.T) {
			got, err := Run(context.Background(), config(t, tc.mode), "test")
			if !errors.Is(err, tc.want) || got.PromptRequests != tc.turns {
				t.Fatalf("result %+v, error %v", got, err)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatal("remote secret leaked")
			}
			if tc.mode == "malformed" && !strings.Contains(err.Error(), "during initialize") {
				t.Fatalf("missing safe protocol phase: %v", err)
			}
		})
	}
}

func TestProtocolFailureReportsOnlyFixedStderrCategory(t *testing.T) {
	d := &stderrOSCode{}
	_, _ = d.Write([]byte("SECRET_SHOULD_NEVER_APPEAR: Error: ENOSPC"))
	err := protocolFailure("initialize", io.EOF, d)
	if !errors.Is(err, ErrProtocol) || !strings.Contains(err.Error(), "initialize") || !strings.Contains(err.Error(), "ENOSPC") || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unsafe or incomplete diagnostic: %v", err)
	}
}
func TestTimeoutKillsDescendants(t *testing.T) {
	cfg := config(t, "timeout")
	cfg.Timeout = 400 * time.Millisecond
	pidPath := filepath.Join(cfg.Dir, "child-pid")
	cfg.Env = append(cfg.Env, "SOFA_PID_PATH="+pidPath)
	got, err := Run(context.Background(), cfg, "hang")
	if !errors.Is(err, context.DeadlineExceeded) || got.PromptRequests != 1 {
		t.Fatalf("result %+v, err %v", got, err)
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processStopped(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("agent descendant survived timeout")
}
func processStopped(pid int) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	// Linux containers may have a PID 1 that does not reap orphan zombies.
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err == nil && strings.Contains(string(b), ") Z ")
}
func TestPreCancelledStartsNoPrompt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := Run(ctx, config(t, "ok"), "test")
	if !errors.Is(err, context.Canceled) || got.PromptRequests != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFilesystemScopeAndPermissions(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &client{root: root, session: "s", allowed: map[string]bool{"ok.go": true, "link.go": true, "dir/nested.go": true, "hard.go": true}}
	if err := os.WriteFile(filepath.Join(root, "ok.go"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = c.WriteTextFile(context.Background(), acp.WriteTextFileRequest{SessionId: "s", Path: filepath.Join(root, "ok.go"), Content: "after"})
	if err != nil {
		t.Fatal(err)
	}
	read, err := c.ReadTextFile(context.Background(), acp.ReadTextFileRequest{SessionId: "s", Path: filepath.Join(root, "ok.go")})
	if err != nil || read.Content != "after" {
		t.Fatalf("%+v %v", read, err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "ok.go"), filepath.Join(root, "hard.go")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(outside, "secret"), filepath.Join(root, "link.go"), filepath.Join(root, "dir/nested.go"), filepath.Join(root, "hard.go"), filepath.Join(root, ".git/config"), "relative.go"} {
		if _, err := c.ReadTextFile(context.Background(), acp.ReadTextFileRequest{SessionId: "s", Path: p}); !errors.Is(err, ErrPermission) {
			t.Fatalf("read allowed %q", p)
		}
		if _, err := c.WriteTextFile(context.Background(), acp.WriteTextFileRequest{SessionId: "s", Path: p, Content: "bad"}); !errors.Is(err, ErrPermission) {
			t.Fatalf("write allowed %q", p)
		}
	}
	if err := os.Remove(filepath.Join(root, "hard.go")); err != nil {
		t.Fatal(err)
	}
	kind := acp.ToolKind("edit")
	p := acp.RequestPermissionRequest{SessionId: "s", ToolCall: acp.ToolCallUpdate{Kind: &kind, Locations: []acp.ToolCallLocation{{Path: filepath.Join(root, "ok.go")}}}, Options: []acp.PermissionOption{{Kind: "allow_once", OptionId: "yes"}}}
	answer, err := c.RequestPermission(context.Background(), p)
	if err != nil || answer.Outcome.Selected == nil {
		t.Fatal("scoped edit permission denied")
	}
	p.ToolCall.Locations = []acp.ToolCallLocation{{Path: filepath.Join(root, "link.go")}}
	answer, err = c.RequestPermission(context.Background(), p)
	if err != nil || answer.Outcome.Cancelled == nil {
		t.Fatal("symlink permission allowed")
	}
	p.ToolCall.Locations = nil
	answer, err = c.RequestPermission(context.Background(), p)
	if err != nil || answer.Outcome.Cancelled == nil {
		t.Fatal("unscoped edit permission allowed")
	}
}

func TestCancellationKillsRunningAgent(t *testing.T) {
	cfg := config(t, "timeout")
	pidPath := filepath.Join(cfg.Dir, "child-pid")
	cfg.Env = append(cfg.Env, "SOFA_PID_PATH="+pidPath)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Run(ctx, cfg, "hang"); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidPath); err == nil {
			pid, _ = strconv.Atoi(string(data))
			if pid > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("peer did not start descendant")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected cancellation: %v", err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if processStopped(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("agent descendant survived cancellation")
}

func TestInvalidConfiguration(t *testing.T) {
	for _, p := range []string{"../x", ".git/config", ".github/workflows/x.yml", "AGENTS.md", "dir/.secret", "/tmp/x"} {
		cfg := config(t, "ok")
		cfg.AllowedPaths = []string{p}
		got, err := Run(context.Background(), cfg, "test")
		if !errors.Is(err, ErrConfiguration) || got.PromptRequests != 0 {
			t.Fatalf("allowed %q: %+v %v", p, got, err)
		}
	}
}
