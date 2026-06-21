package plugins

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/ledger"
)

// UninstallResult reports the outcome of an uninstall: the plugin's name, whether
// its OnUninstall cleanup hook ran, and any error that hook produced. A hook error
// does NOT fail the uninstall — the plugin owns its cleanup, and kasas always
// removes it regardless so a buggy hook can never make a plugin un-removable. The
// error is surfaced so the operator knows cleanup may be incomplete.
type UninstallResult struct {
	Name      string
	HookRan   bool
	HookError string
}

// Uninstall removes a plugin entirely: it stops any running instance, runs the
// plugin's OnUninstall cleanup hook (best-effort), deletes the plugin's files from
// the plugins directory, and removes its control-plane row. This is the inverse of
// installing/dropping a plugin in; enabling/disabling only toggles execution, while
// uninstall makes the plugin gone.
//
// Cleanup is the plugin's responsibility: if it declared OnUninstall, kasas loads a
// fresh, isolated instance (with the plugin's granted capabilities) and invokes the
// hook so the plugin can undo labels/extensions it created. Whether the hook
// succeeds, errors, or times out, the removal proceeds.
func (m *Manager) Uninstall(ctx context.Context, id int64) (UninstallResult, error) {
	if m == nil {
		return UninstallResult{}, ErrDisabled
	}
	row, err := m.store.GetPlugin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return UninstallResult{}, ErrPluginNotFound
	}
	if err != nil {
		return UninstallResult{}, err
	}
	res := UninstallResult{Name: row.Name}

	// Stop any live instance first, so the cleanup hook runs in isolation and no bus
	// events are delivered into a plugin that is being torn down.
	m.unload(row.Name)

	// Run the cleanup hook if the on-disk plugin is loadable and declares it.
	if d, ok := m.discoverByName(row.Name); ok && d.Valid() && manifestDeclares(d.Manifest, HookUninstall) {
		res.HookRan = true
		if herr := m.runUninstallHook(ctx, row, d); herr != nil {
			res.HookError = herr.Error()
			m.logger.Warn("plugin OnUninstall hook failed (removing anyway)", "plugin", row.Name, "error", herr)
		}
	}

	// Purge any ledger rows this plugin produced as a source (ADR 0005). A plugin
	// owns its rows through dedup, so removing the plugin removes them — and since
	// those rows are read-only to the manual-edit API, uninstall is their only
	// removal path. Keyed on the source stamp (plugin:<name>), so it is a no-op for a
	// plugin that never produced any. Like the cleanup hook, a failure is logged but
	// does not block removal.
	if m.emitter != nil {
		if pr, perr := ledger.PurgeSource(ctx, m.store, m.emitter, SourceType(row.Name)); perr != nil {
			m.logger.Error("purge plugin source rows failed (removing anyway)", "plugin", row.Name, "error", perr)
		} else if pr.Accounts > 0 || pr.Transactions > 0 {
			m.logger.Info("purged plugin source rows", "plugin", row.Name, "accounts", pr.Accounts, "transactions", pr.Transactions)
		}
	}

	// Remove the plugin's files. row.Name is a validated slug, so this stays inside
	// the plugins directory. The user config override file lives next to the
	// plugin's directory and belongs to it, so it goes too.
	if m.dir != "" {
		if err := os.RemoveAll(filepath.Join(m.dir, row.Name)); err != nil {
			return res, fmt.Errorf("remove plugin files: %w", err)
		}
		if err := os.Remove(userConfigPath(m.dir, row.Name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, fmt.Errorf("remove plugin config file: %w", err)
		}
	}

	// Remove the durable control-plane row.
	if _, err := m.store.DeletePlugin(ctx, id); err != nil {
		return res, fmt.Errorf("delete plugin record: %w", err)
	}

	m.logger.Info("plugin uninstalled", "plugin", row.Name, "hook_ran", res.HookRan, "hook_error", res.HookError != "")
	return res, nil
}

// runUninstallHook loads a fresh, isolated instance of the plugin and invokes its
// OnUninstall hook under the per-hook timeout, then closes it. It deliberately does
// not register the instance in the live set or start a worker — this is a one-shot
// invocation purely for cleanup.
func (m *Manager) runUninstallHook(ctx context.Context, row db.Plugin, d Discovered) error {
	rt, ok := m.runtimes[d.Manifest.Runtime]
	if !ok {
		return fmt.Errorf("no runtime registered for %q", d.Manifest.Runtime)
	}
	caps := intersectCaps(d.Manifest.Capabilities, decodeCapList(row.GrantedCapabilities))
	host := newHost(m.store, m.emitter, caps, row.Name, m.searchLimit, m.logger,
		newConfigStore(m.dir, row.Name, d.Manifest.Config), m.netGateFor(caps, row, d.Manifest))

	// Cleanup should see the same effective config the plugin ran with, but a
	// broken override file must never make a plugin un-removable: fall back to
	// the manifest defaults instead of failing.
	man := d.Manifest
	if eff, cerr := effectiveConfig(m.dir, row.Name, d.Manifest.Config); cerr == nil {
		man.Config = eff
	} else {
		m.logger.Warn("plugin config unreadable for uninstall, using manifest defaults", "plugin", row.Name, "error", cerr)
	}

	inst, err := rt.Load(ctx, man, d.Dir, host)
	if err != nil {
		return fmt.Errorf("load for uninstall: %w", err)
	}
	defer func() {
		if cerr := inst.Close(); cerr != nil {
			m.logger.Warn("close uninstall instance failed", "plugin", row.Name, "error", cerr)
		}
	}()

	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	err = inst.Invoke(cctx, HookUninstall, HookEvent{Type: "plugin.uninstall", OccurredAt: time.Now()})
	if errors.Is(err, ErrHookNotImpl) {
		return nil // declared but not implemented: nothing to run (defensive)
	}
	return err
}

// manifestDeclares reports whether the manifest's hook list contains h.
func manifestDeclares(m Manifest, h Hook) bool {
	for _, x := range m.Hooks {
		if x == h {
			return true
		}
	}
	return false
}
