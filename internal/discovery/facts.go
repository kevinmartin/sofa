package discovery

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

type RepositoryFacts struct {
	ManifestPaths []string `json:"manifest_paths"`
	WorkflowPaths []string `json:"workflow_paths"`
	TestFiles     int      `json:"test_files"`
	SourceFiles   int      `json:"source_files"`
	RelatedIssues []int64  `json:"related_issue_numbers"`
	Truncated     bool     `json:"truncated"`
}

// GatherFacts walks only a checkout's names and modes. It does not execute or
// import repository code, follow symlinks, or send raw source to the model.
// Related issue IDs are exact deterministic duplicate candidates supplied by
// the trusted source scan; semantic duplicate guesses remain model hypotheses.
// It returns JSON and requires an absolute root and at most 100 positive related
// issue numbers. Enumeration and path-list limits set Truncated; filesystem
// errors are returned instead of partial facts.
func GatherFacts(root string, related []int64) (string, error) {
	if !filepath.IsAbs(root) || len(related) > 100 {
		return "", errors.New("invalid bounded repository facts source")
	}
	facts := RepositoryFacts{
		ManifestPaths: []string{},
		WorkflowPaths: []string{},
		RelatedIssues: append([]int64(nil), related...),
	}
	for _, issue := range facts.RelatedIssues {
		if issue < 1 {
			return "", errors.New("invalid related issue")
		}
	}
	sort.Slice(facts.RelatedIssues, func(i, j int) bool { return facts.RelatedIssues[i] < facts.RelatedIssues[j] })
	seen := 0
	pathBytes := 0
	addPath := func(paths *[]string, path string) {
		if len(*paths) >= 100 || pathBytes+len(path) > 32<<10 {
			facts.Truncated = true
			return
		}
		*paths = append(*paths, path)
		pathBytes += len(path)
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == ".git" && entry.IsDir() {
			return filepath.SkipDir
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		seen++
		if seen > 10000 {
			facts.Truncated = true
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".github/workflows/") && (strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml")) {
			addPath(&facts.WorkflowPaths, rel)
		}
		switch entry.Name() {
		case "go.mod", "package.json", "pyproject.toml", "Cargo.toml", "pom.xml", "build.gradle", "requirements.txt":
			addPath(&facts.ManifestPaths, rel)
		}
		if strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, ".test.ts") || strings.HasSuffix(rel, ".spec.ts") || strings.HasSuffix(rel, ".test.js") || strings.HasSuffix(rel, ".spec.js") {
			facts.TestFiles++
		} else if strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, ".ts") || strings.HasSuffix(rel, ".tsx") || strings.HasSuffix(rel, ".js") || strings.HasSuffix(rel, ".py") || strings.HasSuffix(rel, ".rs") {
			facts.SourceFiles++
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(facts.ManifestPaths)
	sort.Strings(facts.WorkflowPaths)
	b, err := json.Marshal(facts)
	if err != nil || len(b) > 64<<10 {
		return "", errors.New("repository facts exceed bound")
	}
	return string(b), nil
}
