package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestBridgeListensForFastWorkflowName(t *testing.T) {
	read := func(name string, out any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("../../.github/workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	var fast struct {
		Name string `yaml:"name"`
	}
	var bridgeWorkflow struct {
		On struct {
			WorkflowRun struct {
				Workflows []string `yaml:"workflows"`
			} `yaml:"workflow_run"`
		} `yaml:"on"`
	}
	read("pr-fast.yml", &fast)
	read("pr-gate-bridge.yml", &bridgeWorkflow)
	if fast.Name != fastWorkflowName {
		t.Fatalf("bridge event name %q differs from fast workflow %q", fastWorkflowName, fast.Name)
	}
	if len(bridgeWorkflow.On.WorkflowRun.Workflows) != 1 || bridgeWorkflow.On.WorkflowRun.Workflows[0] != fastWorkflowName {
		t.Fatalf("workflow_run listens for %q, want %q", bridgeWorkflow.On.WorkflowRun.Workflows, fastWorkflowName)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Result(), nil
	})}
}

const (
	testCandidateSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testBaseSHA      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func testEvent(numbers ...int) []byte {
	prs := make([]map[string]int, 0, len(numbers))
	for _, number := range numbers {
		prs = append(prs, map[string]int{"number": number})
	}
	data, _ := json.Marshal(map[string]any{
		"action":     "completed",
		"repository": map[string]any{"full_name": sofaRepository},
		"workflow_run": map[string]any{
			"id": 1234, "run_attempt": 2, "event": "pull_request",
			"name": fastWorkflowName, "status": "completed",
			"pull_requests": prs,
		},
	})
	return data
}

func testAppKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestBridgeDispatchesOnlyCurrentExactPRPairWithNarrowAppToken(t *testing.T) {
	key := testAppKey(t)
	var dispatches, policyStatuses int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kevinmartin/sofa/pulls/2":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer read-token" {
				t.Error("PR read did not use read-only workflow token")
			}
			fmt.Fprintf(w, `{"number":2,"state":"open","head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"kevinmartin/sofa"}}}`, testCandidateSHA, testBaseSHA)
		case "/repos/kevinmartin/sofa/contents/.github/workflows/pr-fast.yml", "/repos/kevinmartin/sofa/contents/.github/workflows/quality.reusable.yml":
			if r.URL.Query().Get("ref") != testCandidateSHA || r.Header.Get("Authorization") != "Bearer read-token" {
				t.Error("quality policy did not read exact candidate with read token")
			}
			data, err := os.ReadFile(filepath.Join("../../.github/workflows", filepath.Base(r.URL.Path)))
			if err != nil {
				t.Fatal(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "size": len(data), "content": base64.StdEncoding.EncodeToString(data)})
		case "/repos/kevinmartin/sofa-disposable/installation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer eyJ") {
				t.Error("installation discovery did not use App JWT")
			}
			fmt.Fprint(w, `{"id":42}`)
		case "/repos/kevinmartin/sofa/installation":
			fmt.Fprint(w, `{"id":43}`)
		case "/app/installations/43/access_tokens":
			var request struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Repositories) != 1 || request.Repositories[0] != "sofa" || request.Permissions["statuses"] != "write" || len(request.Permissions) != 2 {
				t.Errorf("sofa status token not scoped: %+v", request)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"status-token"}`)
		case "/repos/kevinmartin/sofa/statuses/" + testCandidateSHA:
			policyStatuses++
			var request struct {
				State, Context string
			}
			if r.Header.Get("Authorization") != "Bearer status-token" || json.NewDecoder(r.Body).Decode(&request) != nil || request.State != "success" || request.Context != "sofa / quality-policy" {
				t.Errorf("unexpected trusted quality status: %+v", request)
			}
			w.WriteHeader(http.StatusCreated)
		case "/app/installations/42/access_tokens":
			var request struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.Repositories) != 1 || request.Repositories[0] != "sofa-disposable" || len(request.Permissions) != 2 || request.Permissions["actions"] != "write" || request.Permissions["metadata"] != "read" {
				t.Errorf("App token was not restricted to disposable Actions: %+v", request)
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"dispatch-token"}`)
		case "/repos/kevinmartin/sofa-disposable/actions/workflows/sofa-gate.yml/dispatches":
			dispatches++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer dispatch-token" {
				t.Error("dispatch did not use scoped App installation token")
			}
			var request struct {
				Ref    string            `json:"ref"`
				Inputs map[string]string `json:"inputs"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			want := map[string]string{"sofa_pr": "2", "candidate_sha": testCandidateSHA, "base_sha": testBaseSHA, "source_run_id": "1234", "source_run_attempt": "2"}
			if request.Ref != "main" || len(request.Inputs) != len(want) {
				t.Errorf("unexpected dispatch: %+v", request)
			}
			for k, v := range want {
				if request.Inputs[k] != v {
					t.Errorf("dispatch %s = %q, want %q", k, request.Inputs[k], v)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	b := bridge{client: testClient(handler), apiURL: "https://api.test", readToken: "read-token", appID: "123", keyPEM: key, now: time.Now}
	if err := b.run(context.Background(), testEvent(2, 2)); err != nil {
		t.Fatal(err)
	}
	if dispatches != 1 || policyStatuses != 1 {
		t.Fatalf("got %d dispatches and %d policy statuses, want one each", dispatches, policyStatuses)
	}
}

func TestBridgeDoesNotDispatchClosedOrUnassociatedPR(t *testing.T) {
	var calls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprintf(w, `{"number":2,"state":"closed","head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"kevinmartin/sofa"}}}`, testCandidateSHA, testBaseSHA)
	})
	b := bridge{client: testClient(handler), apiURL: "https://api.test", readToken: "read-token"}
	if err := b.run(context.Background(), testEvent()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("unassociated run made an API request")
	}
	if err := b.run(context.Background(), testEvent(2)); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("closed PR made %d API requests, want only the current PR read", calls)
	}
}

