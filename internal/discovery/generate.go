package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/agent"
)

type Runner interface {
	Run(context.Context, agent.Config, string) (agent.Result, error)
}

type RunnerFunc func(context.Context, agent.Config, string) (agent.Result, error)

func (f RunnerFunc) Run(ctx context.Context, config agent.Config, prompt string) (agent.Result, error) {
	return f(ctx, config, prompt)
}

type NativeRunner struct{}

func (NativeRunner) Run(ctx context.Context, config agent.Config, prompt string) (agent.Result, error) {
	return agent.Run(ctx, config, prompt)
}

// GenerateInput contains only the admitted idea and bounded deterministic
// repository facts. Neither Project nor publisher credentials enter this job.
type GenerateInput struct {
	Title      string
	Idea       string
	Facts      string
	ModelToken string
	Timeout    time.Duration
	Runner     Runner
}

type GenerateResult struct {
	Body      string
	Telemetry agent.Result
}

const draftTemplate = `<!-- sofa:specification v1 -->

## Problem and intended user

## Evidence and reproducer

## Goals

## Non-goals

## Constraints and affected components

## Dependencies

## Acceptance examples

## Proposed validation

## Risks

## Unanswered questions

## Delivery slices

`

// Generate runs one isolated ACP turn to fill an existing specification file.
// It validates the file, not the model's prose response. The caller must first
// reserve a persistent Discovery claim and recheck its fence before publishing.
func Generate(ctx context.Context, in GenerateInput) (GenerateResult, error) {
	var out GenerateResult
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Idea) == "" || len(in.Title) > 1024 || len(in.Idea) > 64<<10 || len(in.Facts) > 64<<10 || strings.TrimSpace(in.ModelToken) == "" || in.Timeout <= 0 || in.Timeout > 6*time.Hour {
		return out, errors.New("invalid bounded Discovery input")
	}
	directory, err := os.MkdirTemp("", "sofa-discovery-")
	if err != nil {
		return out, errors.New("cannot create Discovery workspace")
	}
	defer os.RemoveAll(directory)
	inputs := map[string]string{
		"idea.md":  "# " + in.Title + "\n\n" + in.Idea + "\n",
		"facts.md": in.Facts + "\n",
		"spec.md":  draftTemplate,
	}
	for name, content := range inputs {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			return out, errors.New("cannot prepare Discovery workspace")
		}
	}
	home, err := os.MkdirTemp("", "sofa-discovery-home-")
	if err != nil {
		return out, errors.New("cannot create Discovery agent home")
	}
	defer os.RemoveAll(home)
	command, args := agent.CopilotCommand()
	config := agent.Config{
		Command:      command,
		Args:         args,
		Dir:          directory,
		Env:          []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home, "GITHUB_TOKEN=" + in.ModelToken},
		Timeout:      in.Timeout,
		AllowedPaths: []string{"idea.md", "facts.md", "spec.md"},
	}
	runner := in.Runner
	if runner == nil {
		runner = NativeRunner{}
	}
	prompt := "Research the admitted idea using idea.md and the deterministic repository facts in facts.md. Fill every section of spec.md with specific evidence, goals, non-goals, dependencies, acceptance examples including negative behavior, proposed validation, risks, unknowns, and bounded delivery slices. Mark unverified claims as hypotheses. Do not change idea.md or facts.md. Do not edit repository code, publish a PR, change a Project status, or claim owner approval. Finish the ACP turn after editing spec.md."
	result, err := runner.Run(ctx, config, prompt)
	out.Telemetry = result
	if err != nil {
		return out, err
	}
	if result.StopReason != "end_turn" {
		return out, errors.New("discovery agent did not complete its turn")
	}
	for _, name := range []string{"idea.md", "facts.md"} {
		info, err := os.Lstat(filepath.Join(directory, name))
		if err != nil || !info.Mode().IsRegular() {
			return out, errors.New("discovery agent replaced immutable input")
		}
		current, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil || string(current) != inputs[name] {
			return out, errors.New("discovery agent changed immutable inputs")
		}
	}
	info, err := os.Lstat(filepath.Join(directory, "spec.md"))
	if err != nil || !info.Mode().IsRegular() {
		return out, errors.New("discovery agent replaced specification file")
	}
	content, err := os.ReadFile(filepath.Join(directory, "spec.md"))
	if err != nil || len(content) > 64<<10 || string(content) == draftTemplate {
		return out, errors.New("discovery agent did not produce a bounded specification")
	}
	if _, err := Parse(string(content)); err != nil {
		return out, fmt.Errorf("invalid Discovery specification: %w", err)
	}
	out.Body = string(content)
	return out, nil
}
