package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// MemoryStore is a race-safe reference implementation with serialized copies:
// callers cannot mutate committed state through shared maps or pointers.
type MemoryStore struct {
	mu       sync.Mutex
	revision uint64
	data     []byte
}

func (m *MemoryStore) Load(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		return Snapshot{State: Empty()}, nil
	}
	s, err := Decode(m.data)
	return Snapshot{strconv.FormatUint(m.revision, 10), s}, err
}

func (m *MemoryStore) CompareAndSwap(ctx context.Context, expected string, state State) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := Encode(state)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := ""
	if m.data != nil {
		current = strconv.FormatUint(m.revision, 10)
	}
	if expected != current {
		return ErrConflict
	}
	m.revision++
	m.data = data
	return nil
}

// GitStore stores ledger.json on an orphan sofa-state branch in a trusted local
// Git repository. It uses plumbing only, does not check out source or invoke
// hooks, and never force-updates a ref. Network transport belongs to the caller.
type GitStore struct{ Directory string }

const gitRef = "refs/heads/sofa-state"

func (g GitStore) Load(ctx context.Context) (Snapshot, error) {
	rev, err := g.command(ctx, nil, "rev-parse", "--verify", "--quiet", gitRef)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return Snapshot{State: Empty()}, nil
		}
		return Snapshot{}, fmt.Errorf("read state reference: %w", err)
	}
	revision := strings.TrimSpace(string(rev))
	if !shaPattern.MatchString(revision) {
		return Snapshot{}, fmt.Errorf("%w: state reference", ErrInvalid)
	}
	data, err := g.command(ctx, nil, "cat-file", "blob", revision+":ledger.json")
	if err != nil {
		return Snapshot{}, fmt.Errorf("read state blob: %w", err)
	}
	s, err := Decode(data)
	return Snapshot{revision, s}, err
}

func (g GitStore) CompareAndSwap(ctx context.Context, expected string, state State) error {
	if expected != "" && !shaPattern.MatchString(expected) {
		return fmt.Errorf("%w: expected revision", ErrInvalid)
	}
	data, err := Encode(state)
	if err != nil {
		return err
	}
	// Fail early on a lost race; update-ref below remains the atomic check.
	previous, err := g.Load(ctx)
	if err != nil {
		return err
	}
	if previous.Revision != expected {
		return ErrConflict
	}
	blob, err := g.command(ctx, data, "hash-object", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("write state blob: %w", err)
	}
	tree, err := g.treeWithUpdates(ctx, expected, map[string]string{"ledger.json": strings.TrimSpace(string(blob))})
	if err != nil {
		return fmt.Errorf("write state tree: %w", err)
	}
	args := []string{"commit-tree", tree}
	if expected != "" {
		args = append(args, "-p", expected)
	}
	commit, err := g.command(ctx, []byte("sofa ledger v1\n"), args...)
	if err != nil {
		return fmt.Errorf("write state commit: %w", err)
	}
	old := expected
	if old == "" {
		old = strings.Repeat("0", len(strings.TrimSpace(string(commit))))
	}
	_, err = g.command(ctx, nil, "update-ref", gitRef, strings.TrimSpace(string(commit)), old)
	if err != nil {
		actual, loadErr := g.Load(ctx)
		if loadErr == nil && actual.Revision != expected {
			return ErrConflict
		}
		return fmt.Errorf("update state reference: %w", err)
	}
	return nil
}