func TestBridgePublishesFailureForWeakenedQuality(t *testing.T) {
	key := testAppKey(t)
	var failed bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/kevinmartin/sofa/pulls/2":
			fmt.Fprintf(w, `{"number":2,"state":"open","head":{"sha":%q},"base":{"sha":%q,"repo":{"full_name":"kevinmartin/sofa"}}}`, testCandidateSHA, testBaseSHA)
		case "/repos/kevinmartin/sofa/contents/.github/workflows/pr-fast.yml", "/repos/kevinmartin/sofa/contents/.github/workflows/quality.reusable.yml":
			name := filepath.Base(r.URL.Path)
			data, err := os.ReadFile(filepath.Join("../../.github/workflows", name))
			if err != nil {
				t.Fatal(err)
			}
			if name == "quality.reusable.yml" {
				data = []byte(strings.Replace(string(data), "go test -count=1 ./...", "true", 1))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "size": len(data), "content": base64.StdEncoding.EncodeToString(data)})
		case "/repos/kevinmartin/sofa/installation":
			fmt.Fprint(w, `{"id":43}`)
		case "/app/installations/43/access_tokens":
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"token":"status-token"}`)
		case "/repos/kevinmartin/sofa/statuses/" + testCandidateSHA:
			var request struct{ State, Context string }
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.State != "failure" || request.Context != "sofa / quality-policy" {
				t.Errorf("weakened quality did not fail: %+v", request)
			}
			failed = true
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected dispatch or API request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	b := bridge{client: testClient(handler), apiURL: "https://api.test", readToken: "read-token", appID: "123", keyPEM: key, now: time.Now}
	if err := b.run(context.Background(), testEvent(2)); err == nil || !failed {
		t.Fatalf("weakened gate was not blocked, err=%v failed=%v", err, failed)
	}
}

func TestBridgeRejectsForeignOrMalformedPRIdentityBeforeDispatch(t *testing.T) {
	for _, response := range []string{
		`{"number":2,"state":"open","head":{"sha":"bad"},"base":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"full_name":"kevinmartin/sofa"}}}`,
		`{"number":2,"state":"open","head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"base":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","repo":{"full_name":"elsewhere/sofa"}}}`,
	} {
		t.Run(response[:20], func(t *testing.T) {
			calls := 0
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				fmt.Fprint(w, response)
			})
			b := bridge{client: testClient(handler), apiURL: "https://api.test", readToken: "read-token"}
			if err := b.run(context.Background(), testEvent(2)); err == nil {
				t.Fatal("accepted invalid PR identity")
			}
			if calls != 1 {
				t.Fatalf("invalid PR made %d API calls; dispatch should not occur", calls)
			}
		})
	}
}
