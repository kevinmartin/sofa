package integrity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Bundle, Expected, Policy) {
	t.Helper()
	b := Bundle{Version: Version, Repository: "owner/disposable", AttemptID: "work-1", Generation: 2, BaseSHA: strings.Repeat("a", 40), Files: []File{{Path: "src/main.go", Operation: "update", Mode: RegularMode, BeforeSHA256: Hash([]byte("old")), Content: []byte("new")}}}
	if err := Seal(&b); err != nil {
		t.Fatal(err)
	}
	return b, Expected{Repository: b.Repository, AttemptID: b.AttemptID, Generation: b.Generation, BaseSHA: b.BaseSHA, CandidateDigest: b.CandidateDigest}, Policy{AllowedPaths: []string{"src/"}}
}

func TestValidateBindsIdentityAndContents(t *testing.T) {
	b, e, p := fixture(t)
	if err := Validate(b, e, p); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"repository", "attempt", "generation", "base", "content", "mode", "digest", "version"} {
		t.Run(field, func(t *testing.T) {
			b, e, p := fixture(t)
			switch field {
			case "repository":
				b.Repository = "stranger/repo"
			case "attempt":
				b.AttemptID = "work-2"
			case "generation":
				b.Generation++
			case "base":
				b.BaseSHA = strings.Repeat("b", 40)
			case "content":
				b.Files[0].Content = []byte("tampered")
			case "mode":
				b.Files[0].Mode = "120000"
			case "digest":
				b.CandidateDigest = strings.Repeat("0", 64)
			case "version":
				b.Version = 99
			}
			if err := Validate(b, e, p); err == nil {
				t.Fatal("forged candidate accepted")
			}
		})
	}
}

