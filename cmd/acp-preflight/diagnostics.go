package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

type result struct {
	phase   string
	detail  string
	stderr  int64
	classes []string
	ok      bool
}

func (r result) String() string {
	s := fmt.Sprintf("sofa ACP preflight: %s: %s; stderr bytes %d", r.phase, r.detail, r.stderr)
	if len(r.classes) != 0 {
		s += "; child " + strings.Join(r.classes, ", ")
	}
	return s
}

var osCodes = []string{"ENOSPC", "EACCES", "EPERM", "EROFS", "ENOMEM", "ENOENT"}
var startupCategories = map[string]string{
	"Failed to extract bundled package":        "package extraction failed",
	"ERR_SYSTEM_ERROR":                         "Node system error",
	"Cannot find module":                       "module unavailable",
	"ERR_DLOPEN_FAILED":                        "native module unavailable",
	"cannot open shared object file":           "shared library unavailable",
	"failed to map segment from shared object": "shared library mapping failed",
	"Operation not permitted":                  "operation not permitted",
	"invalid ELF":                              "invalid executable format",
	"GLIBC_":                                   "glibc incompatible",
}

type stderrSummary struct {
	mu      sync.Mutex
	bytes   int64
	tail    string
	classes map[string]bool
}

func (s *stderrSummary) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bytes += int64(len(p))
	chunk := s.tail + string(p)
	for _, code := range osCodes {
		if strings.Contains(chunk, code) {
			s.classes[code] = true
		}
	}
	for pattern, category := range startupCategories {
		if strings.Contains(chunk, pattern) {
			s.classes[category] = true
		}
	}
	const overlap = 64
	if len(chunk) > overlap {
		s.tail = chunk[len(chunk)-overlap:]
	} else {
		s.tail = chunk
	}
	return len(p), nil
}

func (s *stderrSummary) snapshot() (int64, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	classes := make([]string, 0, len(s.classes))
	for category := range s.classes {
		classes = append(classes, category)
	}
	sort.Strings(classes)
	return s.bytes, classes
}

func processStartCategory(err error) string {
	for _, code := range osCodes {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return "ENOENT"
	}
	if errors.Is(err, os.ErrPermission) {
		return "EACCES"
	}
	return "process start failed"
}

func exitDetail(err error) string {
	if err == nil {
		return "connection closed (exit 0)"
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if exit.ExitCode() >= 0 {
			return fmt.Sprintf("connection closed (exit %d)", exit.ExitCode())
		}
		return "connection closed (exit signal)"
	}
	return "connection closed (exit unknown)"
}
