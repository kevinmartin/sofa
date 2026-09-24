// Package worker creates candidate data in a disposable, unprivileged checkout.
// Publication and authoritative state changes belong to other jobs.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/agent"
	"github.com/kevinmartin/sofa/internal/config"
	"github.com/kevinmartin/sofa/internal/integrity"
)

type Runner interface {
	Run(context.Context, agent.Config, string) (agent.Result, error)
}

var ErrValidation = errors.New("worker: candidate validation failed")

type RunnerFunc func(context.Context, agent.Config, string) (agent.Result, error)

func (f RunnerFunc) Run(ctx context.Context, c agent.Config, p string) (agent.Result, error) {
	return f(ctx, c, p)
}

type NativeRunner struct{}

func (NativeRunner) Run(ctx context.Context, c agent.Config, p string) (agent.Result, error) {
	return agent.Run(ctx, c, p)
}

type Input struct {
	Config        config.Config
	CanonicalSpec []byte
	Directory     string
	AttemptID     string
	Generation    uint64
	BaseSHA       string
	ModelToken    string
	Runner        Runner
}
type Result struct {
	Bundle                   integrity.Bundle `json:"bundle"`
	UsedAgent                bool             `json:"used_agent"`
	PromptRequests           int              `json:"prompt_requests"`
	Updates                  int              `json:"updates"`
	PermissionRequests       int              `json:"permission_requests"`
	PermissionDenials        int              `json:"permission_denials"`
	PermissionExecuteDenials int              `json:"permission_execute_denials"`
	ToolReads                int              `json:"tool_reads"`
	ToolEdits                int              `json:"tool_edits"`
	ToolExecutes             int              `json:"tool_executes"`
	ToolOthers               int              `json:"tool_others"`
	ToolFailedUpdates        int              `json:"tool_failed_updates"`
	ModelCalls               *int             `json:"model_calls"`
	NoChange                 bool             `json:"no_change"`
}

func Execute(ctx context.Context, in Input) (Result, error) {
	var out Result
	if err := in.Config.Validate(); err != nil {
		return out, fmt.Errorf("%w: configuration", ErrValidation)
	}
	if in.Directory == "" || in.AttemptID == "" || in.Generation == 0 || in.BaseSHA == "" {
		return out, fmt.Errorf("%w: identity", ErrValidation)
	}
	var spec struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal(in.CanonicalSpec, &spec); err != nil || spec.Title == "" || spec.Body == "" {
		return out, fmt.Errorf("%w: specification", ErrValidation)
	}
	beforePaths, err := allowedFiles(in.Directory, in.Config)
	if err != nil {
		return out, fmt.Errorf("%w: approved files", ErrValidation)
	}
	before, err := integrity.Snapshot(in.Directory, beforePaths)
	if err != nil {
		return out, fmt.Errorf("%w: base snapshot", ErrValidation)
	}
	recipe := in.Config.Recipe != nil && strings.Contains(spec.Body, "<!-- sofa:recipe=gofmt -->")
	if recipe {
		if err := runRecipe(ctx, in, before); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("%w: recipe", ErrValidation)
		}
	} else {
		if strings.TrimSpace(in.ModelToken) == "" {
			return out, agent.ErrAuthentication
		}
		runner := in.Runner
		if runner == nil {
			runner = NativeRunner{}
		}
		allowed := append([]string(nil), beforePaths...)
		if len(allowed) == 0 {
			return out, fmt.Errorf("%w: no approved existing files", ErrValidation)
		}
		dir, err := os.MkdirTemp("", "sofa-agent-home-")
		if err != nil {
			return out, errors.New("cannot create isolated agent home")
		}
		defer os.RemoveAll(dir)
		ac := agent.Config{
			Command:      "/usr/local/bin/node",
			Args:         []string{"/copilot-package/package/index.js", "--acp", "--stdio"},
			Dir:          in.Directory,
			Env:          []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "GITHUB_TOKEN=" + in.ModelToken},
			Timeout:      time.Duration(in.Config.Limits.AttemptSeconds) * time.Second,
			AllowedPaths: allowed,
		}
		prompt := fmt.Sprintf("Implement the approved issue in this disposable checkout. Title: %s\nSpecification: %s\nEdit only these approved existing files: %s\nUse file read/edit capabilities to make the actual source change; shell execution permission is unavailable. Do not stop at proposing a patch. Do not alter tests or policy to make a failure appear green. Return a completed ACP turn after the change.", spec.Title, spec.Body, strings.Join(allowed, ", "))
		result, err := runner.Run(ctx, ac, prompt)
		// A failed ACP turn can still have sent a prompt. Preserve the bounded
		// accounting returned by the adapter without retaining agent text.
		out.UsedAgent = true
		out.PromptRequests = result.PromptRequests
		out.Updates = result.Updates
		out.PermissionRequests = result.PermissionRequests
		out.PermissionDenials = result.PermissionDenials
		out.PermissionExecuteDenials = result.PermissionExecuteDenials
		out.ToolReads = result.ToolReads
		out.ToolEdits = result.ToolEdits
		out.ToolExecutes = result.ToolExecutes
		out.ToolOthers = result.ToolOthers
		out.ToolFailedUpdates = result.ToolFailedUpdates
		out.ModelCalls = result.ModelCalls
		if err != nil {
			return out, err
		}
		if result.StopReason != "end_turn" {
			return out, fmt.Errorf("%w: agent did not complete requested turn", ErrValidation)
		}
	}
	afterPaths, err := allowedFiles(in.Directory, in.Config)
	if err != nil {
		return out, fmt.Errorf("%w: resulting files", ErrValidation)
	}
	after, err := integrity.Snapshot(in.Directory, afterPaths)
	if err != nil {
		return out, fmt.Errorf("%w: resulting snapshot", ErrValidation)
	}
	files, err := integrity.Changes(before, after)
	if err != nil {
		return out, fmt.Errorf("%w: candidate changes", ErrValidation)
	}
	if len(files) == 0 {
		out.NoChange = true
		return out, nil
	}
	b := integrity.Bundle{Version: integrity.Version, Repository: in.Config.Repository, AttemptID: in.AttemptID, Generation: in.Generation, BaseSHA: in.BaseSHA, Files: files}
	if err := integrity.Seal(&b); err != nil {
		return out, fmt.Errorf("%w: candidate seal", ErrValidation)
	}
	e := integrity.Expected{Repository: b.Repository, AttemptID: b.AttemptID, Generation: b.Generation, BaseSHA: b.BaseSHA, CandidateDigest: b.CandidateDigest}
	p := integrity.Policy{AllowedPaths: in.Config.AllowedPaths, MaxFiles: in.Config.Limits.MaxFiles, MaxFileBytes: in.Config.Limits.MaxFileBytes, MaxTotalBytes: in.Config.Limits.MaxTotalBytes, ForbiddenValues: [][]byte{[]byte(in.ModelToken)}}
	if err := integrity.Validate(b, e, p); err != nil {
		return out, fmt.Errorf("%w: candidate integrity", ErrValidation)
	}
	out.Bundle = b
	return out, nil
}