func TestCandidatePathsAndBounds(t *testing.T) {
	paths := []string{"../src/x", "/src/x", "src//x", "src/./x", "src/../x", "src/x\\y", "src/x\ny", "src/x\x00y", "src/%2e%2e/x", "src/.git/config", "src/.gitaliases", "src/.GiT/hooks/post-commit", ".github/workflows/run.yml", "src/action.yml", "src/.sofa/config.json", "src/AGENTS.md", "src/.env.production", "src/a.", "src/COM1.txt", "src/a:b", "src2/x", "src"}
	for _, name := range paths {
		t.Run(name, func(t *testing.T) {
			b, e, p := fixture(t)
			b.Files[0].Path = name
			Seal(&b)
			e.CandidateDigest = b.CandidateDigest
			if err := Validate(b, e, p); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	for _, kind := range []string{"duplicate", "casefold", "parent", "file_limit", "total_limit", "count_limit", "symlink", "submodule", "executable", "unknown_operation", "bad_delete", "missing_preimage", "add_preimage", "empty", "bad_allowlist", "broadening_limit"} {
		t.Run(kind, func(t *testing.T) {
			b, e, p := fixture(t)
			switch kind {
			case "duplicate":
				b.Files = append(b.Files, b.Files[0])
			case "casefold":
				f := b.Files[0]
				f.Path = "src/MAIN.go"
				b.Files = append(b.Files, f)
			case "parent":
				f := b.Files[0]
				f.Path = "src/main.go/file"
				b.Files = append(b.Files, f)
			case "file_limit":
				p.MaxFileBytes = 2
			case "total_limit":
				p.MaxTotalBytes = 2
			case "count_limit":
				p.MaxFiles = 1
				f := b.Files[0]
				f.Path = "src/other.go"
				b.Files = append(b.Files, f)
			case "symlink":
				b.Files[0].Mode = "120000"
			case "submodule":
				b.Files[0].Mode = "160000"
			case "executable":
				b.Files[0].Mode = "100755"
			case "unknown_operation":
				b.Files[0].Operation = "rename"
			case "bad_delete":
				b.Files[0].Operation = "delete"
			case "missing_preimage":
				b.Files[0].BeforeSHA256 = ""
			case "add_preimage":
				b.Files[0].Operation = "add"
			case "empty":
				b.Files = nil
			case "bad_allowlist":
				p.AllowedPaths = []string{"src/**"}
			case "broadening_limit":
				p.MaxFiles = 101
			}
			Seal(&b)
			e.CandidateDigest = b.CandidateDigest
			if err := Validate(b, e, p); err == nil {
				t.Fatal("invalid candidate accepted")
			}
		})
	}
}

func TestProtectedPathsCannotBeAllowlisted(t *testing.T) {
	b, e, p := fixture(t)
	b.Files[0].Path = ".github/workflows/own.yml"
	p.AllowedPaths = []string{b.Files[0].Path}
	Seal(&b)
	e.CandidateDigest = b.CandidateDigest
	if err := Validate(b, e, p); err == nil {
		t.Fatal("workflow change allowed")
	}
}

func TestDecodeRejectsAmbiguousAndOversizedJSON(t *testing.T) {
	b, _, _ := fixture(t)
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(bytes.NewReader(data), MaxEncodedBytes); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(data, []byte("{}")...), []byte(`{"version":1,"version":2}`), []byte(`{"files":[{"mode":"100644","mode":"120000"}]}`), []byte(`{"unexpected":true}`), []byte(`{"files":[{"content":"%%%"}]}`), []byte(`{"files":`), []byte(`null`)} {
		decoded, err := Decode(bytes.NewReader(bad), MaxEncodedBytes)
		// null is structurally valid JSON but the zero-value schema is invalid.
		if string(bad) == "null" {
			_, e, p := fixture(t)
			err = Validate(decoded, e, p)
		}
		if err == nil {
			t.Fatalf("accepted malformed artifact %s", bad)
		}
	}
	if _, err := Decode(bytes.NewReader(data), int64(len(data)-1)); err == nil {
		t.Fatal("accepted oversized artifact")
	}
}

func TestSecretScannerRedactsErrors(t *testing.T) {
	values := []string{"ghp_" + strings.Repeat("A", 36), "github_pat_" + strings.Repeat("B", 24), "AKIA" + strings.Repeat("A", 16), "sk-ant-" + strings.Repeat("c", 24), "-----BEGIN OPENSSH PRIVATE KEY-----", "inert-sensitive-sentinel"}
	for _, secret := range values {
		b, e, p := fixture(t)
		p.ForbiddenValues = [][]byte{[]byte("inert-sensitive-sentinel")}
		b.Files[0].Content = []byte("prefix " + secret + " suffix")
		Seal(&b)
		e.CandidateDigest = b.CandidateDigest
		err := Validate(b, e, p)
		if err == nil {
			t.Fatal("sensitive fixture accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("error disclosed sensitive fixture")
		}
	}
	if err := ScanSecrets([]byte("normal fixture code"), [][]byte{nil}); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyBaseRejectsStaleAndNonRegularTreeEntries(t *testing.T) {
	b, _, _ := fixture(t)
	lookup := func(name string) (BaseFile, bool, error) {
		if name == "src" {
			return BaseFile{Mode: "040000"}, true, nil
		}
		return BaseFile{Mode: RegularMode, Content: []byte("old")}, true, nil
	}
	if err := VerifyBase(b, lookup); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"120000", "160000", "100755"} {
		if err := VerifyBase(b, func(name string) (BaseFile, bool, error) {
			return BaseFile{Mode: mode, Content: []byte("old")}, true, nil
		}); err == nil {
			t.Fatal("unsupported base mode accepted")
		}
	}
	if err := VerifyBase(b, func(name string) (BaseFile, bool, error) {
		if name == "src" {
			return BaseFile{Mode: "040000"}, true, nil
		}
		return BaseFile{Mode: RegularMode, Content: []byte("human change")}, true, nil
	}); err == nil {
		t.Fatal("human update overwritten")
	}
	b.Files[0].Operation = "add"
	b.Files[0].BeforeSHA256 = ""
	if err := VerifyBase(b, lookup); err == nil {
		t.Fatal("existing add accepted")
	}
}

func TestEvidenceRequiresCurrentCompleteTrustedResults(t *testing.T) {
	b, _, _ := fixture(t)
	good := CheckEvidence{Version: Version, Name: "go-test", CandidateDigest: b.CandidateDigest, Passed: true}
	if err := ValidateEvidence(b, []CheckEvidence{good}, []string{"go-test"}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "stale", "failed", "duplicate", "unknown_version"} {
		t.Run(kind, func(t *testing.T) {
			e := []CheckEvidence{good}
			switch kind {
			case "missing":
				e = nil
			case "stale":
				e[0].CandidateDigest = strings.Repeat("b", 64)
			case "failed":
				e[0].Passed = false
			case "duplicate":
				e = append(e, good)
			case "unknown_version":
				e[0].Version = 2
			}
			if err := ValidateEvidence(b, e, []string{"go-test"}); err == nil {
				t.Fatal("invalid check evidence accepted")
			}
		})
	}
}

func TestApplyChecksAllPreimagesBeforeMutationAndRefusesReplay(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src/main.go"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	b, e, p := fixture(t)
	b.Files = append(b.Files, File{Path: "src/other.go", Operation: "update", Mode: RegularMode, BeforeSHA256: Hash([]byte("absent")), Content: []byte("change")})
	Seal(&b)
	e.CandidateDigest = b.CandidateDigest
	if err := Apply(dir, b, e, p); err == nil {
		t.Fatal("missing preimage accepted")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "src/main.go"))
	if string(data) != "old" {
		t.Fatal("partially applied before preimage failure")
	}
	b, e, p = fixture(t)
	if err := Apply(dir, b, e, p); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "src/main.go"))
	if string(data) != "new" {
		t.Fatal("update missing")
	}
	if err := Apply(dir, b, e, p); err == nil {
		t.Fatal("stale replay accepted")
	}
}

