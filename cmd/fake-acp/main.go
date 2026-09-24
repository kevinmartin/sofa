// fake-acp is a deterministic, test-only ACP peer. It has no provider client.
// Hosted canaries build it from an exact sofa candidate commit and run it only
// inside the networkless disposable worker with an inert token.
package main

import (
	"context"
	"errors"
	"go/format"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// mode is set only when trusted test code builds this binary. Neither issue
// content nor the normal consumer profile can choose a scenario.
var mode = "edit"

type peer struct {
	connection *acp.AgentSideConnection
	ready      chan struct{}
	mu         sync.RWMutex
	cwd        string
}

func (p *peer) Initialize(_ context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if mode == "protocol" {
		return acp.InitializeResponse{ProtocolVersion: 999}, nil
	}
	return acp.InitializeResponse{ProtocolVersion: request.ProtocolVersion, AuthMethods: []acp.AuthMethod{}, AgentInfo: &acp.Implementation{Name: "sofa-fake-acp", Version: "1"}}, nil
}

func (p *peer) NewSession(_ context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if mode == "auth" {
		return acp.NewSessionResponse{}, &acp.RequestError{Code: -32000, Message: "authentication required"}
	}
	p.mu.Lock()
	p.cwd = request.Cwd
	p.mu.Unlock()
	return acp.NewSessionResponse{SessionId: "sofa-fake-session"}, nil
}

func (p *peer) Cancel(context.Context, acp.CancelNotification) error { return nil }

func (p *peer) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	select {
	case <-p.ready:
	case <-ctx.Done():
		return acp.PromptResponse{}, ctx.Err()
	}
	if request.SessionId != "sofa-fake-session" {
		return acp.PromptResponse{}, errors.New("invalid session")
	}
	switch mode {
	case "no-change":
		return acp.PromptResponse{StopReason: "end_turn"}, nil
	case "refusal":
		return acp.PromptResponse{StopReason: "refusal"}, nil
	case "quota":
		return acp.PromptResponse{}, &acp.RequestError{Code: -32603, Message: "quota", Data: map[string]any{"code": "quota_exceeded"}}
	case "timeout":
		<-ctx.Done()
		return acp.PromptResponse{}, ctx.Err()
	case "edit":
		p.mu.RLock()
		path := filepath.Join(p.cwd, "fixture", "greeting.go")
		p.mu.RUnlock()
		read, err := p.connection.ReadTextFile(ctx, acp.ReadTextFileRequest{SessionId: request.SessionId, Path: path})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		formatted, err := format.Source([]byte(read.Content))
		if err != nil {
			return acp.PromptResponse{}, err
		}
		if string(formatted) == read.Content {
			return acp.PromptResponse{}, errors.New("fixture was already formatted")
		}
		_, err = p.connection.WriteTextFile(ctx, acp.WriteTextFileRequest{SessionId: request.SessionId, Path: path, Content: string(formatted)})
		if err != nil {
			return acp.PromptResponse{}, err
		}
		return acp.PromptResponse{StopReason: "end_turn"}, nil
	default:
		return acp.PromptResponse{}, errors.New("unsupported fake scenario")
	}
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--acp" || os.Args[2] != "--stdio" || os.Getenv("GITHUB_TOKEN") != "sofa-fake-acp-inert-token" {
		os.Exit(2)
	}
	p := &peer{ready: make(chan struct{})}
	conn, err := stdio.NewAgentConnection(p, acp.ConnectionOptions{
		MaxFrameSize: 2 << 20, MaxPendingRequests: 8, MaxHandlerConcurrency: 4,
		MaxQueuedRequests: 16, MaxQueuedNotifications: 64, MaxNotificationBytes: 4 << 20,
		MaxQueuedWrites: 32, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		os.Exit(2)
	}
	p.connection = conn
	close(p.ready)
	defer conn.Close()
	if err := conn.Wait(context.Background()); err != nil {
		os.Exit(1)
	}
}
