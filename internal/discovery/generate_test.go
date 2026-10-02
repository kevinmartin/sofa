package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kevinmartin/sofa/internal/agent"
)

func TestGenerateRequiresCompletedValidFileWithoutChangingInputs(t *testing.T) {
	good, err := fixtureSpec().Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		writeSpec  string
		writeIdea  bool
		stopReason string
		wantOK     bool
	}{
		{
			name:       "valid",
			writeSpec:  good,
			stopReason: "end_turn",
			wantOK:     true,
		},
		{
			name:       "partial",
			writeSpec:  "<!-- sofa:specification v1 -->\n## Problem and intended user\nOnly a problem",
			stopReason: "end_turn",
		},
		{
			name:       "source mutation",
			writeSpec:  good,
			writeIdea:  true,
			stopReason: "end_turn",
		},
		{
			name:       "incomplete turn",
			writeSpec:  good,
			stopReason: "max_tokens",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := RunnerFunc(func(_ context.Context, c agent.Config, _ string) (agent.Result, error) {
				if err := os.WriteFile(filepath.Join(c.Dir, "spec.md"), []byte(tc.writeSpec), 0600); err != nil {
					return agent.Result{}, err
				}
				if tc.writeIdea {
					if err := os.WriteFile(filepath.Join(c.Dir, "idea.md"), []byte("changed"), 0600); err != nil {
						return agent.Result{}, err
					}
				}
				return agent.Result{
					StopReason:     tc.stopReason,
					PromptRequests: 1,
				}, nil
			})
			result, err := Generate(context.Background(), GenerateInput{
				Title:      "Greeting",
				Idea:       "A greeting needs a fallback",
				Facts:      "go.mod exists",
				ModelToken: "inert-model-token",
				Timeout:    time.Minute,
				Runner:     runner,
			})
			if (err == nil) != tc.wantOK {
				t.Fatalf("Generate success=%v, want %v; result=%+v, err=%v", err == nil, tc.wantOK, result, err)
			}
			if tc.wantOK && result.Body != good {
				t.Fatal("valid specification not returned")
			}
		})
	}
}
