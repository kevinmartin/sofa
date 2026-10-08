package setupcli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeState struct {
	Promoted     map[string]string `json:"promoted"`
	Releases     []release         `json:"releases"`
	SourceSHA    string
	SourceDirty  bool
	BuildFailure bool
}

func TestMain(m *testing.M) {
	if os.Getenv("SOFA_SETUP_FAKE_TOOL") != "" {
		os.Exit(fakeTool(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func fakeTool(args []string) int {
	call := filepath.Base(os.Args[0])
	for i, arg := range args {
		if i == 2 {
			break
		}
		call += ":" + arg
	}
	if trace := os.Getenv("SOFA_FAKE_TRACE"); trace != "" {
		file, err := os.OpenFile(trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return 2
		}
		if _, err := fmt.Fprintln(file, call); err != nil {
			file.Close()
			return 2
		}
		if err := file.Close(); err != nil {
			return 2
		}
	}
	switch filepath.Base(os.Args[0]) {
	case "uname":
		if len(args) == 1 && args[0] == "-s" {
			fmt.Println("Linux")
			return 0
		}
		if len(args) == 1 && args[0] == "-m" {
			fmt.Println("x86_64")
			return 0
		}
	case "tar":
		return fakeTar(args)
	case "gh":
		return fakeGH(args)
	case "git", "go":
		return fakeSourceTool(filepath.Base(os.Args[0]), args)
	}
	fmt.Fprintln(os.Stderr, "unexpected fake tool call", filepath.Base(os.Args[0]), args)
	return 2
}

func fakeGH(args []string) int {
	data, err := os.ReadFile(os.Getenv("SOFA_FAKE_STATE"))
	if err != nil {
		return 2
	}
	var state fakeState
	if json.Unmarshal(data, &state) != nil {
		return 2
	}
	if len(args) == 0 {
		return 2
	}
	if args[0] == "api" {
		endpoint := ""
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
				break
			}
		}
		if strings.HasPrefix(endpoint, "repos/kevinmartin/sofa/commits/") {
			ref := strings.TrimPrefix(endpoint, "repos/kevinmartin/sofa/commits/")
			if sha := state.Promoted[ref]; sha != "" {
				fmt.Println(sha)
				return 0
			}
			for _, release := range state.Releases {
				if release.Tag == ref {
					fmt.Println(release.SHA)
					return 0
				}
			}
			return 1
		}
		if strings.HasPrefix(endpoint, "repos/kevinmartin/sofa/releases/tags/") {
			ref := strings.TrimPrefix(endpoint, "repos/kevinmartin/sofa/releases/tags/")
			for _, release := range state.Releases {
				if release.Tag == ref {
					json.NewEncoder(os.Stdout).Encode(map[string]any{"tag_name": ref, "draft": release.Draft, "prerelease": release.Prerelease, "immutable": release.Immutable})
					return 0
				}
			}
			return 1
		}
		if endpoint == "repos/kevinmartin/sofa/releases?per_page=100" {
			for _, release := range state.Releases {
				if !release.Draft && !release.Prerelease {
					fmt.Println(release.Tag)
				}
			}
			return 0
		}
	}
	if len(args) >= 3 && args[0] == "release" {
		ref := args[2]
		for _, release := range state.Releases {
			if release.Tag != ref {
				continue
			}
			switch args[1] {
			case "download":
				for i := 3; i+1 < len(args); i++ {
					if args[i] == "--dir" {
						content, err := os.ReadFile(release.Bundle)
						if err != nil {
							return 1
						}
						if os.WriteFile(filepath.Join(args[i+1], "sofa-linux-amd64.tar.gz"), content, 0600) != nil {
							return 1
						}
						return 0
					}
				}
			case "verify-asset":
				if len(args) < 4 {
					return 2
				}
				if !release.Attested {
					fmt.Fprintln(os.Stderr, "attestation unavailable")
					return 1
				}
				content, err := os.ReadFile(args[3])
				if err != nil {
					return 1
				}
				digest := sha256.Sum256(content)
				if hex.EncodeToString(digest[:]) != release.Digest {
					fmt.Fprintln(os.Stderr, "verification failed")
					return 1
				}
				return 0
			}
		}
	}
	fmt.Fprintln(os.Stderr, "unexpected gh call", args)
	return 2
}

func fakeTar(args []string) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("tar (GNU tar) test double")
		return 0
	}
	archive := ""
	directory := ""
	verbose := false
	extract := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			i++
			if i < len(args) {
				archive = args[i]
			}
		case "--directory":
			i++
			if i < len(args) {
				directory = args[i]
			}
		case "--verbose":
			verbose = true
		case "--extract":
			extract = true
		}
	}
	file, err := os.Open(archive)
	if err != nil {
		return 1
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return 1
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return 0
		}
		if err != nil {
			return 1
		}
		if extract {
			// Only safe bundles reach extraction in the test. Never follow links.
			if h.Typeflag != tar.TypeReg || strings.Contains(h.Name, "..") || strings.Contains(h.Name, "/") {
				return 1
			}
			content, err := io.ReadAll(tr)
			if err != nil || os.WriteFile(filepath.Join(directory, h.Name), content, 0600) != nil {
				return 1
			}
			continue
		}
		if verbose {
			kind := byte('-')
			if h.Typeflag == tar.TypeSymlink {
				kind = 'l'
			} else if h.Typeflag == tar.TypeLink {
				kind = 'h'
			}
			fmt.Printf("%crwxr-xr-x test test %d Jan 1 00:00 %s\n", kind, h.Size, h.Name)
		} else {
			escaped := strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\t", "\\t", "\r", "\\r").Replace(h.Name)
			fmt.Println(escaped)
		}
	}
}

func fakeSourceTool(tool string, args []string) int {
	data, err := os.ReadFile(os.Getenv("SOFA_FAKE_STATE"))
	if err != nil {
		return 2
	}
	var state fakeState
	if json.Unmarshal(data, &state) != nil {
		return 2
	}
	if tool == "git" && len(args) >= 4 {
		switch args[2] {
		case "rev-parse":
			fmt.Println(state.SourceSHA)
			return 0
		case "status":
			if state.SourceDirty {
				fmt.Println(" M go.mod")
			}
			return 0
		}
	}
	if tool != "go" || len(args) < 2 || args[0] != "build" {
		return 2
	}
	if os.Getenv("GH_TOKEN") != "" || os.Getenv("GITHUB_TOKEN") != "" || os.Getenv("CGO_ENABLED") != "0" || os.Getenv("GOOS") != "linux" || os.Getenv("GOARCH") != "amd64" {
		return 2
	}
	for i, arg := range args {
		if arg != "-o" || i+1 >= len(args) {
			continue
		}
		output := args[i+1]
		binary := filepath.Base(output)
		if state.BuildFailure && binary == "sofa-test" {
			return 1
		}
		script := "#!/bin/sh\nprintf '%s\\n' '" + binary + "-development'\n"
		if os.WriteFile(output, []byte(script), 0755) != nil {
			return 2
		}
		return 0
	}
	return 2
}
