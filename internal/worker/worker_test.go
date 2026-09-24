package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/agent"
	"github.com/kevinmartin/sofa/internal/config"
)

func workerFixture(t *testing.T) (Input, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "fixture", "main.go")
	if err := os.WriteFile(path, []byte("package fixture\n\nfunc Greet() string {return \"old\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := config.Config{Version: 1, Repository: "owner/repo", RepositoryID: "R_1", ProjectID: "P_1", OwnerID: "U_1", ReadyStatus: "Ready", AllowedPaths: []string{"fixture/"}, Profile: config.Profile{Agent: "copilot", SecretEnv: "GITHUB_TOKEN"}, Limits: config.Limits{AttemptSeconds: 60, RepairAttempts: 2, InfraRetries: 2, MaxAgentTurns: 2, MaxFiles: 5, MaxFileBytes: 4096, MaxTotalBytes: 8192}, Checks: []config.Check{{ID: "go-test", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 30}}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	spec, _ := json.Marshal(map[string]string{"title": "Change fixture", "body": "Make Greet return hello"})
	return Input{Config: c, CanonicalSpec: spec, Directory: root, AttemptID: strings.Repeat("f", 64), Generation: 1, BaseSHA: strings.Repeat("a", 40), ModelToken: "model-token"}, path
}

func TestAgentChangeProducesBoundedCandidate(t *testing.T) {
	t.Setenv("SOFA_COPILOT_ENTRY", "")
	t.Setenv("SOFA_COPILOT_PATH", "")
	in, path := workerFixture(t)
	in.Runner = RunnerFunc(func(_ context.Context, c agent.Config, p string) (agent.Result, error) {
		if len(c.AllowedPaths) != 1 || c.AllowedPaths[0] != "fixture/main.go" || !strings.Contains(p, "Make Greet return hello") {
			t.Fatal("worker did not constrain prompt and allowed files")
		}
		if c.Command != "/usr/local/bin/node" || strings.Join(c.Args, " ") != "/copilot-package/package/index.js --acp --stdio" {
			t.Fatal("worker did not use the checked Copilot package entrypoint")
		}
		for _, v := range c.Env {
			if strings.Contains(v, "SOFA_PUBLISH_TOKEN") || strings.Contains(v, "SOFA_PROJECTS_TOKEN") {
				t.Fatal("privileged credential entered agent environment")
			}
		}
		if err := os.WriteFile(path, []byte("package fixture\n\nfunc Greet() string {return \"hello\"}\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return agent.Result{StopReason: "end_turn", PromptRequests: 1, Updates: 5, PermissionRequests: 2, PermissionDenials: 1, PermissionExecuteDenials: 1, ToolReads: 1, ToolEdits: 1, ToolExecutes: 1, ToolOthers: 1, ToolFailedUpdates: 1}, nil
	})
	out, err := Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.UsedAgent || out.PromptRequests != 1 || out.ModelCalls != nil || out.Updates != 5 || out.PermissionRequests != 2 || out.PermissionDenials != 1 || out.PermissionExecuteDenials != 1 || out.ToolReads != 1 || out.ToolEdits != 1 || out.ToolExecutes != 1 || out.ToolOthers != 1 || out.ToolFailedUpdates != 1 || len(out.Bundle.Files) != 1 || out.Bundle.Files[0].Path != "fixture/main.go" {
		t.Fatal("incorrect candidate evidence")
	}
}

func TestRecipeBypassesAgentAndRequiresPreconditions(t *testing.T) {
	in, path := workerFixture(t)
	in.Config.Recipe = &config.Recipe{Kind: "gofmt", Paths: []string{"fixture/main.go"}}
	in.CanonicalSpec, _ = json.Marshal(map[string]string{"title": "Format Go", "body": "Format existing fixture.\n<!-- sofa:recipe=gofmt -->"})
	calls := 0
	in.Runner = RunnerFunc(func(context.Context, agent.Config, string) (agent.Result, error) { calls++; return agent.Result{}, nil })
	out, err := Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || out.UsedAgent || out.ModelCalls != nil || len(out.Bundle.Files) != 1 {
		t.Fatal("recipe invoked model or did not produce change")
	}
	out, err = Execute(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NoChange || calls != 0 {
		t.Fatal("repeat recipe failed zero-inference no-op")
	}
	if err := os.WriteFile(path, []byte("package fixture\nfunc ("), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), in); err == nil || calls != 0 {
		t.Fatal("invalid recipe input did not fail closed")
	}
}

func TestRefusedAgentTurnCannotBecomeCandidate(t *testing.T) {
	in, _ := workerFixture(t)
	in.Runner = RunnerFunc(func(context.Context, agent.Config, string) (agent.Result, error) {
		return agent.Result{StopReason: "refusal"}, nil
	})
	if _, err := Execute(context.Background(), in); err == nil {
		t.Fatal("refused turn accepted")
	}
}

func TestFailedAgentTurnRetainsPromptAccounting(t *testing.T) {
	in, _ := workerFixture(t)
	in.Runner = RunnerFunc(func(context.Context, agent.Config, string) (agent.Result, error) {
		return agent.Result{PromptRequests: 1, Updates: 2, PermissionRequests: 1, PermissionDenials: 1, PermissionExecuteDenials: 1, ToolExecutes: 1, ToolFailedUpdates: 1}, agent.ErrQuota
	})
	out, err := Execute(context.Background(), in)
	if err != agent.ErrQuota || !out.UsedAgent || out.PromptRequests != 1 || out.Updates != 2 || out.PermissionRequests != 1 || out.PermissionDenials != 1 || out.PermissionExecuteDenials != 1 || out.ToolExecutes != 1 || out.ToolFailedUpdates != 1 || len(out.Bundle.Files) != 0 {
		t.Fatalf("failed ACP turn lost bounded accounting: %+v, %v", out, err)
	}
}
