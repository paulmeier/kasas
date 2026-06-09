package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/paulmeier/kasas/internal/plugins/registry"
)

// ErrRegistryDisabled is returned by the marketplace methods when no community
// registry is configured (Options.Registry is nil), so the API can map it to a
// clean "marketplace unavailable" response rather than a generic error.
var ErrRegistryDisabled = errors.New("plugins: community registry is disabled")

// RegistrySource is the registry-client surface the manager needs: fetch the
// catalog and download a plugin's verified files into a staging directory. The
// concrete implementation is *registry.Client; the interface keeps the manager
// testable without a network.
type RegistrySource interface {
	Catalog(ctx context.Context) (*registry.Index, error)
	Download(ctx context.Context, repository string, entry registry.Entry, destDir string) error
}

// CatalogEntry is a registry entry merged with this host's local install state, so
// the dashboard can show "Install", "Installed", or "Update available" for each
// listed plugin without a second round-trip.
type CatalogEntry struct {
	registry.Entry
	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

// RegistryEnabled reports whether the community marketplace is configured.
func (m *Manager) RegistryEnabled() bool { return m != nil && m.registry != nil }

// Catalog fetches the community registry index and annotates each entry with this
// host's install state. Installing a plugin remains a separate, explicit action.
func (m *Manager) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	if m == nil {
		return nil, ErrDisabled
	}
	if m.registry == nil {
		return nil, ErrRegistryDisabled
	}
	idx, err := m.registry.Catalog(ctx)
	if err != nil {
		return nil, err
	}

	// What is installed on disk right now (the authoritative source of the running
	// version), falling back to the DB row for a plugin registered but missing from
	// disk.
	installed := map[string]string{} // name -> version
	for _, d := range m.discover() {
		if d.Valid() {
			installed[d.Name] = d.Manifest.Version
		}
	}
	if rows, err := m.store.ListPlugins(ctx); err == nil {
		for _, r := range rows {
			if _, ok := installed[r.Name]; !ok {
				installed[r.Name] = r.Version
			}
		}
	}

	out := make([]CatalogEntry, 0, len(idx.Plugins))
	for _, e := range idx.Plugins {
		ce := CatalogEntry{Entry: e}
		if ver, ok := installed[e.Name]; ok {
			ce.Installed = true
			ce.InstalledVersion = ver
			ce.UpdateAvailable = ver != "" && ver != e.Version
		}
		out = append(out, ce)
	}
	return out, nil
}

// Install downloads a plugin from the community registry, verifies its integrity,
// and writes it into the plugins directory, then registers it (disabled, like any
// freshly-discovered plugin). Installing over an already-loaded plugin (an update)
// stops it, swaps the files atomically, and reloads it so the new code takes
// effect. Enabling a newly-installed plugin remains a separate admin action — this
// method never starts running new third-party code on its own.
func (m *Manager) Install(ctx context.Context, name string) (Status, error) {
	if m == nil {
		return Status{}, ErrDisabled
	}
	if m.registry == nil {
		return Status{}, ErrRegistryDisabled
	}
	if !nameRE.MatchString(name) {
		return Status{}, fmt.Errorf("invalid plugin name %q", name)
	}

	idx, err := m.registry.Catalog(ctx)
	if err != nil {
		return Status{}, err
	}
	entry, ok := idx.Find(name)
	if !ok {
		return Status{}, fmt.Errorf("plugin %q is not in the registry", name)
	}

	// Stage into a temp directory inside plugins.dir so the final swap is an
	// atomic same-filesystem rename. The dot-prefix keeps discovery from ever
	// treating a half-written staging dir as a plugin.
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return Status{}, fmt.Errorf("create plugins dir: %w", err)
	}
	staging, err := os.MkdirTemp(m.dir, ".staging-"+name+"-")
	if err != nil {
		return Status{}, err
	}
	defer os.RemoveAll(staging) // no-op once renamed away

	if err := m.registry.Download(ctx, idx.Repository, entry, staging); err != nil {
		return Status{}, err
	}

	// Defense in depth beyond the registry's gate: the bytes we just verified must
	// also parse as a manifest whose name matches the directory we are about to
	// create. This catches a registry entry that points at a mismatched plugin.
	mdata, err := os.ReadFile(filepath.Join(staging, manifestFile))
	if err != nil {
		return Status{}, fmt.Errorf("staged plugin has no %s", manifestFile)
	}
	man, err := ParseManifest(mdata)
	if err != nil {
		return Status{}, fmt.Errorf("staged plugin manifest is invalid: %w", err)
	}
	if man.Name != name {
		return Status{}, fmt.Errorf("staged manifest name %q does not match plugin %q", man.Name, name)
	}

	target := filepath.Join(m.dir, name)

	// If this name is currently loaded, it is an update: stop the old instance
	// before its files change underneath it, and remember to reload after.
	m.mu.RLock()
	_, wasLoaded := m.plugins[name]
	m.mu.RUnlock()
	if wasLoaded {
		m.unload(name)
	}

	if err := os.RemoveAll(target); err != nil {
		return Status{}, fmt.Errorf("replace existing plugin: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return Status{}, fmt.Errorf("install plugin: %w", err)
	}

	// Register/refresh the DB row from disk (inserts a new plugin as disabled).
	if _, _, err := m.register(ctx); err != nil {
		return Status{}, err
	}
	row, err := m.store.GetPluginByName(ctx, name)
	if err != nil {
		return Status{}, err
	}

	// Reload only if it was already running (an update); a fresh install stays
	// disabled until an admin enables it.
	if wasLoaded {
		if d, ok := m.discoverByName(name); ok && d.Valid() {
			if err := m.load(ctx, row, d); err != nil {
				m.markError(ctx, row.ID, err)
				return Status{}, fmt.Errorf("installed but failed to reload: %w", err)
			}
		}
	}
	m.logger.Info("plugin installed from registry", "plugin", name, "version", entry.Version, "reloaded", wasLoaded)
	return m.Get(ctx, row.ID)
}
