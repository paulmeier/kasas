package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeConnector implements api.Connector without touching the network or secret store.
type fakeConnector struct {
	mu        sync.Mutex
	connected bool
	lastToken string
	setErr    error
}

func (f *fakeConnector) SetCredential(_ context.Context, input string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.lastToken = input
	f.connected = true
	return nil
}

func (f *fakeConnector) CredentialConfigured(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected, nil
}

func (f *fakeConnector) LastToken() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastToken
}

func newConfigServer(t *testing.T, cfg *config.Config, conn api.Connector) *httptest.Server {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	s := api.New(api.Options{
		Store:     store,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:   "test",
		Config:    cfg,
		Connector: conn,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv
}

// secretLadenConfig is a config whose every secret carries a distinctive marker so
// tests can assert none of them leak through the redacted endpoint.
func secretLadenConfig() *config.Config {
	return &config.Config{
		Server:   config.Server{Addr: ":8080"},
		Log:      config.Log{Level: "info", Format: "json"},
		Database: config.Database{Driver: "postgres", DSN: "postgres://u:supersecretpw@db:5432/kasas?sslmode=disable"},
		SimpleFIN: config.SimpleFIN{
			SetupToken: "leak-setup-token",
			AccessURL:  "https://bridgeuser:bridgepw@bridge.example/simplefin",
		},
		Sync:      config.Sync{Enabled: true, Interval: 6 * time.Hour, LookbackDays: 90, RunOnStart: true},
		Vault:     config.Vault{Enabled: true, Address: "http://vault:8200", Token: "leak-vault-token", Mount: "secret", Path: "kasas", AccessURLKey: "k"},
		Secrets:   config.Secrets{File: "/data/secrets.json"},
		MCP:       config.MCP{Enabled: true},
		Dashboard: config.Dashboard{Enabled: true, Token: "leak-dashboard-token"},
		Update:    config.Update{Check: true, AllowApply: true, Repository: "paulmeier/kasas"},
	}
}

func TestGetConfigRedactsSecrets(t *testing.T) {
	srv := newConfigServer(t, secretLadenConfig(), &fakeConnector{connected: true})

	resp, err := http.Get(srv.URL + "/api/v1/config")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	body := string(raw)

	// No secret value may appear anywhere in the response.
	for _, secret := range []string{"supersecretpw", "leak-setup-token", "leak-vault-token", "bridgepw", "leak-dashboard-token"} {
		assert.NotContains(t, body, secret, "secret leaked in /config response")
	}

	var dto api.ConfigDTO
	require.NoError(t, json.Unmarshal(raw, &dto))
	assert.True(t, dto.SimpleFIN.Connected, "connected reflects the connector")
	assert.True(t, dto.Vault.TokenSet, "vault token reduced to a boolean")
	assert.Equal(t, "postgres", dto.Database.Driver)
	assert.Equal(t, "postgres://u:xxxxx@db:5432/kasas?sslmode=disable", dto.Database.DSN, "dsn password masked")
	assert.Equal(t, "6h0m0s", dto.Sync.Interval)
	assert.Equal(t, "paulmeier/kasas", dto.Update.Repository)
}

func TestGetConfigConnectedFalseWhenNoCredential(t *testing.T) {
	srv := newConfigServer(t, secretLadenConfig(), &fakeConnector{connected: false})

	var dto api.ConfigDTO
	status := getJSON(t, srv, "/api/v1/config", &dto)
	require.Equal(t, http.StatusOK, status)
	assert.False(t, dto.SimpleFIN.Connected)
}

func TestSetSimpleFINCredential(t *testing.T) {
	conn := &fakeConnector{}
	srv := newConfigServer(t, secretLadenConfig(), conn)

	var out struct {
		Connected bool `json:"connected"`
	}
	status := putJSON(t, srv, "/api/v1/simplefin/credential", map[string]string{"token": "https://user:pass@bridge.example/simplefin"}, &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Connected)
	assert.Equal(t, "https://user:pass@bridge.example/simplefin", conn.LastToken())
}

func TestSetSimpleFINCredentialRejectsEmpty(t *testing.T) {
	srv := newConfigServer(t, secretLadenConfig(), &fakeConnector{})
	status := putJSON(t, srv, "/api/v1/simplefin/credential", map[string]string{"token": "  "}, nil)
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestSetSimpleFINCredentialUnavailableWithoutConnector(t *testing.T) {
	// With no Connector, the credential route is not registered at all.
	srv := newConfigServer(t, secretLadenConfig(), nil)
	status := putJSON(t, srv, "/api/v1/simplefin/credential", map[string]string{"token": "x"}, nil)
	assert.Equal(t, http.StatusNotFound, status)
}
