// sofa-gate-bridge only runs from sofa's trusted default-branch workflow_run.
// Candidate code and workflow artifacts are never loaded in this process.
package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"time"
)

const (
	sofaRepository       = "kevinmartin/sofa"
	disposableRepository = "kevinmartin/sofa-disposable"
	disposableWorkflow   = "sofa-gate.yml"
	disposableRef        = "main"
	fastWorkflowName     = "sofa PR fast"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type event struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	WorkflowRun struct {
		ID           int64  `json:"id"`
		RunAttempt   int64  `json:"run_attempt"`
		Event        string `json:"event"`
		Name         string `json:"name"`
		Status       string `json:"status"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
}

type pullRequest struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		SHA  string `json:"sha"`
		Repo struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"base"`
}

type bridge struct {
	client    *http.Client
	apiURL    string
	readToken string
	appID     string
	keyPEM    string
	now       func() time.Time
}

func main() {
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err == nil {
		b := bridge{
			client: &http.Client{
				Timeout: 15 * time.Second,
				CheckRedirect: func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				},
			},
			apiURL:    "https://api.github.com",
			readToken: os.Getenv("SOFA_GATE_READ_TOKEN"),
			appID:     os.Getenv("SOFA_GATE_APP_ID"),
			keyPEM:    os.Getenv("SOFA_GATE_APP_PRIVATE_KEY"),
			now:       time.Now,
		}
		err = b.run(context.Background(), data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gate bridge:", err)
		os.Exit(1)
	}
}

func (b bridge) run(ctx context.Context, raw []byte) error {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return errors.New("invalid workflow event")
	}
	if e.Action != "completed" || e.Repository.FullName != sofaRepository ||
		e.WorkflowRun.Event != "pull_request" || e.WorkflowRun.Name != fastWorkflowName ||
		e.WorkflowRun.Status != "completed" || e.WorkflowRun.ID <= 0 || e.WorkflowRun.RunAttempt <= 0 {
		return errors.New("unexpected workflow event identity")
	}
	if b.readToken == "" {
		return errors.New("read credential unavailable")
	}
	// GitHub can omit pull_requests for forked runs. The disposable default-
	// branch watcher remains the fallback for an empty association list.
	if len(e.WorkflowRun.PullRequests) == 0 {
		return nil
	}
	if len(e.WorkflowRun.PullRequests) > 20 {
		return errors.New("too many PR associations")
	}
	numbers := make([]int, 0, len(e.WorkflowRun.PullRequests))
	for _, pr := range e.WorkflowRun.PullRequests {
		if pr.Number <= 0 {
			return errors.New("invalid PR association")
		}
		if !slices.Contains(numbers, pr.Number) {
			numbers = append(numbers, pr.Number)
		}
	}
	// Read current GitHub PR state, not candidate-supplied workflow output.
	// An old run is allowed to wake the current revision; the coordinator
	// independently checks the same pair before and after its suite.
	for _, number := range numbers {
		pr, err := b.readPR(ctx, number)
		if err != nil {
			return err
		}
		if pr.State != "open" {
			continue
		}
		if pr.Number != number || pr.Base.Repo.FullName != sofaRepository ||
			!shaPattern.MatchString(pr.Head.SHA) || !shaPattern.MatchString(pr.Base.SHA) {
			return errors.New("invalid current PR identity")
		}
		if err := b.dispatch(ctx, pr, e.WorkflowRun.ID, e.WorkflowRun.RunAttempt); err != nil {
			return err
		}
	}
	return nil
}

func (b bridge) readPR(ctx context.Context, number int) (pullRequest, error) {
	var pr pullRequest
	url := fmt.Sprintf("%s/repos/%s/pulls/%d", b.apiURL, sofaRepository, number)
	if err := b.request(ctx, http.MethodGet, url, b.readToken, nil, &pr, http.StatusOK); err != nil {
		return pr, fmt.Errorf("read current PR: %w", err)
	}
	return pr, nil
}

func (b bridge) dispatch(ctx context.Context, pr pullRequest, runID, attempt int64) error {
	token, err := b.disposableAppToken(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/actions/workflows/%s/dispatches", b.apiURL, disposableRepository, disposableWorkflow)
	body := struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}{
		Ref: disposableRef,
		Inputs: map[string]string{
			"sofa_pr":            strconv.Itoa(pr.Number),
			"candidate_sha":      pr.Head.SHA,
			"base_sha":           pr.Base.SHA,
			"source_run_id":      strconv.FormatInt(runID, 10),
			"source_run_attempt": strconv.FormatInt(attempt, 10),
		},
	}
	if err := b.request(ctx, http.MethodPost, url, token, body, nil, http.StatusNoContent); err != nil {
		return fmt.Errorf("dispatch disposable suite: %w", err)
	}
	return nil
}

func (b bridge) disposableAppToken(ctx context.Context) (string, error) {
	jwt, err := b.appJWT()
	if err != nil {
		return "", err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	url := fmt.Sprintf("%s/repos/%s/installation", b.apiURL, disposableRepository)
	if err := b.request(ctx, http.MethodGet, url, jwt, nil, &installation, http.StatusOK); err != nil {
		return "", fmt.Errorf("find disposable App installation: %w", err)
	}
	if installation.ID <= 0 {
		return "", errors.New("invalid disposable App installation")
	}
	var credential struct {
		Token string `json:"token"`
	}
	body := struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{
		Repositories: []string{"sofa-disposable"},
		Permissions:  map[string]string{"actions": "write", "metadata": "read"},
	}
	url = fmt.Sprintf("%s/app/installations/%d/access_tokens", b.apiURL, installation.ID)
	if err := b.request(ctx, http.MethodPost, url, jwt, body, &credential, http.StatusCreated); err != nil {
		return "", fmt.Errorf("mint disposable dispatch token: %w", err)
	}
	if credential.Token == "" {
		return "", errors.New("empty disposable dispatch token")
	}
	return credential.Token, nil
}

func (b bridge) appJWT() (string, error) {
	if b.appID == "" || b.keyPEM == "" {
		return "", errors.New("App credential unavailable")
	}
	if _, err := strconv.ParseInt(b.appID, 10, 64); err != nil {
		return "", errors.New("invalid App ID")
	}
	block, _ := pem.Decode([]byte(b.keyPEM))
	if block == nil {
		return "", errors.New("invalid App key")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, _ = parsed.(*rsa.PrivateKey)
	}
	if key == nil {
		return "", errors.New("invalid App RSA key")
	}
	now := b.now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{"iat": now - 60, "exp": now + 9*60, "iss": b.appID})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	message := header + "." + payload
	hash := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", errors.New("sign App credential")
	}
	return message + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (b bridge) request(ctx context.Context, method, url, token string, body, out any, expected int) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errors.New("encode API request")
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return errors.New("construct API request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return errors.New("API transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		// Never print response bodies: a provider may echo inputs or credentials.
		return fmt.Errorf("API status %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return errors.New("invalid API response")
		}
	}
	return nil
}