// SaveSpec appends the approved, bounded canonical specification by digest.
// It preserves the ledger and prior specifications and retries only CAS races.
func (g GitStore) SaveSpec(ctx context.Context, issueID, specDigest string, canonical []byte) error {
	path, err := SpecPath(issueID, specDigest)
	if err != nil || len(canonical) == 0 || len(canonical) > 64<<10 {
		return fmt.Errorf("%w: specification identity or size", ErrInvalid)
	}
	h := sha256.Sum256(canonical)
	if hex.EncodeToString(h[:]) != specDigest {
		return fmt.Errorf("%w: specification digest", ErrInvalid)
	}
	for attempt := 0; attempt < 12; attempt++ {
		snapshot, err := g.Load(ctx)
		if err != nil {
			return err
		}
		if snapshot.Revision != "" {
			prior, err := g.command(ctx, nil, "cat-file", "blob", snapshot.Revision+":"+path)
			if err == nil {
				if bytes.Equal(prior, canonical) {
					return nil
				}
				return fmt.Errorf("%w: immutable specification collision", ErrInvalid)
			}
		}
		blob, err := g.command(ctx, canonical, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		tree, err := g.treeWithUpdates(ctx, snapshot.Revision, map[string]string{path: strings.TrimSpace(string(blob))})
		if err != nil {
			return err
		}
		args := []string{"commit-tree", tree}
		if snapshot.Revision != "" {
			args = append(args, "-p", snapshot.Revision)
		}
		commit, err := g.command(ctx, []byte("sofa specification v1\n"), args...)
		if err != nil {
			return err
		}
		old := snapshot.Revision
		if old == "" {
			old = strings.Repeat("0", len(strings.TrimSpace(string(commit))))
		}
		_, err = g.command(ctx, nil, "update-ref", gitRef, strings.TrimSpace(string(commit)), old)
		if err == nil {
			return nil
		}
		latest, loadErr := g.Load(ctx)
		if loadErr != nil {
			return loadErr
		}
		if latest.Revision == snapshot.Revision {
			return err
		}
	}
	return ErrConflict
}

func (g GitStore) treeWithUpdates(ctx context.Context, expected string, updates map[string]string) (string, error) {
	index, err := os.CreateTemp("", "sofa-git-index-*")
	if err != nil {
		return "", err
	}
	indexPath := index.Name()
	index.Close()
	os.Remove(indexPath)
	defer os.Remove(indexPath)
	if expected == "" {
		_, err = g.commandIndex(ctx, indexPath, nil, "read-tree", "--empty")
	} else {
		_, err = g.commandIndex(ctx, indexPath, nil, "read-tree", expected+"^{tree}")
	}
	if err != nil {
		return "", err
	}
	for path, blob := range updates {
		if path != "ledger.json" && !strings.HasPrefix(path, "specs/") {
			return "", fmt.Errorf("%w: state path", ErrInvalid)
		}
		if !shaPattern.MatchString(blob) {
			return "", fmt.Errorf("%w: state blob", ErrInvalid)
		}
		_, err = g.commandIndex(ctx, indexPath, nil, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
		if err != nil {
			return "", err
		}
	}
	tree, err := g.commandIndex(ctx, indexPath, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(tree)), nil
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 16*1024*1024 {
		return 0, errors.New("git metadata exceeds 16 MiB")
	}
	return b.Buffer.Write(p)
}

func (g GitStore) command(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	return g.commandIndex(ctx, "", input, args...)
}

func (g GitStore) commandIndex(ctx context.Context, indexPath string, input []byte, args ...string) ([]byte, error) {
	if g.Directory == "" {
		return nil, fmt.Errorf("%w: Git directory", ErrInvalid)
	}
	base := []string{"--no-pager", "-C", g.Directory, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.attributesFile=/dev/null", "-c", "commit.gpgsign=false", "-c", "core.pager=cat"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	// Deliberately do not inherit credentials, helpers, Git redirections,
	// external diff commands, signing programs, or user/system configuration.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_AUTHOR_NAME=sofa", "GIT_AUTHOR_EMAIL=sofa@localhost", "GIT_COMMITTER_NAME=sofa", "GIT_COMMITTER_EMAIL=sofa@localhost"}
	if indexPath != "" {
		cmd.Env = append(cmd.Env, "GIT_INDEX_FILE="+indexPath)
	}
	cmd.Stdin = bytes.NewReader(input)
	var output boundedBuffer
	cmd.Stdout = &output
	// Do not surface Git stderr, which can include local paths/configuration.
	err := cmd.Run()
	return output.Bytes(), err
}
