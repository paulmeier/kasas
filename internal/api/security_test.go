package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/auth"
	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
)

// decodeJSON decodes a response body into dst.
func decodeJSON(resp *http.Response, dst any) error {
	return json.NewDecoder(resp.Body).Decode(dst)
}

// newSecuredServer builds an API server whose Authenticator is a real auth.Guard
// over a temp file-backed secret store. configToken, when non-empty, is the
// authoritative config/env token.
func newSecuredServer(t *testing.T, configToken string) (*httptest.Server, *auth.Guard) {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	guard, err := auth.New(configToken, secrets)
	require.NoError(t, err)

	s := api.New(api.Options{
		Store:      store,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		MCPEnabled: true,
		Config:     &config.Config{},
		Auth:       guard,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, guard
}

// doReq issues a request with an optional Bearer token and returns the response
// (closed via t.Cleanup).
func doReq(t *testing.T, srv *httptest.Server, method, path, token string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, body)
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeAuthStatus(t *testing.T, resp *http.Response) (required, authed bool) {
	t.Helper()
	var out struct {
		AuthRequired  bool `json:"auth_required"`
		Authenticated bool `json:"authenticated"`
	}
	require.NoError(t, decodeJSON(resp, &out))
	return out.AuthRequired, out.Authenticated
}

// TestOpenWhenNoToken: with no token configured anywhere, the API stays open and
// the auth-status endpoint reports unsecured.
func TestOpenWhenNoToken(t *testing.T) {
	srv, _ := newSecuredServer(t, "")

	resp := doReq(t, srv, http.MethodGet, "/api/v1/accounts", "", nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	required, authed := decodeAuthStatus(t, doReq(t, srv, http.MethodGet, "/api/v1/auth", "", nil))
	assert.False(t, required)
	assert.True(t, authed)
}

// TestGenerateThenRequiresToken walks the bootstrap path: generate a token while
// unsecured, after which the API rejects unauthenticated requests but accepts the
// new token.
func TestGenerateThenRequiresToken(t *testing.T) {
	srv, _ := newSecuredServer(t, "")

	// Generate is reachable while unsecured (no token needed yet).
	resp := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var gen struct {
		Token        string `json:"token"`
		AuthRequired bool   `json:"auth_required"`
		TokenSource  string `json:"token_source"`
	}
	require.NoError(t, decodeJSON(resp, &gen))
	require.NotEmpty(t, gen.Token)
	assert.True(t, gen.AuthRequired)
	assert.Equal(t, "stored", gen.TokenSource)

	// Now the API requires the token.
	assert.Equal(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/api/v1/accounts", "", nil).StatusCode)
	assert.Equal(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/api/v1/accounts", "wrong-token", nil).StatusCode)
	assert.Equal(t, http.StatusOK, doReq(t, srv, http.MethodGet, "/api/v1/accounts", gen.Token, nil).StatusCode)
}

// TestMCPGatedByToken: the MCP-over-HTTP endpoint is rejected without the token
// and passes auth with it.
func TestMCPGatedByToken(t *testing.T) {
	srv, _ := newSecuredServer(t, "")
	gen := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", nil)
	var out struct {
		Token string `json:"token"`
	}
	require.NoError(t, decodeJSON(gen, &out))

	assert.Equal(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/mcp", "", nil).StatusCode)
	// With the token the request gets past auth (the MCP handler then handles it;
	// whatever it returns, it must not be a 401).
	assert.NotEqual(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/mcp", out.Token, nil).StatusCode)
}

// TestAuthStatusReflectsToken: /api/v1/auth reports whether the presented token is
// valid once auth is on.
func TestAuthStatusReflectsToken(t *testing.T) {
	srv, _ := newSecuredServer(t, "")
	gen := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", nil)
	var out struct {
		Token string `json:"token"`
	}
	require.NoError(t, decodeJSON(gen, &out))

	req, auth1 := decodeAuthStatus(t, doReq(t, srv, http.MethodGet, "/api/v1/auth", "", nil))
	assert.True(t, req)
	assert.False(t, auth1)

	_, auth2 := decodeAuthStatus(t, doReq(t, srv, http.MethodGet, "/api/v1/auth", out.Token, nil))
	assert.True(t, auth2)

	_, auth3 := decodeAuthStatus(t, doReq(t, srv, http.MethodGet, "/api/v1/auth", "nope", nil))
	assert.False(t, auth3)
}

// TestRevokeDisablesAuth: clearing the token re-opens the API.
func TestRevokeDisablesAuth(t *testing.T) {
	srv, _ := newSecuredServer(t, "")
	gen := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", nil)
	var out struct {
		Token string `json:"token"`
	}
	require.NoError(t, decodeJSON(gen, &out))

	resp := doReq(t, srv, http.MethodDelete, "/api/v1/security/token", out.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Re-opened: no token needed.
	assert.Equal(t, http.StatusOK, doReq(t, srv, http.MethodGet, "/api/v1/accounts", "", nil).StatusCode)
}

// TestConfigManagedRefusesManagement: when the token comes from config/env, the
// management endpoints refuse with 409 (but the token still authenticates).
func TestConfigManagedRefusesManagement(t *testing.T) {
	const cfgToken = "config-managed-token-value"
	srv, _ := newSecuredServer(t, cfgToken)

	assert.Equal(t, http.StatusUnauthorized, doReq(t, srv, http.MethodGet, "/api/v1/accounts", "", nil).StatusCode)
	assert.Equal(t, http.StatusOK, doReq(t, srv, http.MethodGet, "/api/v1/accounts", cfgToken, nil).StatusCode)

	// Generate/revoke are refused with 409.
	assert.Equal(t, http.StatusConflict, doReq(t, srv, http.MethodPost, "/api/v1/security/token", cfgToken, nil).StatusCode)
	assert.Equal(t, http.StatusConflict, doReq(t, srv, http.MethodDelete, "/api/v1/security/token", cfgToken, nil).StatusCode)
}

// TestSetCustomToken: a caller-supplied token of sufficient length is accepted.
func TestSetCustomToken(t *testing.T) {
	srv, _ := newSecuredServer(t, "")
	body := strings.NewReader(`{"token":"my-own-custom-token-1234"}`)
	resp := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", body)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Equal(t, http.StatusOK, doReq(t, srv, http.MethodGet, "/api/v1/accounts", "my-own-custom-token-1234", nil).StatusCode)
}

// TestConfigEndpointSecuritySection: the redacted config exposes the token source
// (never the token).
func TestConfigEndpointSecuritySection(t *testing.T) {
	srv, _ := newSecuredServer(t, "")

	// Generate once (this also secures the API), then read /config with the token.
	gen := doReq(t, srv, http.MethodPost, "/api/v1/security/token", "", nil)
	var g struct {
		Token string `json:"token"`
	}
	require.NoError(t, decodeJSON(gen, &g))

	var out struct {
		Security struct {
			AuthRequired bool   `json:"auth_required"`
			TokenSource  string `json:"token_source"`
		} `json:"security"`
	}
	resp := doReq(t, srv, http.MethodGet, "/api/v1/config", g.Token, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, decodeJSON(resp, &out))
	assert.True(t, out.Security.AuthRequired)
	assert.Equal(t, "stored", out.Security.TokenSource)
}
