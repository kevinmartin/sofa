package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPreflightHelper(t *testing.T) {
	mode := os.Getenv("GITHUB_TOKEN")
	if !strings.HasPrefix(mode, "test-secret-") {
		return
	}
	if mode == "test-secret-hold" {
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	in := bufio.NewScanner(os.Stdin)
	if !in.Scan() || !strings.Contains(in.Text(), `"method":"initialize"`) {
		os.Exit(9)
	}
	switch mode {
	case "test-secret-descendant":
		child := exec.Command(os.Args[0], "-test.run=^TestPreflightHelper$")
		child.Env = []string{"GITHUB_TOKEN=test-secret-hold"}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if child.Start() != nil || os.WriteFile("preflight-child.pid", []byte(strconv.Itoa(child.Process.Pid)), 0600) != nil {
			os.Exit(9)
		}
		os.Exit(1)
	case "test-secret-error":
		fmt.Fprint(os.Stderr, "credential test-secret-error; ERR_DLOPEN_FAILED; Cannot find module\n")
		fmt.Println(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"test-secret-error"}}`)
		os.Exit(1)
	case "test-secret-closed":
		fmt.Fprint(os.Stderr, "credential test-secret-closed; ER")
		fmt.Fprint(os.Stderr, "OFS\n")
		os.Exit(1)
	case "test-secret-success":
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}`)
		if !in.Scan() || !strings.Contains(in.Text(), `"method":"session/new"`) {
			os.Exit(9)
		}
		fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"sessionId":"test-secret-session"}}`)
		// The parent must terminate here; any prompt would be a probe failure.
		if in.Scan() {
			os.Exit(9)
		}
		os.Exit(0)
	}
	os.Exit(9)
}

func helperConfig(t *testing.T, mode string) config {
	t.Helper()
	return config{
		command: os.Args[0], args: []string{"-test.run=TestPreflightHelper"},
		workspace: t.TempDir(), token: "test-secret-" + mode, timeout: 3 * time.Second,
	}
}

func TestRunZeroPrompt(t *testing.T) {
	r := run(helperConfig(t, "success"))
	if !r.ok || r.phase != "session/new" || r.detail != "accepted without a model prompt" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if strings.Contains(r.String(), "test-secret") {
		t.Fatal("secret leaked in result")
	}
}

func TestRunRPCErrorRedactsChildMessages(t *testing.T) {
	r := run(helperConfig(t, "error"))
	if r.ok || r.phase != "initialize" || r.detail != "RPC code -32000" {
		t.Fatalf("unexpected result: %+v", r)
	}
	if !strings.Contains(r.String(), "module unavailable") || !strings.Contains(r.String(), "native module unavailable") {
		t.Fatalf("missing fixed categories: %+v", r)
	}
	if strings.Contains(r.String(), "test-secret") || strings.Contains(r.String(), "credential") {
		t.Fatal("child text leaked in result")
	}
}

func TestRunClosedReportsFixedCategory(t *testing.T) {
	r := run(helperConfig(t, "closed"))
	if r.ok || r.phase != "initialize" || !strings.Contains(r.detail, "connection closed") {
		t.Fatalf("unexpected result: %+v", r)
	}
	if !strings.Contains(r.String(), "EROFS") || strings.Contains(r.String(), "credential") {
		t.Fatalf("unsafe result: %+v", r)
	}
}

func TestPreflightWaitIsBoundedWhenDescendantHoldsOutputPipes(t *testing.T) {
	c := helperConfig(t, "descendant")
	c.timeout = 100 * time.Millisecond
	started := time.Now()
	r := run(c)
	if r.ok || time.Since(started) > 5*time.Second {
		t.Fatalf("preflight waited for inherited pipes: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(c.workspace, "preflight-child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
		_ = process.Release()
	}
}

func TestStderrCategoryAcrossWrites(t *testing.T) {
	s := &stderrSummary{classes: make(map[string]bool)}
	_, _ = s.Write([]byte("ERR_DLO"))
	_, _ = s.Write([]byte("PEN_FAILED"))
	n, classes := s.snapshot()
	if n != int64(len("ERR_DLOPEN_FAILED")) || len(classes) != 1 || classes[0] != "native module unavailable" {
		t.Fatalf("unexpected summary: bytes=%d classes=%v", n, classes)
	}
}
