package pluginsource

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/source"
)

type fakeProducer struct {
	raw        json.RawMessage
	gotName    string
	gotHook    plugins.Hook
	gotPayload json.RawMessage
}

func (f *fakeProducer) Produce(_ context.Context, name string, hook plugins.Hook, payload json.RawMessage) (json.RawMessage, error) {
	f.gotName, f.gotHook, f.gotPayload = name, hook, payload
	return f.raw, nil
}

func testManifest() plugins.Manifest {
	return plugins.Manifest{
		Name:   "demo",
		Source: &plugins.SourceManifest{Type: "acme-card", Archetype: "pull"},
		Net:    &plugins.NetManifest{Allow: []string{"api.acme.example"}},
	}
}

func TestDescriptorKeysOnPluginName(t *testing.T) {
	src := New("demo", testManifest(), &fakeProducer{})
	d := src.Descriptor()
	assert.Equal(t, "plugin:demo", d.Type, "engine key + provenance stamp is plugin:<name>")
	assert.Equal(t, source.ArchetypePull, d.Archetype)
	assert.Equal(t, "acme-card", d.Title, "the manifest source.type is the human title")
	assert.Equal(t, []string{"api.acme.example"}, d.Egress, "[net].allow surfaces as egress")
}

func TestFetchNamespacesAndStamps(t *testing.T) {
	// The guest claims source "simplefin" and emits an exponential date (as gopher-lua
	// does) and an un-namespaced id; the adapter must override all three.
	raw := json.RawMessage(`{
		"source": "simplefin",
		"accounts": [{
			"external_id": "acct-1",
			"org": {"id": "acme", "name": "ACME"},
			"name": "ACME Card",
			"currency": "USD",
			"balance": "100.00",
			"balance_date": 1.7e9,
			"transactions": [
				{"external_id": "tx-1", "amount": "-12.50", "date": 1.7e9, "description": "Blue Bottle", "payee": "Blue Bottle Coffee"}
			]
		}]
	}`)
	prod := &fakeProducer{raw: raw}
	src := New("demo", testManifest(), prod)

	batch, err := src.Fetch(context.Background(), time.Unix(1690000000, 0), "")
	require.NoError(t, err)

	// Provenance is forced to plugin:<name>, NOT the guest's "simplefin".
	assert.Equal(t, "plugin:demo", batch.Source)
	require.Len(t, batch.Accounts, 1)
	acct := batch.Accounts[0]
	assert.Equal(t, "plugin:demo:acct-1", acct.ExternalID, "account id namespaced")
	assert.Equal(t, "plugin:demo:acme", acct.Org.ID, "org id namespaced")
	assert.Equal(t, int64(1700000000), acct.BalanceDate, "exponential balance_date rounded to int64")
	require.Len(t, acct.Transactions, 1)
	txn := acct.Transactions[0]
	assert.Equal(t, "plugin:demo:tx-1", txn.ExternalID, "txn id namespaced")
	assert.Equal(t, int64(1700000000), txn.Date, "exponential date rounded to int64")
	assert.Equal(t, "-12.50", txn.Amount, "decimal amount preserved as a string")

	// The hook was addressed by plugin name with the OnFetch hook and a since/cursor.
	assert.Equal(t, "demo", prod.gotName)
	assert.Equal(t, plugins.HookFetch, prod.gotHook)
	assert.Contains(t, string(prod.gotPayload), `"since":1690000000`)
}

func TestFetchRejectsEmptyIDs(t *testing.T) {
	// An account with no external_id would collapse rows into one namespaced key, so
	// the adapter rejects it loudly.
	raw := json.RawMessage(`{"accounts":[{"external_id":"","transactions":[]}]}`)
	src := New("demo", testManifest(), &fakeProducer{raw: raw})
	_, err := src.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)

	raw = json.RawMessage(`{"accounts":[{"external_id":"a","transactions":[{"external_id":""}]}]}`)
	src = New("demo", testManifest(), &fakeProducer{raw: raw})
	_, err = src.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
}

func TestFetchZeroSinceSendsZero(t *testing.T) {
	prod := &fakeProducer{raw: json.RawMessage(`{"accounts":[]}`)}
	src := New("demo", testManifest(), prod)
	_, err := src.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	assert.Contains(t, string(prod.gotPayload), `"since":0`, "a zero since sends 0, not a negative epoch")
}