func TestSnapshotAndApplyRejectSymlinkFilesAndAncestors(t *testing.T) {
	for _, kind := range []string{"file", "parent"} {
		t.Run(kind, func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "main.go"), []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			if kind == "parent" {
				if err := os.Symlink(outside, filepath.Join(dir, "src")); err != nil {
					t.Fatal(err)
				}
			} else {
				os.Mkdir(filepath.Join(dir, "src"), 0755)
				if err := os.Symlink(filepath.Join(outside, "main.go"), filepath.Join(dir, "src/main.go")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Snapshot(dir, []string{"src/main.go"}); err == nil {
				t.Fatal("symlink snapshot accepted")
			}
			b, e, p := fixture(t)
			if err := Apply(dir, b, e, p); err == nil {
				t.Fatal("symlink write accepted")
			}
			data, _ := os.ReadFile(filepath.Join(outside, "main.go"))
			if string(data) != "old" {
				t.Fatal("outside file changed")
			}
		})
	}
}

func TestChangesAddUpdateDeleteAndApplyWithoutHooks(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src/main.go"), []byte("old"), 0644)
	os.WriteFile(filepath.Join(dir, "src/remove.go"), []byte("remove"), 0644)
	// The hook is inert test data; no subprocess should execute it.
	os.MkdirAll(filepath.Join(dir, ".git/hooks"), 0755)
	hook := []byte("untrusted hook fixture\n")
	os.WriteFile(filepath.Join(dir, ".git/hooks/post-checkout"), hook, 0755)
	paths := []string{"src/main.go", "src/remove.go", "src/deep/new.go"}
	before, err := Snapshot(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	after := map[string]BaseFile{"src/main.go": {Mode: RegularMode, Content: []byte("updated")}, "src/deep/new.go": {Mode: RegularMode, Content: []byte("added")}}
	files, err := Changes(before, after)
	if err != nil {
		t.Fatal(err)
	}
	b, e, p := fixture(t)
	b.Files = files
	Seal(&b)
	e.CandidateDigest = b.CandidateDigest
	if err := Apply(dir, b, e, p); err != nil {
		t.Fatal(err)
	}
	actual, err := Snapshot(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := Changes(after, actual)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("apply differs: %v, %v", remaining, err)
	}
	actualHook, _ := os.ReadFile(filepath.Join(dir, ".git/hooks/post-checkout"))
	if !bytes.Equal(actualHook, hook) {
		t.Fatal("hook touched")
	}
}

func TestDigestCanonicalOrderAndExactEmptyFile(t *testing.T) {
	b, e, p := fixture(t)
	b.Files = append(b.Files, File{Path: "src/empty.go", Operation: "add", Mode: RegularMode})
	Seal(&b)
	e.CandidateDigest = b.CandidateDigest
	if err := Validate(b, e, p); err != nil {
		t.Fatal(err)
	}
	d := b.CandidateDigest
	b.Files[0], b.Files[1] = b.Files[1], b.Files[0]
	Seal(&b)
	if b.CandidateDigest != d {
		t.Fatal("digest depends on file order")
	}
	data, _ := json.Marshal(b)
	decoded, err := Decode(bytes.NewReader(data), MaxEncodedBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(decoded, e, p); err != nil {
		t.Fatal(err)
	}
}

func TestApplyDoesNotMutateHardlinkTarget(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(outside, "original")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(dir, "src/main.go")); err != nil {
		t.Fatal(err)
	}
	b, e, p := fixture(t)
	if err := Apply(dir, b, e, p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "old" {
		t.Fatal("hardlink target changed")
	}
}

func FuzzDecodeCandidate(f *testing.F) {
	f.Add([]byte(`{"version":1,"files":[]}`))
	f.Add([]byte(`{"files":[{"mode":"100644","mode":"120000"}]}`))
	f.Add([]byte(`[[[[]]]]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		b, err := Decode(bytes.NewReader(data), 4096)
		if err == nil {
			_, e, p := fixture(t)
			_ = Validate(b, e, p)
		}
	})
}
