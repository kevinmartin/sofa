package integrity

import (
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// Snapshot reads bounded regular files from a quiescent workspace (the agent
// process/container must already be stopped). Absent files are omitted. Root
// confinement prevents escape even if an outside process changes a symlink;
// callers must prevent concurrent mutation for a consistent snapshot.
func Snapshot(directory string, paths []string) (map[string]BaseFile, error) {
	r, err := os.OpenRoot(directory)
	if err != nil {
		return nil, errors.New("cannot open candidate workspace")
	}
	defer r.Close()
	return snapshot(r, paths)
}

func snapshot(r *os.Root, paths []string) (map[string]BaseFile, error) {
	if len(paths) == 0 || len(paths) > 100 {
		return nil, errors.New("invalid snapshot path count")
	}
	files := map[string]BaseFile{}
	seen := map[string]bool{}
	total := 0
	for _, name := range paths {
		if err := SafePath(name); err != nil {
			return nil, err
		}
		if seen[strings.ToLower(name)] {
			return nil, errors.New("duplicate snapshot path")
		}
		seen[strings.ToLower(name)] = true
		if err := safeParents(r, name); err != nil {
			return nil, err
		}
		info, err := r.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 != 0 || info.Size() > 1<<20 {
			return nil, errors.New("unsupported snapshot file")
		}
		f, err := r.Open(name)
		if err != nil {
			return nil, errors.New("cannot read snapshot file")
		}
		openedInfo, statErr := f.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
			f.Close()
			return nil, errors.New("snapshot file changed during read")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || len(data) > 1<<20 {
			return nil, errors.New("cannot read bounded snapshot file")
		}
		total += len(data)
		if total > 10<<20 {
			return nil, errors.New("snapshot exceeds total limit")
		}
		files[name] = BaseFile{Mode: RegularMode, Content: data}
	}
	return files, nil
}

func safeParents(r *os.Root, name string) error {
	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		info, err := r.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return errors.New("workspace path parent is not a directory")
		}
	}
	return nil
}

// Changes builds a complete file-content transport from two bounded snapshots.
// The caller supplies admitted identity, then seals and validates the bundle.
func Changes(before, after map[string]BaseFile) ([]File, error) {
	names := map[string]bool{}
	for name := range before {
		names[name] = true
	}
	for name := range after {
		names[name] = true
	}
	files := []File{}
	for name := range names {
		if err := SafePath(name); err != nil {
			return nil, err
		}
		old, existed := before[name]
		next, exists := after[name]
		if (existed && old.Mode != RegularMode) || (exists && next.Mode != RegularMode) {
			return nil, errors.New("unsupported snapshot file mode")
		}
		f := File{Path: name, Mode: RegularMode}
		switch {
		case !existed:
			f.Operation, f.Content = "add", append([]byte(nil), next.Content...)
		case !exists:
			f.Operation, f.BeforeSHA256 = "delete", Hash(old.Content)
		case Hash(old.Content) != Hash(next.Content):
			f.Operation, f.BeforeSHA256, f.Content = "update", Hash(old.Content), append([]byte(nil), next.Content...)
		default:
			continue
		}
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// Apply is only for secretless disposable verifier workspaces, never privileged
// publication. It refuses stale/already-applied inputs, verifies ALL preimages
// before writing, and executes no hooks or code. Stop all workspace writers
// before calling; this is not an atomic multi-file filesystem transaction.
func Apply(directory string, b Bundle, e Expected, p Policy) error {
	if err := Validate(b, e, p); err != nil {
		return err
	}
	r, err := os.OpenRoot(directory)
	if err != nil {
		return errors.New("cannot open verifier workspace")
	}
	defer r.Close()
	names := make([]string, len(b.Files))
	for i, f := range b.Files {
		names[i] = f.Path
	}
	before, err := snapshot(r, names)
	if err != nil {
		return err
	}
	lookup := func(name string) (BaseFile, bool, error) {
		if base, ok := before[name]; ok {
			return base, true, nil
		}
		info, err := r.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			return BaseFile{}, false, nil
		}
		if err != nil {
			return BaseFile{}, false, err
		}
		if info.IsDir() {
			return BaseFile{Mode: "040000"}, true, nil
		}
		return BaseFile{Mode: "unsupported"}, true, nil
	}
	if err := VerifyBase(b, lookup); err != nil {
		return err
	}
	for _, change := range b.Files {
		if err := safeParents(r, change.Path); err != nil {
			return err
		}
		if change.Operation == "delete" {
			if err := r.Remove(change.Path); err != nil {
				return errors.New("cannot delete verifier file")
			}
			continue
		}
		parts := strings.Split(path.Dir(change.Path), "/")
		parent := ""
		for _, part := range parts {
			if part == "." {
				continue
			}
			parent = path.Join(parent, part)
			if err := r.Mkdir(parent, 0755); err != nil && !errors.Is(err, os.ErrExist) {
				return errors.New("cannot create verifier directory")
			}
			info, err := r.Lstat(parent)
			if err != nil || !info.IsDir() {
				return errors.New("unsafe verifier directory")
			}
		}
		// Replace the directory entry rather than truncate an existing inode:
		// a pre-existing hardlink must not modify another path's contents.
		if change.Operation == "update" {
			if err := r.Remove(change.Path); err != nil {
				return errors.New("cannot replace verifier file")
			}
		}
		f, err := r.OpenFile(change.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return errors.New("cannot open verifier file")
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			f.Close()
			return errors.New("unsupported verifier file")
		}
		_, writeErr := f.Write(change.Content)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			return errors.New("cannot write verifier file")
		}
	}
	return nil
}