func allowedFiles(root string, c config.Config) ([]string, error) {
	set := map[string]bool{}
	for _, rule := range c.AllowedPaths {
		base := strings.TrimSuffix(rule, "/")
		location := filepath.Join(root, filepath.FromSlash(base))
		if strings.HasSuffix(rule, "/") {
			err := filepath.WalkDir(location, func(p string, d os.DirEntry, walkErr error) error {
				if os.IsNotExist(walkErr) {
					return nil
				}
				if walkErr != nil {
					return walkErr
				}
				if d.Type()&os.ModeSymlink != 0 {
					return errors.New("approved directory contains symlink")
				}
				if d.IsDir() {
					return nil
				}
				if !d.Type().IsRegular() {
					return errors.New("approved directory contains unsupported file")
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				rel = filepath.ToSlash(rel)
				if !c.Allows(rel) {
					return errors.New("approved directory contains excluded file")
				}
				set[rel] = true
				if len(set) > c.Limits.MaxFiles {
					return errors.New("approved file count exceeds limit")
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			info, err := os.Lstat(location)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				return nil, errors.New("approved file is not regular")
			}
			set[base] = true
		}
	}
	files := make([]string, 0, len(set))
	for p := range set {
		files = append(files, p)
	}
	sort.Strings(files)
	return files, nil
}

func runRecipe(ctx context.Context, in Input, before map[string]integrity.BaseFile) error {
	for _, name := range in.Config.Recipe.Paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		base, ok := before[name]
		if !ok || base.Mode != integrity.RegularMode {
			return errors.New("recipe preconditions not satisfied")
		}
		formatted, err := format.Source(base.Content)
		if err != nil {
			return errors.New("recipe input is not valid Go")
		}
		if string(formatted) == string(base.Content) {
			continue
		}
		b := integrity.Bundle{Version: integrity.Version, Repository: in.Config.Repository, AttemptID: in.AttemptID, Generation: in.Generation, BaseSHA: in.BaseSHA, Files: []integrity.File{{Path: name, Operation: "update", Mode: integrity.RegularMode, BeforeSHA256: integrity.Hash(base.Content), Content: formatted}}}
		if err := integrity.Seal(&b); err != nil {
			return err
		}
		e := integrity.Expected{Repository: b.Repository, AttemptID: b.AttemptID, Generation: b.Generation, BaseSHA: b.BaseSHA, CandidateDigest: b.CandidateDigest}
		p := integrity.Policy{AllowedPaths: in.Config.AllowedPaths, MaxFiles: in.Config.Limits.MaxFiles, MaxFileBytes: in.Config.Limits.MaxFileBytes, MaxTotalBytes: in.Config.Limits.MaxTotalBytes}
		if err := integrity.Apply(in.Directory, b, e, p); err != nil {
			return err
		}
	}
	return nil
}
