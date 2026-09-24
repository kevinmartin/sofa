// Package agent is the private, bounded ACP boundary. ACP permissions are not an
// operating-system sandbox: callers must isolate the process and its native tools.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

type Config struct {
	Command      string
	Args         []string
	Dir          string
	Env          []string // Exact process environment; never inherits the host environment.
	Timeout      time.Duration
	AllowedPaths []string // Exact repository-relative files. No globs or directories.
}

type Result struct {
	SessionID          string `json:"session_id_hash,omitempty"` // SHA-256, never raw agent-controlled text.
	Updates            int    `json:"updates"`
	PermissionRequests int    `json:"permission_requests"`
	PermissionDenials  int    `json:"permission_denials"`
	PromptRequests     int    `json:"prompt_requests"` // ACP turns attempted, not provider model calls.
	ModelCalls         *int   `json:"model_calls"`     // Unknown: ACP does not expose internal inference count.
	StopReason         string `json:"stop_reason,omitempty"`
}

var (
	ErrConfiguration  = errors.New("agent: invalid configuration")
	ErrStart          = errors.New("agent: process startup failed")
	ErrProtocol       = errors.New("agent: protocol failure")
	ErrAuthentication = errors.New("agent: authentication required")
	ErrQuota          = errors.New("agent: quota exhausted")
	ErrPermission     = errors.New("agent: filesystem operation denied")
)

// Run starts one session and one prompt. Raw protocol output, stderr, file
// contents and remote error messages are never retained in result/error logs.
func Run(parent context.Context, cfg Config, prompt string) (result Result, retErr error) {
	if cfg.Timeout <= 0 || cfg.Timeout > 6*time.Hour || !filepath.IsAbs(cfg.Dir) || prompt == "" || len(prompt) > 1<<20 {
		return result, ErrConfiguration
	}
	root, err := filepath.EvalSymlinks(cfg.Dir)
	if err != nil {
		return result, ErrConfiguration
	}
	allowed := map[string]bool{}
	for _, p := range cfg.AllowedPaths {
		if !safeRelative(p) {
			return result, ErrConfiguration
		}
		allowed[p] = true
	}
	if len(allowed) == 0 {
		return result, ErrConfiguration
	}
	for _, e := range cfg.Env {
		if strings.IndexByte(e, '=') < 1 || strings.IndexByte(e, 0) >= 0 {
			return result, ErrConfiguration
		}
	}
	command := cfg.Command
	args := cfg.Args
	if command == "" {
		command = "copilot"
		if args == nil {
			args = []string{"--acp", "--stdio"}
		}
	}
	// Resolve the executable from the explicit environment, never host PATH or cwd.
	if !filepath.IsAbs(command) {
		if strings.ContainsAny(command, `/\\`) {
			return result, ErrConfiguration
		}
		var path string
		for _, e := range cfg.Env {
			if strings.HasPrefix(e, "PATH=") {
				path = strings.TrimPrefix(e, "PATH=")
			}
		}
		command = resolveExecutable(command, path)
		if command == "" {
			return result, ErrStart
		}
	}
	ctx, cancel := context.WithTimeout(parent, cfg.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	cmd := exec.Command(command, args...)
	cmd.Dir = root
	cmd.Env = append([]string{}, cfg.Env...)
	stderr := &stderrOSCode{}
	cmd.Stderr = stderr
	configureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, ErrStart
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return result, ErrStart
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return result, ErrStart
	}
	defer func() { killProcessGroup(cmd); stdin.Close(); stdout.Close(); _ = cmd.Wait() }()
	c := &client{root: root, allowed: allowed}
	conn, err := acp.NewClientSideConnectionWithOptions(c, stdin, stdout, acp.ConnectionOptions{
		MaxFrameSize: 2 << 20, MaxPendingRequests: 8, MaxHandlerConcurrency: 4,
		MaxQueuedRequests: 16, MaxQueuedNotifications: 64, MaxNotificationBytes: 4 << 20,
		MaxQueuedWrites: 32, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return result, protocolFailure("connection setup", err, stderr)
	}
	defer func() {
		_ = conn.Close()
		wait, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = conn.Wait(wait)
	}()
	defer func() {
		c.mu.Lock()
		result.Updates = c.updates
		result.PermissionRequests = c.permissions
		result.PermissionDenials = c.denials
		c.mu.Unlock()
	}()
	init, err := conn.Initialize(ctx, acp.InitializeRequest{ProtocolVersion: acp.ProtocolVersionNumber, ClientInfo: &acp.Implementation{Name: "sofa", Version: "prototype"}, ClientCapabilities: acp.ClientCapabilities{Fs: acp.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true}}})
	if err != nil {
		return result, classify(ctx, "initialize", err, stderr)
	}
	if init.ProtocolVersion != acp.ProtocolVersionNumber {
		return result, protocolFailure("initialize", nil, stderr)
	}
	session, err := conn.NewSessionWithResponseHook(ctx, acp.NewSessionRequest{Cwd: root, McpServers: []acp.McpServer{}}, func(_ context.Context, s acp.NewSessionResponse) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.session = s.SessionId
		return nil
	})
	if err != nil {
		return result, classify(ctx, "session/new", err, stderr)
	}
	if len(session.SessionId) == 0 || len(session.SessionId) > 256 {
		return result, protocolFailure("session/new", nil, stderr)
	}
	id := sha256.Sum256([]byte(session.SessionId))
	result.SessionID = hex.EncodeToString(id[:])
	result.PromptRequests++
	response, err := conn.Prompt(ctx, acp.PromptRequest{SessionId: session.SessionId, Prompt: []acp.ContentBlock{acp.TextBlock(prompt)}})
	if err != nil {
		if ctx.Err() != nil {
			stop, done := context.WithTimeout(context.Background(), 250*time.Millisecond)
			_ = conn.Cancel(stop, acp.CancelNotification{SessionId: session.SessionId})
			done()
		}
		return result, classify(ctx, "session/prompt", err, stderr)
	}
	switch string(response.StopReason) {
	case "end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled":
		result.StopReason = string(response.StopReason)
	default:
		return result, protocolFailure("session/prompt", nil, stderr)
	}
	return result, nil
}

