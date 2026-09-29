// The gate bridge only runs from sofa's trusted default-branch workflow_run.
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kevinmartin/sofa/internal/workflow"
	"github.com/spf13/cobra"
)

const (
	sofaRepository       = "kevinmartin/sofa"
	disposableRepository = "kevinmartin/sofa-disposable"
	disposableWorkflow   = "sofa-gate.yml"
	disposableRef        = "main"
	fastWorkflowName     = "Sofa / PR deterministic checks"
)

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
		Ref  string `json:"ref"`
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

func newGateBridgeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "gate-bridge",
		Short: "Dispatch an exact PR pair from a trusted workflow event",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runGateBridge(cmd.Context())
		},
	}
}

func runGateBridge(ctx context.Context) error {
	data, err := os.ReadFile(os.Getenv("GITHUB_EVENT_PATH"))
	if err != nil {
		return fmt.Errorf("read workflow event: %w", err)
	}
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
	return b.run(ctx, data)
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
		if pr.Number != number || pr.Base.Repo.FullName != sofaRepository || pr.Base.Ref != "main" ||
			!shaPattern.MatchString(pr.Head.SHA) || !shaPattern.MatchString(pr.Base.SHA) {
			return errors.New("invalid current PR identity")
		}
		if err := b.checkQualityContract(ctx, pr.Head.SHA, pr.Base.SHA); err != nil {
			if statusErr := b.qualityStatus(ctx, pr.Head.SHA, "failure", "Required quality contract was weakened"); statusErr != nil {
				return statusErr
			}
			return err
		}
		if err := b.qualityStatus(ctx, pr.Head.SHA, "success", "Required quality contract is intact"); err != nil {
			return err
		}
		if err := b.dispatch(ctx, pr, e.WorkflowRun.ID, e.WorkflowRun.RunAttempt); err != nil {
			return err
		}
	}
	return nil
}

func (b bridge) qualityStatus(ctx context.Context, sha, state, description string) error {
	token, err := b.sofaStatusToken(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/repos/%s/statuses/%s", b.apiURL, sofaRepository, sha)
	body := map[string]string{"state": state, "context": "sofa / quality-policy", "description": description}
	if err := b.request(ctx, http.MethodPost, url, token, body, nil, http.StatusCreated); err != nil {
		return fmt.Errorf("publish trusted quality status: %w", err)
	}
	return nil
}

func (b bridge) sofaStatusToken(ctx context.Context) (string, error) {
	jwt, err := b.appJWT()
	if err != nil {
		return "", err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	url := fmt.Sprintf("%s/repos/%s/installation", b.apiURL, sofaRepository)
	if err := b.request(ctx, http.MethodGet, url, jwt, nil, &installation, http.StatusOK); err != nil || installation.ID <= 0 {
		return "", errors.New("sofa App installation unavailable")
	}
	var credential struct {
		Token string `json:"token"`
	}
	body := struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{[]string{"sofa"}, map[string]string{"statuses": "write", "metadata": "read"}}
	url = fmt.Sprintf("%s/app/installations/%d/access_tokens", b.apiURL, installation.ID)
	if err := b.request(ctx, http.MethodPost, url, jwt, body, &credential, http.StatusCreated); err != nil || credential.Token == "" {
		return "", errors.New("unable to mint sofa status token")
	}
	return credential.Token, nil
}

func (b bridge) checkQualityContract(ctx context.Context, sha, baseSHA string) error {
	caller, err := b.workflowAt(ctx, sha, "pr-fast.yml")
	if err != nil {
		return err
	}
	reusable, err := b.workflowAt(ctx, sha, "quality.reusable.yml")
	if err != nil {
		return err
	}
	if err := workflow.CheckSofaQualityContract(caller, reusable); err == nil {
		return nil
	} else if !errors.Is(err, workflow.ErrUnapprovedQualityDigest) {
		return fmt.Errorf("PR weakens required quality contract: %w", err)
	}
	baseReusable, err := b.workflowAt(ctx, baseSHA, "quality.reusable.yml")
	if err != nil {
		return err
	}
	if err := workflow.CheckSofaQualityContractWithActionPins(caller, reusable, baseReusable, func(pin workflow.ActionPin) error {
		return b.verifyOfficialAction(ctx, pin)
	}); err != nil {
		return fmt.Errorf("PR weakens required quality contract: %w", err)
	}
	return nil
}

func (b bridge) verifyOfficialAction(ctx context.Context, pin workflow.ActionPin) error {
	var release struct {
		Draft      bool `json:"draft"`
		Prerelease bool `json:"prerelease"`
	}
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", b.apiURL, pin.Name, pin.Tag)
	if err := b.request(ctx, http.MethodGet, url, b.readToken, nil, &release, http.StatusOK); err != nil {
		return err
	}
	if release.Draft || release.Prerelease {
		return errors.New("action release is not stable")
	}
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	url = fmt.Sprintf("%s/repos/%s/git/ref/tags/%s", b.apiURL, pin.Name, pin.Tag)
	if err := b.request(ctx, http.MethodGet, url, b.readToken, nil, &ref, http.StatusOK); err != nil {
		return err
	}
	if ref.Object.Type == "tag" {
		if !shaPattern.MatchString(ref.Object.SHA) {
			return errors.New("action release tag object SHA is invalid")
		}
		var tag struct {
			Object struct {
				Type string `json:"type"`
				SHA  string `json:"sha"`
			} `json:"object"`
		}
		url = fmt.Sprintf("%s/repos/%s/git/tags/%s", b.apiURL, pin.Name, ref.Object.SHA)
		if err := b.request(ctx, http.MethodGet, url, b.readToken, nil, &tag, http.StatusOK); err != nil {
			return err
		}
		ref.Object = tag.Object
	}
	if ref.Object.Type != "commit" || ref.Object.SHA != pin.SHA {
		return errors.New("action release tag does not resolve to pinned commit")
	}
	return nil
}

func (b bridge) workflowAt(ctx context.Context, sha, name string) ([]byte, error) {
	var file struct {
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
		Size     int    `json:"size"`
	}
	url := fmt.Sprintf("%s/repos/%s/contents/.github/workflows/%s?ref=%s", b.apiURL, sofaRepository, name, sha)
	if err := b.request(ctx, http.MethodGet, url, b.readToken, nil, &file, http.StatusOK); err != nil {
		return nil, fmt.Errorf("read candidate quality workflow: %w", err)
	}
	if file.Encoding != "base64" || file.Size <= 0 || file.Size > 128<<10 {
		return nil, errors.New("candidate quality workflow unavailable")
	}
	data, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil || len(data) != file.Size {
		return nil, errors.New("candidate quality workflow content invalid")
	}
	return data, nil
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
		return "", errors.New("app credential unavailable")
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
