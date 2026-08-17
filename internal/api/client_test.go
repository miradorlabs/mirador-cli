package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miradorlabs/mirador-cli/internal/auth"
	"github.com/miradorlabs/mirador-cli/internal/config"
)

// newTestClient builds a client against a stub server with a CLI credential,
// pointing the credential store at a temp dir so a refresh cannot touch the
// developer's real ~/.mirador.
func newTestClient(t *testing.T, url string, cred *auth.Credential, projectID string) *Client {
	t.Helper()
	return newSplitTestClient(t, url, url, cred, projectID)
}

// newSplitTestClient points the two surfaces at (possibly) different servers, which is
// how they are deployed: api.mirador.org and auth.mirador.org.
func newSplitTestClient(t *testing.T, apiURL, authURL string, cred *auth.Credential, projectID string) *Client {
	t.Helper()
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	if err := auth.SaveCredential(config.DefaultProfile, cred); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	client, err := New(&config.Config{
		ProfileName: config.DefaultProfile,
		APIURL:      apiURL,
		AuthURL:     authURL,
		ProjectID:   projectID,
	}, Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func liveCredential() *auth.Credential {
	return &auth.Credential{
		AccessToken:  "mir_cli_live",
		RefreshToken: "mir_clr_live",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
}

func TestClient_SendsBearerAndProjectHeader(t *testing.T) {
	var gotAuth, gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.Header.Get(projectHeader)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL, liveCredential(), "project-123")
	if err := client.Get(context.Background(), "/v1/identity", nil, &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if gotAuth != "Bearer mir_cli_live" {
		t.Errorf("Authorization = %q, want the access token", gotAuth)
	}
	// Without this header every project-scoped read is a 400 — it is the whole
	// mechanism by which a project-less credential picks a project.
	if gotProject != "project-123" {
		t.Errorf("%s = %q, want project-123", projectHeader, gotProject)
	}
}

func TestClient_ServerKeyDoesNotSendProjectHeader(t *testing.T) {
	var gotAuth, gotProject string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotProject = r.Header.Get(projectHeader)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())
	client, err := New(&config.Config{
		ProfileName: config.DefaultProfile,
		APIURL:      srv.URL,
		AuthURL:     srv.URL,
		APIKey:      "mir_srv_abc",
		ProjectID:   "project-123",
	}, Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Get(context.Background(), "/v1/identity", nil, &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if gotAuth != "Bearer mir_srv_abc" {
		t.Errorf("Authorization = %q, want the server key", gotAuth)
	}
	// The key's grant already fixes the project; sending a header would imply a
	// scope the key does not have.
	if gotProject != "" {
		t.Errorf("%s = %q, want it omitted under a server key", projectHeader, gotProject)
	}
}

func TestClient_RefreshesExpiredTokenBeforeRequesting(t *testing.T) {
	var refreshes, reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			refreshes.Add(1)
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mir_cli_rotated",
				"refresh_token": "mir_clr_rotated",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"organization":  map[string]string{"id": "org-1", "name": "Acme"},
			})
			return
		}
		reads.Add(1)
		if r.Header.Get("Authorization") != "Bearer mir_cli_rotated" {
			t.Errorf("read used %q, want the rotated token", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	expired := &auth.Credential{
		AccessToken:  "mir_cli_stale",
		RefreshToken: "mir_clr_stale",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}
	client := newTestClient(t, srv.URL, expired, "project-123")

	if err := client.Get(context.Background(), "/v1/identity", nil, &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}

	// A known-expired token refreshes up front, so the read costs one request
	// rather than a guaranteed 401 followed by a retry.
	if got := refreshes.Load(); got != 1 {
		t.Errorf("refreshes = %d, want 1", got)
	}
	if got := reads.Load(); got != 1 {
		t.Errorf("reads = %d, want 1 (no wasted 401 round trip)", got)
	}

	// The rotated pair must be persisted: the server already invalidated the old
	// refresh token, so losing the new one would strand the session.
	saved, err := auth.LoadCredential(config.DefaultProfile)
	if err != nil {
		t.Fatalf("LoadCredential: %v", err)
	}
	if saved.RefreshToken != "mir_clr_rotated" {
		t.Errorf("stored refresh token = %q, want the rotated one", saved.RefreshToken)
	}
}

func TestClient_RetriesOnceAfterUnexpected401(t *testing.T) {
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mir_cli_rotated",
				"refresh_token": "mir_clr_rotated",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"organization":  map[string]string{"id": "org-1"},
			})
			return
		}
		// First read rejects a token the client believed was live — what a
		// revocation or an out-of-band rotation looks like.
		if reads.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"invalid or expired CLI token"}}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL, liveCredential(), "project-123")

	var out map[string]any
	if err := client.Get(context.Background(), "/v1/identity", nil, &out); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("expected the retried request to succeed, got %v", out)
	}
	if got := reads.Load(); got != 2 {
		t.Errorf("reads = %d, want exactly 2 (one retry, not a loop)", got)
	}
}

func TestClient_SurfacesGatewayErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":"INVALID_ARGUMENT","message":"missing X-Mirador-Project header","details":[{"request_id":"req-9"}]}}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL, liveCredential(), "")

	err := client.Get(context.Background(), "/v1/traces", nil, &struct{}{})
	if err == nil {
		t.Fatal("expected an error")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T", err)
	}
	// The gateway's message names the fix, so it has to reach the user intact.
	if apiErr.Message != "missing X-Mirador-Project header" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.RequestID != "req-9" {
		t.Errorf("RequestID = %q, want it carried through for support", apiErr.RequestID)
	}
}

