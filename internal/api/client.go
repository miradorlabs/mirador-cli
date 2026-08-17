// Package api is the typed HTTP client for the Mirador API gateway.
//
// It owns two things no command should repeat: attaching the right credential
// (server key or CLI token, plus the per-request project header), and refreshing an
// expired CLI token transparently so a long-lived shell never sees a spurious 401.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miradorlabs/mirador-cli/internal/auth"
	"github.com/miradorlabs/mirador-cli/internal/config"
)

const (
	userAgentPrefix = "mirador-cli/"

	projectHeader = "X-Mirador-Project"
)

// APIError is a structured failure from the gateway. Commands print Message rather
// than a bare status code, since the gateway's messages name the fix.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
}

func (e *APIError) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("%s (%s, request_id=%s)", e.Message, e.Code, e.RequestID)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s (%s)", e.Message, e.Code)
	}
	return fmt.Sprintf("request failed with status %d", e.StatusCode)
}

// Unauthenticated reports whether the credential itself was rejected, as opposed to
// the request being malformed or the caller lacking access to a specific resource.
func (e *APIError) Unauthenticated() bool { return e.StatusCode == http.StatusUnauthorized }

type Client struct {
	// baseURL is the data plane; authURL is the credential surface. They are separate
	// hosts, so every call has to say which one it means — see dataHost/authHost.
	baseURL string
	authURL string
	http    *http.Client
	version string

	// profile and credential are empty under a server key: MIRADOR_API_KEY has
	// nothing to refresh and nothing to persist.
	profile string
	apiKey  string

	mu         sync.Mutex
	credential *auth.Credential
	lastLogin  *LoginResult

	projectID string
}

type Options struct {
	Version   string
	ProjectID string
	Timeout   time.Duration
}

// New builds a client for the resolved config. A server key short-circuits the
// credential path entirely — that is the CI story, where no browser exists.
func New(cfg *config.Config, opts Options) (*Client, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	c := &Client{
		baseURL:   strings.TrimRight(cfg.APIURL, "/"),
		authURL:   strings.TrimRight(cfg.AuthURL, "/"),
		http:      &http.Client{Timeout: timeout},
		version:   opts.Version,
		profile:   cfg.ProfileName,
		apiKey:    cfg.APIKey,
		projectID: firstNonEmpty(opts.ProjectID, cfg.ProjectID),
	}
	if c.apiKey != "" {
		return c, nil
	}

	cred, err := auth.LoadCredential(cfg.ProfileName)
	if err != nil {
		return nil, err
	}
	// Refuse before the first request rather than letting the wrong environment's auth
	// host answer 401 — a message naming both hosts is the difference between "my login
	// broke" and "I am pointed at dev".
	if err := cred.CheckEnvironment(c.authURL); err != nil {
		return nil, err
	}
	c.credential = cred
	return c, nil
}

// NewAnonymous builds a client for the endpoints that mint credentials, which by
// definition cannot present one. Only the auth host is reachable from it.
func NewAnonymous(authURL, version string) *Client {
	return &Client{
		authURL: strings.TrimRight(authURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		version: version,
	}
}

// host selects which surface a call targets. Requests never guess from the path —
// the two hosts have overlapping path prefixes and a wrong guess would send a
// credential to the wrong service.
type host int

const (
	dataHost host = iota
	authHost
)

func (c *Client) baseFor(h host) string {
	if h == authHost {
		return c.authURL
	}
	return c.baseURL
}

func (c *Client) ProjectID() string { return c.projectID }

// Credential returns the in-memory credential, which may be newer than the one on
// disk if a refresh has happened this run.
func (c *Client) Credential() *auth.Credential {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.credential
}

// Get and Post address the data plane.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, dataHost, http.MethodGet, path, query, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, dataHost, http.MethodPost, path, nil, body, out)
}

// AuthGet and AuthPost address the credential surface.
func (c *Client) AuthGet(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, authHost, http.MethodGet, path, query, nil, out)
}

func (c *Client) AuthPost(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, authHost, http.MethodPost, path, nil, body, out)
}

func (c *Client) do(ctx context.Context, h host, method, path string, query url.Values, body, out any) error {
	// Refresh before the first attempt when the token is known-expired, so the common
	// case costs one round trip rather than a guaranteed 401 followed by a retry.
	if err := c.refreshIfNeeded(ctx); err != nil {
		return err
	}

	resp, err := c.attempt(ctx, h, method, path, query, body)
	if err != nil {
		return err
	}

	// A 401 on a token we believed was live means it was revoked or rotated
	// elsewhere. One refresh-and-retry covers that; a second 401 is a real logout.
	if resp.StatusCode == http.StatusUnauthorized && c.canRefresh() {
		resp.Body.Close()
		if refreshErr := c.refresh(ctx); refreshErr != nil {
			return refreshErr
		}
		resp, err = c.attempt(ctx, h, method, path, query, body)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	return decode(resp, out)
}

func (c *Client) attempt(ctx context.Context, h host, method, path string, query url.Values, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := c.baseFor(h) + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgentPrefix+c.version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.mu.Lock()
	switch {
	case c.apiKey != "":
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	case c.credential != nil:
		req.Header.Set("Authorization", "Bearer "+c.credential.AccessToken)
	}
	c.mu.Unlock()

	// Only the data plane resolves a project, and only under a CLI token: a server
	// key's grant already fixes it, and the auth surface is organization-scoped
	// throughout. Sending it anywhere else would imply a scope that does not exist.
	if h == dataHost && c.apiKey == "" && c.projectID != "" {
		req.Header.Set(projectHeader, c.projectID)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

func decode(resp *http.Response, out any) error {
	if resp.StatusCode >= 400 {
		return parseError(resp)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func parseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var envelope struct {
		Error struct {
			Code    string           `json:"code"`
			Message string           `json:"message"`
			Details []map[string]any `json:"details"`
		} `json:"error"`
	}
	apiErr := &APIError{StatusCode: resp.StatusCode}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		apiErr.Code = envelope.Error.Code
		apiErr.Message = envelope.Error.Message
		for _, detail := range envelope.Error.Details {
			if id, ok := detail["request_id"].(string); ok {
				apiErr.RequestID = id
			}
		}
		return apiErr
	}
	apiErr.Message = strings.TrimSpace(string(body))
	if apiErr.Message == "" {
		apiErr.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