func resolveExecutable(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			return p
		}
	}
	return ""
}

func classify(ctx context.Context, stage string, err error, stderr *stderrOSCode) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var remote *acp.RequestError
	if errors.As(err, &remote) {
		if remote.Code == -32000 {
			return ErrAuthentication
		}
		// Only recognized structured codes select retry categories. Remote messages
		// can contain secrets or adversarial instructions and are never emitted.
		if data, ok := remote.Data.(map[string]any); ok {
			if code, ok := data["code"].(string); ok {
				switch code {
				case "quota_exceeded", "rate_limit_exceeded":
					return ErrQuota
				case "authentication_required", "unauthorized":
					return ErrAuthentication
				}
			}
		}
	}
	return protocolFailure(stage, err, stderr)
}

// stderrOSCode keeps only a fixed operating-system error category. Copilot's
// stderr may contain credentials or untrusted text, so neither its bytes nor
// remote RPC messages are returned to the caller.
type stderrOSCode struct {
	mu   sync.Mutex
	code string
}

func (d *stderrOSCode) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.code == "" {
		for _, code := range []string{"ENOSPC", "EACCES", "EPERM", "EROFS", "ENOMEM", "ENOENT"} {
			if strings.Contains(string(p), code) {
				d.code = code
				break
			}
		}
	}
	return len(p), nil
}

func (d *stderrOSCode) Code() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.code
}

func protocolFailure(stage string, err error, stderr *stderrOSCode) error {
	category := "invalid response"
	if err != nil {
		category = "transport"
		var remote *acp.RequestError
		if errors.As(err, &remote) {
			category = fmt.Sprintf("RPC code %d", remote.Code)
		} else if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			category = "connection closed"
		}
	}
	if code := stderr.Code(); code != "" {
		category += ", child " + code
	}
	return fmt.Errorf("%w during %s (%s)", ErrProtocol, stage, category)
}

type client struct {
	mu                            sync.Mutex
	root                          string
	allowed                       map[string]bool
	session                       acp.SessionId
	updates, permissions, denials int
}

func (c *client) SessionUpdate(_ context.Context, p acp.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p.SessionId != c.session {
		return ErrProtocol
	}
	c.updates++
	return nil
}
func (c *client) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.permissions++
	allow := ctx.Err() == nil && p.SessionId == c.session && p.ToolCall.Kind != nil && (*p.ToolCall.Kind == "read" || *p.ToolCall.Kind == "edit") && len(p.ToolCall.Locations) > 0
	for _, loc := range p.ToolCall.Locations {
		if !c.permissionPath(loc.Path, p.ToolCall.Kind != nil && *p.ToolCall.Kind == "edit") {
			allow = false
		}
	}
	if allow {
		for _, o := range p.Options {
			if o.Kind == "allow_once" {
				return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Selected: &acp.RequestPermissionOutcomeSelected{OptionId: o.OptionId}}}, nil
			}
		}
	}
	c.denials++
	return acp.RequestPermissionResponse{Outcome: acp.RequestPermissionOutcome{Cancelled: &acp.RequestPermissionOutcomeCancelled{}}}, nil
}

func safeRelative(p string) bool {
	if p == "" || filepath.IsAbs(p) || filepath.Clean(p) != p || p == "." || strings.ContainsAny(p, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || strings.HasPrefix(part, ".") || part == "AGENTS.md" || part == "SKILL.md" {
			return false
		}
	}
	return true
}
func (c *client) relative(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrPermission
	}
	rel, err := filepath.Rel(c.root, path)
	if err != nil || !safeRelative(rel) || !c.allowed[rel] {
		return "", ErrPermission
	}
	return rel, nil
}
