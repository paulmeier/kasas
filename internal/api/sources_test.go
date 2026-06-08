package api_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSources(t *testing.T) {
	srv := newConfigServer(t, secretLadenConfig(), &fakeSources{connected: true})

	var out struct {
		Enabled bool `json:"enabled"`
		Sources []struct {
			Type      string `json:"type"`
			Connected bool   `json:"connected"`
		} `json:"sources"`
	}
	status := getJSON(t, srv, "/api/v1/sources", &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Enabled)
	require.Len(t, out.Sources, 1)
	assert.Equal(t, "simplefin", out.Sources[0].Type)
	assert.True(t, out.Sources[0].Connected)
}

func TestListSourcesDisabled(t *testing.T) {
	// With no source manager wired, the route still responds (enabled=false) so the
	// dashboard can distinguish disabled from a routing error.
	srv := newConfigServer(t, secretLadenConfig(), nil)
	var out struct {
		Enabled bool `json:"enabled"`
	}
	status := getJSON(t, srv, "/api/v1/sources", &out)
	require.Equal(t, http.StatusOK, status)
	assert.False(t, out.Enabled)
}

func TestSyncSource(t *testing.T) {
	srv := newConfigServer(t, secretLadenConfig(), &fakeSources{connected: true})

	// Known source: accepted (runs asynchronously).
	status := postJSON(t, srv, "/api/v1/sources/simplefin/sync", nil, nil)
	assert.Equal(t, http.StatusAccepted, status)

	// Unknown source: 404.
	status = postJSON(t, srv, "/api/v1/sources/nope/sync", nil, nil)
	assert.Equal(t, http.StatusNotFound, status)
}

func TestSetSourceCredential(t *testing.T) {
	f := &fakeSources{}
	srv := newConfigServer(t, secretLadenConfig(), f)

	var out struct {
		Connected bool `json:"connected"`
	}
	status := putJSON(t, srv, "/api/v1/sources/simplefin/credential", map[string]string{"token": "tok-123"}, &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Connected)
	assert.Equal(t, "tok-123", f.LastToken())
}

// TestSourceOAuthFlow drives the OAuth start + callback: start returns the consent
// URL (carrying the issued state), and the callback verifies that state, exchanges
// the code, and redirects back to the Sources page.
func TestSourceOAuthFlow(t *testing.T) {
	f := &fakeSources{oauthURL: "https://consent.example/auth"}
	srv := newConfigServer(t, secretLadenConfig(), f)

	var startOut struct {
		URL string `json:"url"`
	}
	status := getJSON(t, srv, "/api/v1/sources/csv/oauth/start", &startOut)
	require.Equal(t, http.StatusOK, status)

	u, err := url.Parse(startOut.URL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEmpty(t, state, "the consent URL must carry the issued state")

	// The provider redirects the browser to the callback; it should not follow on.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/api/v1/sources/csv/oauth/callback?state=" + url.QueryEscape(state) + "&code=auth-code")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "connected=csv")
	assert.Equal(t, "auth-code", f.exchangedCode())
}

func TestSourceOAuthCallbackRejectsBadState(t *testing.T) {
	f := &fakeSources{oauthURL: "https://consent.example/auth"}
	srv := newConfigServer(t, secretLadenConfig(), f)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/api/v1/sources/csv/oauth/callback?state=forged&code=auth-code")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Location"), "error=")
	assert.Empty(t, f.exchangedCode(), "a forged state must not trigger an exchange")
}
