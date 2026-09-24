// Package github contains bounded GitHub API transport. Credentials never appear
// in errors and cannot be forwarded to a redirected endpoint.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const maxResponse = 8 << 20

type Client struct {
	http  *http.Client
	token string
}

func New(token string, transport http.RoundTripper) (*Client, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("GitHub credential reference is unset or invalid")
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{token: token, http: &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("GitHub redirect refused") }}}, nil
}

type APIError struct {
	Status      int
	RateLimited bool
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitHub API request failed (HTTP %d)", e.Status)
}

func (c *Client) Request(ctx context.Context, method, p string, input, output any) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\r\n#") {
		return errors.New("invalid GitHub API path")
	}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return errors.New("encode GitHub request")
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+p, body)
	if err != nil {
		return errors.New("construct GitHub request")
	}
	r.Header.Set("Authorization", "Bearer "+c.token)
	r.Header.Set("Accept", "application/vnd.github+json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	r.Header.Set("User-Agent", "sofa-milestone-01")
	resp, err := c.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("GitHub transport unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, RateLimited: resp.StatusCode == 429 || resp.Header.Get("X-RateLimit-Remaining") == "0"}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return errors.New("read GitHub response")
	}
	if len(b) > maxResponse {
		return errors.New("GitHub response exceeds limit")
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(b, output); err != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}

func (c *Client) GraphQL(ctx context.Context, query string, variables map[string]any, output any) error {
	var envelope struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := c.Request(ctx, http.MethodPost, "/graphql", map[string]any{"query": query, "variables": variables}, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("GitHub GraphQL query incomplete or unavailable")
	}
	if err := json.Unmarshal(envelope.Data, output); err != nil {
		return errors.New("invalid GitHub GraphQL data")
	}
	return nil
}
