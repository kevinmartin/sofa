package managedconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kevinmartin/sofa/internal/github"
)

type testRoundTrip func(*http.Request) (*http.Response, error)

func (f testRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func clientForServer(t *testing.T, server *httptest.Server) Client {
	t.Helper()
	transport := testRoundTrip(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "api.github.com" {
			t.Fatalf("unexpected GitHub destination: %s", request.URL)
		}
		redirected := request.Clone(request.Context())
		redirected.URL.Scheme = "http"
		redirected.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return http.DefaultTransport.RoundTrip(redirected)
	})
	api, err := github.New("scoped-token", transport)
	if err != nil {
		t.Fatal(err)
	}
	return Client{GitHub: api}
}

func consumerSpec(t *testing.T) Spec {
	t.Helper()
	spec, err := Load("../../managed-repos", "kevinmartin/sofa")
	if err != nil {
		t.Fatal(err)
	}
	spec.Repository = "kevinmartin/example"
	spec.Mode = "consumer"
	spec.Profiles = []string{"typescript", "react"}
	spec.Dependabot.Ecosystems = []string{"npm", "github-actions"}
	return spec
}

func stubActionlint(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "actionlint")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestReconcileReusesExistingConfigPR(t *testing.T) {
	stubActionlint(t)
	spec := consumerSpec(t)
	desired, err := Render(spec)
	if err != nil {
		t.Fatal(err)
	}
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
		}
		switch {
		case r.URL.Path == "/repos/kevinmartin/example":
			fmt.Fprint(w, `{"full_name":"kevinmartin/example","default_branch":"main"}`)
		case strings.HasPrefix(r.URL.Path, "/repos/kevinmartin/example/contents/"):
			if r.URL.Query().Get("ref") == "main" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			path := strings.TrimPrefix(r.URL.Path, "/repos/kevinmartin/example/contents/")
			content := desired[path]
			fmt.Fprintf(w, `{"sha":"file-sha","encoding":"base64","size":%d,"content":%q}`, len(content), base64.StdEncoding.EncodeToString(content))
		case r.URL.Path == "/repos/kevinmartin/example/git/ref/heads/sofa/config":
			fmt.Fprint(w, `{"object":{"sha":"existing-branch"}}`)
		case r.URL.Path == "/repos/kevinmartin/example/compare/main...sofa/config":
			fmt.Fprint(w, `{"files":[{"filename":".github/dependabot.yml"},{"filename":".github/workflows/sofa.quality.yml"}]}`)
		case r.URL.Path == "/repos/kevinmartin/example/pulls":
			fmt.Fprint(w, `[{"html_url":"https://github.com/kevinmartin/example/pull/1"}]`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := clientForServer(t, server)
	result, err := client.Reconcile(context.Background(), spec, true)
	if err != nil || writes != 0 || result.PullURL != "https://github.com/kevinmartin/example/pull/1" {
		t.Fatalf("existing PR not reused: %+v, writes=%d, err=%v", result, writes, err)
	}
}

func TestReconcilePlansAndOpensOneScopedPR(t *testing.T) {
	stubActionlint(t)
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if r.Header.Get("Authorization") != "Bearer scoped-token" {
				t.Error("write without scoped token")
			}
			writes = append(writes, r.Method+" "+r.URL.Path)
		}
		switch {
		case r.URL.Path == "/repos/kevinmartin/example" && r.Method == http.MethodGet:
			fmt.Fprint(w, `{"full_name":"kevinmartin/example","default_branch":"main"}`)
		case strings.HasPrefix(r.URL.Path, "/repos/kevinmartin/example/contents/") && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/kevinmartin/example/git/ref/heads/sofa/config":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/repos/kevinmartin/example/git/ref/heads/main":
			fmt.Fprint(w, `{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		case r.URL.Path == "/repos/kevinmartin/example/git/refs" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/repos/kevinmartin/example/compare/main...sofa/config":
			fmt.Fprint(w, `{"files":[]}`)
		case strings.HasPrefix(r.URL.Path, "/repos/kevinmartin/example/contents/") && r.Method == http.MethodPut:
			var body struct {
				Branch string `json:"branch"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Branch != configBranch {
				t.Error("config write used wrong branch")
			}
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/repos/kevinmartin/example/pulls" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[]`)
		case r.URL.Path == "/repos/kevinmartin/example/pulls" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"html_url":"https://github.com/kevinmartin/example/pull/1"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := clientForServer(t, server)
	plan, err := client.Reconcile(context.Background(), consumerSpec(t), false)
	if err != nil || len(plan.Changes) != 2 || len(writes) != 0 {
		t.Fatalf("dry run changed state: %+v, writes=%v, err=%v", plan, writes, err)
	}
	result, err := client.Reconcile(context.Background(), consumerSpec(t), true)
	if err != nil || len(writes) != 4 || result.PullURL == "" {
		t.Fatalf("apply did not create exactly one PR: %+v, writes=%v, err=%v", result, writes, err)
	}
}

func TestReconcileRefusesUnmanagedFile(t *testing.T) {
	stubActionlint(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unmanaged file led to a write")
		}
		if r.URL.Path == "/repos/kevinmartin/example" {
			fmt.Fprint(w, `{"full_name":"kevinmartin/example","default_branch":"main"}`)
			return
		}
		content := "dmVyc2lvbjogMgo="
		fmt.Fprintf(w, `{"sha":"abc","encoding":"base64","size":11,"content":%q}`, content)
	}))
	defer server.Close()
	client := clientForServer(t, server)
	if _, err := client.Reconcile(context.Background(), consumerSpec(t), true); err == nil {
		t.Fatal("unmanaged repository file was accepted")
	}
}

func TestReconcileApplyRequiresActionlintBeforeGitHub(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	api, err := github.New("scoped-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{GitHub: api}
	if _, err := client.Reconcile(context.Background(), consumerSpec(t), true); err == nil || !strings.Contains(err.Error(), "actionlint") {
		t.Fatalf("apply did not fail before GitHub without actionlint: %v", err)
	}
}
