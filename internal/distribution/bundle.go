package distribution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Build is used only by the secretless, approved-source build job. Child build
// processes receive an explicit environment that excludes publication tokens.
func Build(ctx context.Context, p Plan, root, output string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	head := buildCommand(ctx, "git", "rev-parse", "HEAD")
	head.Dir = root
	actual, err := head.Output()
	if err != nil || strings.TrimSpace(string(actual)) != p.SourceSHA {
		return errors.New("release checkout does not match requested source")
	}
	dir, err := os.MkdirTemp("", "sofa-release-build-")
	if err != nil {
		return errors.New("prepare release build directory")
	}
	defer os.RemoveAll(dir)
	flags := "-s -w -X github.com/kevinmartin/sofa/internal/version.Version=" + p.Version + " -X github.com/kevinmartin/sofa/internal/version.SourceCommit=" + p.SourceSHA
	files := make(map[string][]byte, 2)
	for _, name := range []string{"sofa", "sofa-test"} {
		command := buildCommand(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", filepath.Join(dir, name), "./cmd/"+name)
		command.Dir = root
		command.Env = []string{"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0", "GOENV=off"}
		for _, key := range []string{"PATH", "HOME", "TMPDIR", "GOCACHE", "GOMODCACHE", "GOPROXY", "GOTOOLCHAIN"} {
			if value := os.Getenv(key); value != "" {
				command.Env = append(command.Env, key+"="+value)
			}
		}
		if err := command.Run(); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("release binary build failed")
		}
		files[name], err = os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return errors.New("read release binary")
		}
	}
	bundle, err := Bundle(p, files)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return errors.New("prepare release bundle directory")
	}
	if err := os.WriteFile(filepath.Join(output, BundleName), bundle, 0600); err != nil {
		return errors.New("write release bundle")
	}
	if err := os.WriteFile(filepath.Join(output, MetadataName), metadataBytes(p), 0600); err != nil {
		return errors.New("write release metadata")
	}
	return nil
}

// Bundle fixes header order/timestamps so an interrupted publication can be
// rebuilt and compared without replacing assets that already reached GitHub.
func Bundle(p Plan, binaries map[string][]byte) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"sofa", "sofa-test", MetadataName} {
		data := binaries[name]
		mode := int64(0755)
		if name == MetadataName {
			data = metadataBytes(p)
			mode = 0644
		}
		if len(data) == 0 || len(data) > 64<<20 {
			return nil, errors.New("release bundle member size invalid")
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     mode,
			Size:     int64(len(data)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			return nil, errors.New("write release bundle header")
		}
		if _, err := tw.Write(data); err != nil {
			return nil, errors.New("write release bundle member")
		}
	}
	if tw.Close() != nil || gz.Close() != nil {
		return nil, errors.New("close release bundle")
	}
	return buffer.Bytes(), nil
}

// ValidateBundle does not execute downloaded artifacts in the credentialed
// publisher. It checks exact members, metadata and Linux amd64 ELF identity.
func ValidateBundle(p Plan, content []byte) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if len(content) == 0 || len(content) > 128<<20 {
		return errors.New("release bundle size invalid")
	}
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return errors.New("release bundle compression invalid")
	}
	defer gz.Close()
	limited := &io.LimitedReader{
		R: gz,
		N: 128<<20 + 1,
	}
	reader := tar.NewReader(limited)
	seen := make(map[string]bool)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("release bundle archive invalid")
		}
		if seen[header.Name] || (header.Name != "sofa" && header.Name != "sofa-test" && header.Name != MetadataName) || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 64<<20 {
			return errors.New("release bundle member invalid")
		}
		seen[header.Name] = true
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return errors.New("release bundle member incomplete")
		}
		if header.Name == MetadataName {
			var metadata Metadata
			if json.Unmarshal(data, &metadata) != nil || metadata != MetadataFor(p) {
				return errors.New("release bundle metadata differs")
			}
			continue
		}
		file, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return errors.New("release binary is not ELF")
		}
		valid := file.Class == elf.ELFCLASS64 && file.Machine == elf.EM_X86_64 && file.Type == elf.ET_EXEC && header.Mode&0111 != 0
		file.Close()
		if !valid {
			return errors.New("release binary platform invalid")
		}
	}
	if len(seen) != 3 {
		return errors.New("release bundle missing a binary or metadata")
	}
	if _, err := io.Copy(io.Discard, limited); err != nil || limited.N == 0 {
		return errors.New("release bundle compression integrity invalid")
	}
	return nil
}
