package plugins

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

const sourceManifest = `name="source-mgr"
runtime="lua"
hooks=["OnFetch"]
capabilities=["source:provide"]

[source]
type = "acme-card"
`

const sourceLua = `function OnFetch(req)
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

// TestManagerProduce drives the producer hook end-to-end through the manager's
// worker queue (ADR 0005): a disabled plugin is unreachable, and an enabled
// source:provide plugin returns its ImportBatch JSON.
func TestManagerProduce(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "source-mgr", sourceManifest, sourceLua)

	store := testutil.NewStore(t)
	bus := events.NewBus()
	mgr := NewManager(Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	pg, ok := findByName(statuses, "source-mgr")
	require.True(t, ok)

	// Disabled (not loaded): the producer is unreachable.
	_, err = mgr.Produce(context.Background(), "source-mgr", HookFetch, json.RawMessage(`{"since":0,"cursor":""}`))
	assert.ErrorIs(t, err, ErrPluginNotFound)

	_, err = mgr.SetEnabled(context.Background(), pg.ID, true, nil)
	require.NoError(t, err)
	defer mgr.unload("source-mgr")

	raw, err := mgr.Produce(context.Background(), "source-mgr", HookFetch, json.RawMessage(`{"since":0,"cursor":""}`))
	require.NoError(t, err)

	var batch struct {
		Accounts []struct {
			ExternalID   string `json:"external_id"`
			Transactions []struct {
				ExternalID string `json:"external_id"`
				Amount     string `json:"amount"`
			} `json:"transactions"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(raw, &batch))
	require.Len(t, batch.Accounts, 1)
	require.Len(t, batch.Accounts[0].Transactions, 1)
	assert.Equal(t, "tx-1", batch.Accounts[0].Transactions[0].ExternalID)
	assert.Equal(t, "-12.50", batch.Accounts[0].Transactions[0].Amount)

	// An unknown plugin is a clean not-found.
	_, err = mgr.Produce(context.Background(), "missing", HookFetch, nil)
	assert.ErrorIs(t, err, ErrPluginNotFound)
}
