package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type releaseTransport func(*http.Request) (*http.Response, error)

func (f releaseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func releaseResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestReleaseChannelGuardRejectsCompetingRefWithoutMutation(t *testing.T) {
	old := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	competing := strings.Repeat("c", 40)
	writes := 0
	api, _ := New("secret", releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			writes++
			t.Errorf("unexpected mutation %s", r.Method)
		}
		return releaseResponse(200, `{"object":{"sha":"`+competing+`","type":"commit"}}`), nil
	}))
	if err := api.UpdateReleaseChannel(context.Background(), "kevinmartin/sofa", "v0", old, newSHA); err == nil {
		t.Fatal("competing ref accepted")
	}
	if writes != 0 {
		t.Fatal("competing ref was overwritten")
	}
}

func TestReleaseChannelDirectUpdateAndIdempotentRetry(t *testing.T) {
	old := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	current := old
	writes := 0
	api, _ := New("secret", releaseTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("wrong credential boundary")
		}
		if r.Method == "GET" {
			return releaseResponse(200, `{"object":{"sha":"`+current+`","type":"commit"}}`), nil
		}
		if r.Method != "PATCH" || r.URL.Path != "/repos/kevinmartin/sofa/git/refs/tags/v0" {
			t.Fatalf("update=%s %s", r.Method, r.URL.Path)
		}
		var body struct {
			SHA   string `json:"sha"`
			Force bool   `json:"force"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.SHA != newSHA || !body.Force {
			t.Fatal("wrong ref update")
		}
		writes++
		current = newSHA
		return releaseResponse(200, `{}`), nil
	}))
	if err := api.UpdateReleaseChannel(context.Background(), "kevinmartin/sofa", "v0", old, newSHA); err != nil {
		t.Fatal(err)
	}
	if err := api.UpdateReleaseChannel(context.Background(), "kevinmartin/sofa", "v0", old, newSHA); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("retry performed %d writes", writes)
	}
	if err := api.UpdateReleaseChannel(context.Background(), "kevinmartin/sofa", "v0.1.0", old, newSHA); err == nil {
		t.Fatal("exact version tag can be rewritten")
	}
}

func TestReleaseUploadUsesFixedHostAndRefusesRedirects(t *testing.T) {
	calls := 0
	api, _ := New("secret", releaseTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "uploads.github.com" || r.URL.Path != "/repos/kevinmartin/sofa/releases/7/assets" || r.URL.Query().Get("name") != "release.json" {
			t.Fatalf("wrong upload identity %s", r.URL)
		}
		response := releaseResponse(302, "")
		response.Header.Set("Location", "https://attacker.invalid/steal")
		return response, nil
	}))
	if _, err := api.UploadReleaseAsset(context.Background(), "kevinmartin/sofa", 7, "release.json", []byte("metadata")); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("redirect result=%v", err)
	}
	if calls != 1 {
		t.Fatalf("redirect forwarded credentials in %d calls", calls)
	}
}

func TestReleaseCanaryDispatchIsFixedRepositoryAndBoundedIdentity(t *testing.T) {
	source := strings.Repeat("a", 40)
	correlation := strings.Repeat("b", 40)
	calls := 0
	api, _ := New("secret", releaseTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/repos/kevinmartin/sofa-disposable/actions/workflows/sofa-release-canary.yml/dispatches" {
			t.Fatalf("wrong dispatch %s", r.URL)
		}
		var body struct {
			Ref    string            `json:"ref"`
			Inputs map[string]string `json:"inputs"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Ref != "main" || body.Inputs["source_sha"] != source || body.Inputs["correlation"] != correlation || body.Inputs["release_version"] != "v0.1.0" {
			t.Fatal("dispatch lost identity")
		}
		return releaseResponse(204, ""), nil
	}))
	if err := api.DispatchReleaseCanary(context.Background(), "kevinmartin/sofa-disposable", "main", "v0.1.0", source, correlation, "100", "1"); err != nil {
		t.Fatal(err)
	}
	if err := api.DispatchReleaseCanary(context.Background(), "attacker/disposable", "main", "v0.1.0", source, correlation, "100", "1"); err == nil {
		t.Fatal("dispatch escaped enrolled disposable")
	}
	if calls != 1 {
		t.Fatal("invalid identity made a credentialed request")
	}
}

func TestReleaseCreationRejectsMajorChannelNamesBeforeCredentialedRequest(t *testing.T) {
	api, _ := New("secret", releaseTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid release creation made a credentialed request")
		return nil, nil
	}))
	for _, tag := range []string{"v0", "v1", "v00.1.0", "v0.01.0", "v0.1.00"} {
		if _, err := api.CreateDraftRelease(context.Background(), "kevinmartin/sofa", tag, strings.Repeat("a", 40), "body"); err == nil {
			t.Fatalf("nonexact release tag %q accepted", tag)
		}
	}
}

func TestReleaseLookupRecoversPendingDraftWhenPublishedTagEndpointReturns404(t *testing.T) {
	requests := 0
	api, _ := New("secret", releaseTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		switch r.URL.Path {
		case "/repos/kevinmartin/sofa/releases/tags/v0.1.0":
			return releaseResponse(404, `{}`), nil
		case "/graphql":
			var body struct {
				Variables map[string]string `json:"variables"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Variables["owner"] != "kevinmartin" || body.Variables["name"] != "sofa" || body.Variables["tag"] != "v0.1.0" {
				t.Fatal("draft lookup lost repository/tag identity")
			}
			return releaseResponse(200, `{"data":{"repository":{"release":{"databaseId":7,"isDraft":true}}}}`), nil
		case "/repos/kevinmartin/sofa/releases/7":
			return releaseResponse(200, `{"id":7,"tag_name":"v0.1.0","target_commitish":"`+strings.Repeat("a", 40)+`","draft":true,"assets":[{"id":8,"name":"sofa-linux-amd64.tar.gz","state":"uploaded"}]}`), nil
		default:
			t.Fatalf("unexpected release lookup endpoint %s", r.URL.Path)
			return nil, nil
		}
	}))
	record, found, err := api.ReleaseRecordByTag(context.Background(), "kevinmartin/sofa", "v0.1.0")
	if err != nil || !found || !record.Draft || record.ID != 7 || len(record.Assets) != 1 || requests != 3 {
		t.Fatalf("pending draft recovery=%+v found=%v err=%v requests=%d", record, found, err, requests)
	}
}
