package api_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/testutil"
)

func newPluginTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)

	dir := t.TempDir()
	pdir := filepath.Join(dir, "demo")
	require.NoError(t, os.MkdirAll(pdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "plugin.toml"), []byte(
		`name="demo"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+`capabilities=["labels:write"]`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "main.lua"), []byte(`function OnTransactionCreate(txn) end`), 0o644))

	bus := events.NewBus()
	mgr := plugins.NewManager(plugins.Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes: map[string]plugins.Runtime{plugins.RuntimeLua: plugins.NewLuaRuntime()},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s := api.New(api.Options{
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		MCPEnabled:    true,
		PluginManager: mgr,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv
}

func TestPluginsListAndLifecycle(t *testing.T) {
	srv := newPluginTestServer(t)

	// Listing registers the on-disk plugin (disabled).
	var list struct {
		Enabled bool            `json:"enabled"`
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	assert.True(t, list.Enabled)
	require.Len(t, list.Plugins, 1)
	demo := list.Plugins[0]
	assert.Equal(t, "demo", demo.Name)
	assert.False(t, demo.Enabled)
	assert.Equal(t, "disabled", demo.State)
	assert.Equal(t, []string{"OnTransactionCreate"}, demo.Hooks)
	assert.Equal(t, []string{"labels:write"}, demo.Granted)

	id := strconv.FormatInt(demo.ID, 10)

	// Enable -> loaded and running.
	var enabled api.PluginDTO
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/"+id+"/enable", nil, &enabled))
	assert.True(t, enabled.Enabled)
	assert.True(t, enabled.Loaded)
	assert.Equal(t, "loaded", enabled.State)

	// Get reflects the loaded state.
	var got api.PluginDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/"+id, &got))
	assert.True(t, got.Loaded)

	// Reload while enabled stays loaded.
	var reloaded api.PluginDTO
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/"+id+"/reload", nil, &reloaded))
	assert.True(t, reloaded.Loaded)

	// Disable -> unloaded.
	var disabled api.PluginDTO
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/"+id+"/disable", nil, &disabled))
	assert.False(t, disabled.Enabled)
	assert.False(t, disabled.Loaded)
	assert.Equal(t, "disabled", disabled.State)
}

func TestPluginUninstall(t *testing.T) {
	srv := newPluginTestServer(t)

	// Discover the on-disk plugin.
	var list struct {
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	require.Len(t, list.Plugins, 1)
	id := strconv.FormatInt(list.Plugins[0].ID, 10)

	// Uninstall it (the demo plugin declares no OnUninstall hook, so HookRan is false).
	var res api.UninstallResultDTO
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/plugins/"+id, &res))
	assert.True(t, res.Uninstalled)
	assert.False(t, res.HookRan)
	assert.Equal(t, "demo", res.Name)

	// It is gone from the list and a second uninstall 404s.
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	assert.Empty(t, list.Plugins)
	require.Equal(t, http.StatusNotFound, deleteJSON(t, srv, "/api/v1/plugins/"+id, nil))
}

func TestPluginGetNotFound(t *testing.T) {
	srv := newPluginTestServer(t)
	require.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/plugins/9999", nil))
}

func TestPluginEnableNonexistent(t *testing.T) {
	srv := newPluginTestServer(t)
	require.Equal(t, http.StatusNotFound, postJSON(t, srv, "/api/v1/plugins/9999/enable", nil, nil))
}

// TestPluginsListWhenDisabled checks that with no plugin manager wired (the plugin
// system disabled — the default), the list endpoint still responds 200 with an
// empty, disabled list rather than a routing 404, so the dashboard can show a clean
// "disabled" state.
func TestPluginsListWhenDisabled(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	s := api.New(api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// No PluginManager: the plugin system is disabled.
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	var list struct {
		Enabled bool            `json:"enabled"`
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	assert.False(t, list.Enabled)
	assert.Empty(t, list.Plugins)
}

// TestMarketplaceBrowseWhenDisabled checks that with no plugin manager wired, the
// marketplace catalog endpoint responds 200 {available:false} rather than falling
// through to /plugins/{id} (which 400s on the non-numeric "registry"). Before the
// read route was registered unconditionally, the dashboard's Marketplace page
// surfaced that 400 as an error instead of a clean "unavailable" state.
func TestMarketplaceBrowseWhenDisabled(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	s := api.New(api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// No PluginManager: the plugin system is disabled.
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	var cat struct {
		Available bool                    `json:"available"`
		Plugins   []api.RegistryPluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/registry", &cat))
	assert.False(t, cat.Available)
	assert.Empty(t, cat.Plugins)
}