func TestClient_DoesNotRefreshUnderAServerKey(t *testing.T) {
	var tokenCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == tokenPath {
			tokenCalls.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"invalid API key"}}`))
	}))
	defer srv.Close()

	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())
	client, err := New(&config.Config{
		ProfileName: config.DefaultProfile,
		APIURL:      srv.URL,
		AuthURL:     srv.URL,
		APIKey:      "mir_srv_abc",
	}, Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := client.Get(context.Background(), "/v1/identity", nil, &struct{}{}); err == nil {
		t.Fatal("expected the 401 to surface")
	}
	// A server key has nothing to refresh; attempting it would be a pointless
	// round trip and could mask a genuinely bad key.
	if got := tokenCalls.Load(); got != 0 {
		t.Errorf("token endpoint called %d times under a server key, want 0", got)
	}
}

// TestClient_RoutesCredentialCallsToTheAuthHost pins the two-host split: a refresh must
// go to the auth surface even though it was triggered by a data-plane read. Sending it
// to the data host would leak a refresh token to a service that cannot honour it and
// would fail every login on a real deployment.
func TestClient_RoutesCredentialCallsToTheAuthHost(t *testing.T) {
	var dataPaths, authPaths []string

	dataSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dataPaths = append(dataPaths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer mir_cli_rotated" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":"UNAUTHENTICATED","message":"stale"}}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer dataSrv.Close()

	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authPaths = append(authPaths, r.URL.Path)
		if r.URL.Path == tokenPath {
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "mir_cli_rotated",
				"refresh_token": "mir_clr_rotated",
				"token_type":    "Bearer",
				"expires_in":    3600,
				"organization":  map[string]string{"id": "org-1"},
			})
			return
		}
		w.Write([]byte(`{"projects":[]}`))
	}))
	defer authSrv.Close()

	expired := &auth.Credential{
		AccessToken:  "mir_cli_stale",
		RefreshToken: "mir_clr_stale",
		ExpiresAt:    time.Now().Add(-time.Hour),
	}
	client := newSplitTestClient(t, dataSrv.URL, authSrv.URL, expired, "project-123")

	if err := client.Get(context.Background(), "/v1/traces", nil, &struct{}{}); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := client.AuthGet(context.Background(), "/v1/projects", nil, &struct{}{}); err != nil {
		t.Fatalf("AuthGet: %v", err)
	}

	for _, p := range dataPaths {
		if p == tokenPath {
			t.Fatalf("a credential call reached the data host: %v", dataPaths)
		}
	}
	if len(authPaths) == 0 || authPaths[0] != tokenPath {
		t.Errorf("the refresh should have gone to the auth host first, got %v", authPaths)
	}
	if !slices.Contains(authPaths, "/v1/projects") {
		t.Errorf("project listing should go to the auth host, got %v", authPaths)
	}
	if !slices.Contains(dataPaths, "/v1/traces") {
		t.Errorf("trace reads should go to the data host, got %v", dataPaths)
	}
}

// TestClient_RefusesACredentialFromAnotherEnvironment covers the mistake a developer
// makes weekly: logging in against dev, then running a command with the prod endpoints.
// Without this the prod auth host answers 401 and the message reads like a broken login
// rather than a wrong endpoint — and a dev-issued token has been handed to prod on the way.
func TestClient_RefusesACredentialFromAnotherEnvironment(t *testing.T) {
	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())

	cred := liveCredential()
	cred.AuthURL = "https://auth-dev.mirador.org"
	if err := auth.SaveCredential(config.DefaultProfile, cred); err != nil {
		t.Fatalf("seed credential: %v", err)
	}

	_, err := New(&config.Config{
		ProfileName: config.DefaultProfile,
		APIURL:      "https://api.mirador.org",
		AuthURL:     "https://auth.mirador.org",
	}, Options{Version: "test"})
	if err == nil {
		t.Fatal("expected a credential from another environment to be refused")
	}

	var wrongEnv *auth.ErrWrongEnvironment
	if !errors.As(err, &wrongEnv) {
		t.Fatalf("expected ErrWrongEnvironment, got %T: %v", err, err)
	}
	// Both hosts must appear, or the message does not tell you what to fix.
	for _, want := range []string{"auth-dev.mirador.org", "auth.mirador.org"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q, got %v", want, err)
		}
	}
}

// TestClient_AcceptsACredentialFromTheSameEnvironment keeps the guard from being a wall:
// the ordinary case must be untouched, and credentials written before the field existed
// carry no host and must still work.
func TestClient_AcceptsACredentialFromTheSameEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		authURL string
	}{
		{"same host", "https://auth.mirador.org"},
		{"pre-existing credential with no recorded host", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())
			cred := liveCredential()
			cred.AuthURL = tc.authURL
			if err := auth.SaveCredential(config.DefaultProfile, cred); err != nil {
				t.Fatalf("seed credential: %v", err)
			}
			if _, err := New(&config.Config{
				ProfileName: config.DefaultProfile,
				APIURL:      "https://api.mirador.org",
				AuthURL:     "https://auth.mirador.org",
			}, Options{Version: "test"}); err != nil {
				t.Fatalf("New: %v", err)
			}
		})
	}
}

// TestClient_StampsTheIssuingHostOnLogin is what makes the guard above possible.
func TestClient_StampsTheIssuingHostOnLogin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token": "mir_cli_x", "refresh_token": "mir_clr_x",
			"token_type": "Bearer", "expires_in": 3600,
			"organization": map[string]string{"id": "org-1"},
		})
	}))
	defer srv.Close()

	t.Setenv("MIRADOR_CONFIG_DIR", t.TempDir())
	client := NewAnonymous(srv.URL, "test")

	cred, err := client.ExchangeCode(context.Background(), "mir_cod_x", "verifier", 54321)
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if cred.AuthURL != srv.URL {
		t.Errorf("AuthURL = %q, want the host that minted it (%q)", cred.AuthURL, srv.URL)
	}
}
