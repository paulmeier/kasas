package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/settings"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
)

// settingDTO mirrors settings.Status for decoding responses.
type settingDTO struct {
	Key             string `json:"key"`
	Kind            string `json:"kind"`
	Secret          bool   `json:"secret"`
	Source          string `json:"source"`
	Section         string `json:"section"`
	Value           string `json:"value"`
	Set             bool   `json:"set"`
	Overridden      bool   `json:"overridden"`
	RestartRequired bool   `json:"restart_required"`
}

type settingsResponse struct {
	Enabled         bool         `json:"enabled"`
	RestartRequired bool         `json:"restart_required"`
	Settings        []settingDTO `json:"settings"`
}

type setSettingResponse struct {
	Setting         settingDTO `json:"setting"`
	RestartRequired bool       `json:"restart_required"`
}

// newSettingsServer builds a server with a real settings service over a fresh
// store and file-backed secrets, plus a restart hook that records invocation.
func newSettingsServer(t *testing.T, sources api.SourceManager) (*httptest.Server, *int) {
	t.Helper()
	store := testutil.NewStore(t)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	base, err := config.Load("")
	require.NoError(t, err)
	boot := settings.Clone(base)
	restarts := 0
	s := api.New(api.Options{
		Store:    store,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:  "test",
		Config:   boot,
		Sources:  sources,
		Settings: settings.NewService(store, secrets, base, boot),
		Restart:  func() { restarts++ },
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, &restarts
}

func settingByKey(t *testing.T, list []settingDTO, key string) settingDTO {
	t.Helper()
	for _, s := range list {
		if s.Key == key {
			return s
		}
	}
	t.Fatalf("setting %q not in response", key)
	return settingDTO{}
}

func TestListSettings(t *testing.T) {
	srv, _ := newSettingsServer(t, nil)

	var out settingsResponse
	status := getJSON(t, srv, "/api/v1/settings", &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Enabled)
	assert.False(t, out.RestartRequired)

	plugins := settingByKey(t, out.Settings, "plugins.enabled")
	assert.Equal(t, "false", plugins.Value)
	assert.Equal(t, "bool", plugins.Kind)
	assert.False(t, plugins.Overridden)

	secret := settingByKey(t, out.Settings, "plaid.secret")
	assert.True(t, secret.Secret)
	assert.Empty(t, secret.Value, "secrets are never echoed")
	assert.Equal(t, "plaid", secret.Source)
}

func TestListSettingsDisabled(t *testing.T) {
	// With no settings service wired, the route still responds (enabled=false) so
	// the dashboard can distinguish disabled from a routing error.
	srv := newConfigServer(t, secretLadenConfig(), nil)
	var out settingsResponse
	status := getJSON(t, srv, "/api/v1/settings", &out)
	require.Equal(t, http.StatusOK, status)
	assert.False(t, out.Enabled)
}

func TestSetAndResetSetting(t *testing.T) {
	srv, _ := newSettingsServer(t, nil)

	var out setSettingResponse
	status := putJSON(t, srv, "/api/v1/settings/plugins.enabled", map[string]any{"value": "true"}, &out)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "true", out.Setting.Value)
	assert.True(t, out.Setting.Overridden)
	assert.True(t, out.Setting.RestartRequired)
	assert.True(t, out.RestartRequired)

	// A bare JSON bool is accepted too.
	status = putJSON(t, srv, "/api/v1/settings/sync.run_on_start", map[string]any{"value": false}, &out)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "false", out.Setting.Value)

	// The override is visible in the listing and survives as stored state.
	var list settingsResponse
	getJSON(t, srv, "/api/v1/settings", &list)
	assert.True(t, settingByKey(t, list.Settings, "plugins.enabled").Overridden)
	assert.True(t, list.RestartRequired)

	status = deleteJSON(t, srv, "/api/v1/settings/plugins.enabled", &out)
	require.Equal(t, http.StatusOK, status)
	assert.False(t, out.Setting.Overridden)
	assert.Equal(t, "false", out.Setting.Value)
}

