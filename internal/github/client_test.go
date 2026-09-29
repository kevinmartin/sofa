package github

import (
	"net/http"
	"testing"
)

func TestAnonymousClientOmitsAuthorization(t *testing.T) {
	client := NewAnonymous(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.github.com/repos/kevinmartin/sofa" ||
			request.Header.Get("Authorization") != "" {
			t.Fatalf("anonymous GitHub request changed: %s, authorization=%q", request.URL, request.Header.Get("Authorization"))
		}
		return jsonResponse(http.StatusOK, map[string]string{"full_name": "kevinmartin/sofa"}), nil
	}))
	if client.Authenticated() {
		t.Fatal("anonymous client claims a credential")
	}
	var repository struct {
		FullName string `json:"full_name"`
	}
	if err := client.Request(t.Context(), http.MethodGet, "/repos/kevinmartin/sofa", nil, &repository); err != nil ||
		repository.FullName != "kevinmartin/sofa" {
		t.Fatalf("anonymous public read failed: %v, %+v", err, repository)
	}
}
