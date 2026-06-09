package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins/registry"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeRegistry is an in-memory RegistrySource for testing the manager's install
// orchestration without a network. Download writes the per-entry files map.
type fakeRegistry struct {
	index *registry.Index
	files map[string]map[string]string // plugin name -> {filename: contents}
}

func (f *fakeRegistry) Catalog(context.Context) (*registry.Index, error) { return f.index, nil }

func (f *fakeRegistry) Download(_ context.Context, _ string, entry registry.Entry, destDir string) error {
	for name, body := range f.files[entry.Name] {
		if err := os.WriteFile(filepath.Join(destDir, name), []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func luaPluginFiles(name string) map[string]string {
	return map[string]string{
		"plugin.toml": "name=\"" + name + "\"\nruntime=\"lua\"\nversion=\"1.0.0\"\nhooks=[\"OnTransactionCreate\"]\ncapabilities=[\"labels:write\"]\n",
		"main.lua":    "function OnTransactionCreate(txn) end\n",
		"README.md":   "# " + name + "\n",
	}
}

func newMarketplaceManager(t *testing.T, dir string, reg RegistrySource) *Manager {
	t.Helper()
	return NewManager(Options{
		Store:    testutil.NewStore(t),
		Bus:      events.NewBus(),
		Dir:      dir,
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		Registry: reg,
		Logger:   testLogger(),
	})
}

func TestInstallWritesAndRegistersDisabled(t *testing.T) {
	dir := t.TempDir()
	reg := &fakeRegistry{
		index: &registry.Index{
			SchemaVersion: 1,
			Repository:    "https://example.com/repo",
			Plugins:       []registry.Entry{{Name: "coffee", Version: "1.0.0", Runtime: "lua"}},
		},
		files: map[string]map[string]string{"coffee": luaPluginFiles("coffee")},
	}
	mgr := newMarketplaceManager(t, dir, reg)

	st, err := mgr.Install(context.Background(), "coffee")
	require.NoError(t, err)
	assert.Equal(t, "coffee", st.Name)
	assert.False(t, st.Enabled, "a freshly installed plugin is disabled")
	assert.False(t, st.Loaded, "a freshly installed plugin is not loaded")
	assert.Equal(t, "disabled", st.State)

	// Files landed on disk in the plugin directory.
	b, err := os.ReadFile(filepath.Join(dir, "coffee", "main.lua"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "OnTransactionCreate")

	// And it shows up in the normal plugin list.
	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	_, ok := findByName(statuses, "coffee")
	assert.True(t, ok, "installed plugin is discoverable")
}

func TestInstallUnknownPlugin(t *testing.T) {
	reg := &fakeRegistry{index: &registry.Index{SchemaVersion: 1, Repository: "x"}}
	mgr := newMarketplaceManager(t, t.TempDir(), reg)
	_, err := mgr.Install(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the registry")
}

func TestInstallRejectsManifestNameMismatch(t *testing.T) {
	dir := t.TempDir()
	reg := &fakeRegistry{
		index: &registry.Index{
			SchemaVersion: 1, Repository: "x",
			Plugins: []registry.Entry{{Name: "coffee", Version: "1.0.0", Runtime: "lua"}},
		},
		// The downloaded manifest claims a different name than requested.
		files: map[string]map[string]string{"coffee": luaPluginFiles("evil")},
	}
	mgr := newMarketplaceManager(t, dir, reg)
	_, err := mgr.Install(context.Background(), "coffee")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match")
	// Nothing should have been left installed under the requested name.
	_, statErr := os.Stat(filepath.Join(dir, "coffee"))
	assert.True(t, os.IsNotExist(statErr), "no plugin dir left behind on failure")
}

func TestRegistryDisabled(t *testing.T) {
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: t.TempDir(),
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()}, Logger: testLogger(),
	})
	assert.False(t, mgr.RegistryEnabled())
	_, err := mgr.Catalog(context.Background())
	assert.ErrorIs(t, err, ErrRegistryDisabled)
	_, err = mgr.Install(context.Background(), "coffee")
	assert.ErrorIs(t, err, ErrRegistryDisabled)
}

func TestCatalogMergesInstalledState(t *testing.T) {
	dir := t.TempDir()
	// Pre-install "coffee" on disk at an older version.
	writePlugin(t, dir, "coffee",
		"name=\"coffee\"\nruntime=\"lua\"\nversion=\"0.9.0\"\nhooks=[\"OnTransactionCreate\"]",
		"function OnTransactionCreate(txn) end")

	reg := &fakeRegistry{
		index: &registry.Index{
			SchemaVersion: 1, Repository: "x",
			Plugins: []registry.Entry{
				{Name: "coffee", Version: "1.0.0", Runtime: "lua"}, // newer than installed
				{Name: "other", Version: "2.0.0", Runtime: "lua"},  // not installed
			},
		},
	}
	mgr := newMarketplaceManager(t, dir, reg)

	entries, err := mgr.Catalog(context.Background())
	require.NoError(t, err)
	byName := map[string]CatalogEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	coffee := byName["coffee"]
	assert.True(t, coffee.Installed)
	assert.Equal(t, "0.9.0", coffee.InstalledVersion)
	assert.True(t, coffee.UpdateAvailable, "installed 0.9.0 < registry 1.0.0")

	other := byName["other"]
	assert.False(t, other.Installed)
	assert.False(t, other.UpdateAvailable)
}