func TestSetSettingRejectsBadInput(t *testing.T) {
	srv, _ := newSettingsServer(t, nil)

	var out setSettingResponse
	status := putJSON(t, srv, "/api/v1/settings/no.such.key", map[string]any{"value": "x"}, &out)
	assert.Equal(t, http.StatusNotFound, status)

	status = putJSON(t, srv, "/api/v1/settings/plugins.enabled", map[string]any{"value": "maybe"}, &out)
	assert.Equal(t, http.StatusBadRequest, status)

	// Parses but fails whole-config validation: rejected, nothing stored.
	status = putJSON(t, srv, "/api/v1/settings/webhooks.max_attempts", map[string]any{"value": "0"}, &out)
	assert.Equal(t, http.StatusBadRequest, status)
	var list settingsResponse
	getJSON(t, srv, "/api/v1/settings", &list)
	assert.False(t, settingByKey(t, list.Settings, "webhooks.max_attempts").Overridden)
}

func TestSetSecretSettingNeverEchoes(t *testing.T) {
	srv, _ := newSettingsServer(t, nil)

	var out setSettingResponse
	status := putJSON(t, srv, "/api/v1/settings/ethereum.api_key", map[string]any{"value": "etherscan-key"}, &out)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, out.Setting.Value)
	assert.True(t, out.Setting.Set)
	assert.True(t, out.Setting.Overridden)
}

func TestRestartEndpoint(t *testing.T) {
	srv, restarts := newSettingsServer(t, nil)

	var out struct {
		Restarting bool `json:"restarting"`
	}
	status := postJSON(t, srv, "/api/v1/system/restart", nil, &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Restarting)
	assert.Equal(t, 1, *restarts)

	// Without a restart hook the endpoint reports unavailable.
	plain := newConfigServer(t, secretLadenConfig(), nil)
	status = postJSON(t, plain, "/api/v1/system/restart", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, status)
}

// TestMCPSettingsTools drives the MCP mirror of the settings surface:
// list_settings, set_setting, reset_setting, and restart_kasas.
func TestMCPSettingsTools(t *testing.T) {
	store := testutil.NewStore(t)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	base, err := config.Load("")
	require.NoError(t, err)
	boot := settings.Clone(base)
	restarts := 0
	s := api.New(api.Options{
		Store:      store,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		MCPEnabled: true,
		Settings:   settings.NewService(store, secrets, base, boot),
		Restart:    func() { restarts++ },
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = s.MCPServer().Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	var list settingsResponse
	callTool(t, session, "list_settings", nil, &list)
	assert.Equal(t, "false", settingByKey(t, list.Settings, "plugins.enabled").Value)

	var out setSettingResponse
	callTool(t, session, "set_setting", map[string]any{"key": "plugins.enabled", "value": "true"}, &out)
	assert.Equal(t, "true", out.Setting.Value)
	assert.True(t, out.RestartRequired)

	res := callTool(t, session, "set_setting", map[string]any{"key": "plugins.enabled", "value": "maybe"}, nil)
	assert.True(t, res.IsError, "invalid values are rejected")

	callTool(t, session, "reset_setting", map[string]any{"key": "plugins.enabled"}, &out)
	assert.False(t, out.Setting.Overridden)

	var rst struct {
		Restarting bool `json:"restarting"`
	}
	callTool(t, session, "restart_kasas", nil, &rst)
	assert.True(t, rst.Restarting)
	assert.Equal(t, 1, restarts)
}

func TestListSourcesIncludesInactiveAndConfig(t *testing.T) {
	srv, _ := newSettingsServer(t, &fakeSources{connected: true})

	var out struct {
		Enabled bool `json:"enabled"`
		Sources []struct {
			Type   string       `json:"type"`
			Active bool         `json:"active"`
			Config []settingDTO `json:"config"`
		} `json:"sources"`
	}
	status := getJSON(t, srv, "/api/v1/sources", &out)
	require.Equal(t, http.StatusOK, status)
	assert.True(t, out.Enabled)

	byType := map[string]int{}
	for i, s := range out.Sources {
		byType[s.Type] = i
	}

	// The engine's active source comes first; registered-but-unbuilt sources are
	// listed as inactive so they can be configured (and then activated by restart).
	require.Contains(t, byType, "simplefin")
	assert.True(t, out.Sources[byType["simplefin"]].Active)
	require.Contains(t, byType, "plaid")
	plaid := out.Sources[byType["plaid"]]
	assert.False(t, plaid.Active)

	// Per-source config rides along, with secrets redacted.
	var keys []string
	secretEmpty := true
	for _, c := range plaid.Config {
		keys = append(keys, c.Key)
		if c.Secret && c.Value != "" {
			secretEmpty = false
		}
	}
	assert.Contains(t, keys, "plaid.client_id")
	assert.Contains(t, keys, "plaid.secret")
	assert.Contains(t, keys, "plaid.environment")
	assert.True(t, secretEmpty)
}
