package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/plugins/registry"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeRegistryServer serves a registry index plus raw plugin files in the layout
// the client expects: <repo>/raw/<ref>/<path>/<file>.
func fakeRegistryServer(t *testing.T, name string, files map[string]string) *httptest.Server {
	t.Helper()
	var srv *httptest.Server

	pluginPath := "plugins/" + name
	var refs []registry.FileRef
	rawByPath := map[string]string{}
	for fname, body := range files {
		sum := sha256.Sum256([]byte(body))
		refs = append(refs, registry.FileRef{Path: fname, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))})
		rawByPath["/raw/main/"+pluginPath+"/"+fname] = body
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })

	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		idx := registry.Index{
			SchemaVersion: 1,
			Repository:    srv.URL,
			Plugins: []registry.Entry{{
				Name: name, Version: "1.0.0", Description: "A demo plugin.", Author: "tester",
				License: "MIT", Homepage: "https://example.com", Runtime: "lua", Entrypoint: "main.lua",
				Hooks: []string{"OnTransactionCreate"}, Capabilities: []string{"labels:write"},
				CapabilityTier: "write", UI: &registry.UIRef{Title: "Demo Page", Icon: "chart"},
				Path: pluginPath, Files: refs, ContentHash: aggHash(refs),
			}},
		}
		_ = json.NewEncoder(w).Encode(idx)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := rawByPath[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func aggHash(files []registry.FileRef) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\n", f.Path, strings.ToLower(f.SHA256))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func newMarketplaceTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)

	reg := fakeRegistryServer(t, "demo", map[string]string{
		"plugin.toml": "name=\"demo\"\nruntime=\"lua\"\nversion=\"1.0.0\"\nhooks=[\"OnTransactionCreate\"]\ncapabilities=[\"labels:write\"]\n",
		"main.lua":    "function OnTransactionCreate(txn) end\n",
		"README.md":   "# demo\n",
	})

	dir := t.TempDir()
	bus := events.NewBus()
	mgr := plugins.NewManager(plugins.Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes: map[string]plugins.Runtime{plugins.RuntimeLua: plugins.NewLuaRuntime()},
		Registry: registry.New(reg.URL+"/index.json", "main", reg.Client(), registry.DefaultLimits()),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s := api.New(api.Options{
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		PluginManager: mgr,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, dir
}

func TestMarketplaceBrowseAndInstall(t *testing.T) {
	srv, dir := newMarketplaceTestServer(t)

	// Browse the catalog.
	var cat struct {
		Available bool                    `json:"available"`
		Plugins   []api.RegistryPluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/registry", &cat))
	assert.True(t, cat.Available)
	require.Len(t, cat.Plugins, 1)
	assert.Equal(t, "demo", cat.Plugins[0].Name)
	assert.False(t, cat.Plugins[0].Installed)
	assert.Equal(t, "write", cat.Plugins[0].CapabilityTier)
	require.NotNil(t, cat.Plugins[0].UI, "the index's ui metadata reaches the catalog DTO")
	assert.Equal(t, "Demo Page", cat.Plugins[0].UI.Title)
	assert.Equal(t, "chart", cat.Plugins[0].UI.Icon)

	// Install it.
	var installed api.PluginDTO
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/registry/demo/install", nil, &installed))
	assert.Equal(t, "demo", installed.Name)
	assert.False(t, installed.Enabled, "installed plugin starts disabled")
	assert.Equal(t, "disabled", installed.State)

	// Files were written to plugins.dir, integrity-verified.
	b, err := os.ReadFile(filepath.Join(dir, "demo", "main.lua"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "OnTransactionCreate")

	// Browsing again reflects the installed state.
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/registry", &cat))
	require.Len(t, cat.Plugins, 1)
	assert.True(t, cat.Plugins[0].Installed)

	// And the normal plugins list now includes it.
	var list struct {
		Enabled bool            `json:"enabled"`
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	found := false
	for _, p := range list.Plugins {
		if p.Name == "demo" {
			found = true
		}
	}
	assert.True(t, found, "installed plugin appears in the plugins list")
}

func TestMarketplaceUnavailableWhenNoRegistry(t *testing.T) {
	// A plugin manager with no Registry configured: browse reports unavailable.
	srv := newPluginTestServer(t) // from plugins_test.go; no registry wired
	var cat struct {
		Available bool                    `json:"available"`
		Plugins   []api.RegistryPluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/registry", &cat))
	assert.False(t, cat.Available)
	assert.Empty(t, cat.Plugins)
}
