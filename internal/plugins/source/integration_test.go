package pluginsource_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	pluginsource "github.com/paulmeier/kasas/internal/plugins/source"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/testutil"
)

// testRegistrar mirrors cmd/kasas's pluginSourceRegistrar: it wires a source:provide
// plugin into the engine by building the adapter (driven by the manager) and a poller.
type testRegistrar struct {
	engine  *poller.Engine
	mgr     *plugins.Manager
	store   db.Store
	emitter *events.Emitter
	logger  *slog.Logger
}

func (r *testRegistrar) RegisterSource(ctx context.Context, name string, m plugins.Manifest) error {
	src := pluginsource.New(name, m, r.mgr)
	p := poller.New(poller.Options{Store: r.store, Source: src, Logger: r.logger, Emitter: r.emitter, Interval: time.Hour})
	return r.engine.AddPoller(ctx, p)
}

func (r *testRegistrar) UnregisterSource(ctx context.Context, name string) error {
	return r.engine.RemovePoller(ctx, pluginsource.SourceType(name))
}

const sourcePluginManifest = `name="acme-card"
runtime="lua"
hooks=["OnFetch"]
capabilities=["source:provide"]

[source]
type = "acme-card"
`

const sourcePluginLua = `function OnFetch(req)
  return {
    source = "acme-card",
    accounts = {
      {
        external_id = "acct-1",
        org = { id = "acme", name = "ACME" },
        name = "ACME Card",
        currency = "USD",
        transactions = {
          { external_id = "tx-1", amount = "-12.50", date = 1700000000, description = "Blue Bottle", payee = "Blue Bottle Coffee" },
        },
      },
    },
  }
end`

func writeSourcePlugin(t *testing.T, dir string) {
	t.Helper()
	pdir := filepath.Join(dir, "acme-card")
	require.NoError(t, os.MkdirAll(pdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "plugin.toml"), []byte(sourcePluginManifest), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "main.lua"), []byte(sourcePluginLua), 0o644))
}

// TestPluginSourceEndToEnd drives ADR 0005 through the real seams: enabling a
// source:provide plugin registers a source on the engine; syncing it persists the
// plugin's batch (stamped plugin:<name>, ids namespaced); a re-sync is idempotent;
// and uninstalling the plugin purges the rows it produced.
func TestPluginSourceEndToEnd(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := testutil.NewStore(t)
	bus := events.NewBus()
	emitter := events.NewEmitter(bus)

	dir := t.TempDir()
	writeSourcePlugin(t, dir)

	mgr := plugins.NewManager(plugins.Options{
		Store: store, Emitter: emitter, Bus: bus, Dir: dir,
		Runtimes:    map[string]plugins.Runtime{plugins.RuntimeLua: plugins.NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: logger,
	})
	engine := poller.NewEngine()
	mgr.SetSourceRegistrar(&testRegistrar{engine: engine, mgr: mgr, store: store, emitter: emitter, logger: logger})

	// Reconcile disk -> DB rows, then enable the plugin (which registers the source).
	statuses, err := mgr.List(ctx)
	require.NoError(t, err)
	pg, ok := findStatus(statuses, "acme-card")
	require.True(t, ok)

	// Before enable, the engine has no sources.
	srcs, err := engine.Sources(ctx)
	require.NoError(t, err)
	require.Empty(t, srcs)

	_, err = mgr.SetEnabled(ctx, pg.ID, true, nil)
	require.NoError(t, err)

	// The plugin source now appears on the engine, typed plugin:<name>.
	srcs, err = engine.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.Equal(t, "plugin:acme-card", srcs[0].Type)
	assert.Equal(t, "acme-card", srcs[0].Title)
	assert.Equal(t, "pull", srcs[0].Archetype)
	assert.True(t, srcs[0].Connected)

	// Sync it: the engine persists the plugin's batch.
	res, err := engine.SyncSource(ctx, "plugin:acme-card")
	require.NoError(t, err)
	assert.Equal(t, 1, res.Accounts)
	assert.Equal(t, 1, res.NewTransactions)

	// The row landed, with namespaced ids and a forged-proof provenance stamp.
	txn, err := store.GetTransaction(ctx, "plugin:acme-card:tx-1")
	require.NoError(t, err)
	assert.Equal(t, "plugin:acme-card", txn.Source, "engine stamps plugin:<name>, not the guest's 'acme-card'")
	assert.Equal(t, "plugin:acme-card:acct-1", txn.AccountID)
	assert.Equal(t, "-12.50", txn.Amount)
	assert.Equal(t, int64(1700000000), txn.Date)
	acct, err := store.GetAccount(ctx, "plugin:acme-card:acct-1")
	require.NoError(t, err)
	assert.Equal(t, "plugin:acme-card", acct.Source)

	// A re-sync is idempotent: dedup by the namespaced id inserts nothing new.
	res, err = engine.SyncSource(ctx, "plugin:acme-card")
	require.NoError(t, err)
	assert.Equal(t, 0, res.NewTransactions, "re-sync inserts no duplicate")

	// The produced row is NOT manual, so the manual-edit gate (source != "manual")
	// makes it read-only — the same 409 a synced row gets.
	assert.NotEqual(t, "manual", txn.Source)

	// Uninstalling the plugin removes it from the engine AND purges its rows.
	_, err = mgr.Uninstall(ctx, pg.ID)
	require.NoError(t, err)

	srcs, err = engine.Sources(ctx)
	require.NoError(t, err)
	require.Empty(t, srcs, "the source is gone from the engine")

	_, err = store.GetTransaction(ctx, "plugin:acme-card:tx-1")
	assert.ErrorIs(t, err, sql.ErrNoRows, "produced transaction purged on uninstall")
	_, err = store.GetAccount(ctx, "plugin:acme-card:acct-1")
	assert.ErrorIs(t, err, sql.ErrNoRows, "produced account purged on uninstall")
}

func findStatus(ss []plugins.Status, name string) (plugins.Status, bool) {
	for _, s := range ss {
		if s.Name == name {
			return s, true
		}
	}
	return plugins.Status{}, false
}
