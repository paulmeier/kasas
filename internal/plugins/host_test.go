package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/testutil"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newTestHost returns a host facade over a fresh seeded store with the given
// capabilities, plus the store so tests can assert persisted state.
func newTestHost(t *testing.T, caps ...Capability) (*hostFacade, db.Store) {
	t.Helper()
	store := testutil.NewStore(t)
	testutil.Seed(t, store)
	emitter := events.NewEmitter(events.NewBus())
	return newHost(store, emitter, newCapSet(caps), "tester", 0, testLogger(), nil, nil), store
}

func labelsOf(t *testing.T, store db.Store, id string) map[string]string {
	t.Helper()
	row, err := store.GetTransaction(context.Background(), id)
	require.NoError(t, err)
	return labels.Decode(row.Labels)
}

func recentEventTypes(t *testing.T, store db.Store) []string {
	t.Helper()
	rows, err := store.ListRecentEvents(context.Background(), 50)
	require.NoError(t, err)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.EventType
	}
	return out
}

func TestHostApplyLabelsDeniedWithoutCapability(t *testing.T) {
	h, store := newTestHost(t, CapTransactionsRead) // no labels:write
	err := h.ApplyLabels(context.Background(), "tx-1", map[string]string{"category": "food"})
	assert.ErrorIs(t, err, ErrCapabilityDenied)
	assert.Empty(t, labelsOf(t, store, "tx-1"), "no label should be written when the capability is denied")
}

func TestHostApplyLabelsWithCapabilityLandsAndEmits(t *testing.T) {
	h, store := newTestHost(t, CapLabelsWrite)
	require.NoError(t, h.ApplyLabels(context.Background(), "tx-1", map[string]string{"category": "food"}))

	assert.Equal(t, map[string]string{"category": "food"}, labelsOf(t, store, "tx-1"))
	assert.Contains(t, recentEventTypes(t, store), events.TypeLabelApplied, "a label.applied event is emitted")
}

func TestHostApplyLabelsMerges(t *testing.T) {
	h, store := newTestHost(t, CapLabelsWrite)
	require.NoError(t, h.ApplyLabels(context.Background(), "tx-1", map[string]string{"category": "food"}))
	require.NoError(t, h.ApplyLabels(context.Background(), "tx-1", map[string]string{"person": "dad"}))
	assert.Equal(t, map[string]string{"category": "food", "person": "dad"}, labelsOf(t, store, "tx-1"),
		"apply merges rather than replaces")
}

func TestHostRemoveLabels(t *testing.T) {
	h, store := newTestHost(t, CapLabelsWrite)
	require.NoError(t, h.ApplyLabels(context.Background(), "tx-1", map[string]string{"category": "food", "person": "dad"}))
	require.NoError(t, h.RemoveLabels(context.Background(), "tx-1", []string{"category"}))
	assert.Equal(t, map[string]string{"person": "dad"}, labelsOf(t, store, "tx-1"))
}

func TestHostSetExtensionDeniedWithoutCapability(t *testing.T) {
	h, store := newTestHost(t, CapLabelsWrite) // no extensions:write
	err := h.SetExtension(context.Background(), "tx-1", "budgeting.flag", json.RawMessage(`true`))
	assert.ErrorIs(t, err, ErrCapabilityDenied)

	row, err := store.GetTransaction(context.Background(), "tx-1")
	require.NoError(t, err)
	assert.Equal(t, "{}", row.Extensions, "no extension should be written when the capability is denied")
}

func TestHostSetExtensionWithCapability(t *testing.T) {
	h, store := newTestHost(t, CapExtensionsWrite)
	require.NoError(t, h.SetExtension(context.Background(), "tx-1", "budgeting.flag", json.RawMessage(`true`)))

	row, err := store.GetTransaction(context.Background(), "tx-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"budgeting.flag":true}`, row.Extensions)
	assert.Contains(t, recentEventTypes(t, store), events.TypeExtensionSet)
}

func TestHostSetExtensionRejectsInvalidJSON(t *testing.T) {
	h, _ := newTestHost(t, CapExtensionsWrite)
	err := h.SetExtension(context.Background(), "tx-1", "x.bad", json.RawMessage(`not json`))
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrCapabilityDenied)
}

func TestHostGetTransaction(t *testing.T) {
	h, _ := newTestHost(t, CapTransactionsRead)
	tx, err := h.GetTransaction(context.Background(), "tx-1")
	require.NoError(t, err)
	assert.Equal(t, "tx-1", tx.ID)
	assert.Equal(t, "Coffee", tx.Description)

	denied, _ := newTestHost(t, CapLabelsWrite) // no transactions:read
	_, err = denied.GetTransaction(context.Background(), "tx-1")
	assert.ErrorIs(t, err, ErrCapabilityDenied)
}

func TestHostGetTransactionNotFound(t *testing.T) {
	h, _ := newTestHost(t, CapTransactionsRead)
	_, err := h.GetTransaction(context.Background(), "nope")
	assert.ErrorIs(t, err, ErrTxnNotFound)
}

func TestHostSearch(t *testing.T) {
	h, _ := newTestHost(t, CapTransactionsRead)
	res, err := h.Search(context.Background(), "coffee", 100)
	require.NoError(t, err)
	require.Len(t, res, 1)
	assert.Equal(t, "tx-1", res[0].ID)

	denied, _ := newTestHost(t, CapLabelsWrite)
	_, err = denied.Search(context.Background(), "coffee", 100)
	assert.ErrorIs(t, err, ErrCapabilityDenied)
}

func TestHostSetConfigPersistsAndMerges(t *testing.T) {
	dir := t.TempDir()
	defaults := map[string]any{"keyword": "coffee", "limit": int64(10), "enabled": false}
	h := newHost(nil, nil, capSet{}, "budgeting", 0, testLogger(),
		newConfigStore(dir, "budgeting", defaults), nil)

	merged, err := h.SetConfig(context.Background(), map[string]any{"keyword": "tea", "limit": "25"})
	require.NoError(t, err)
	assert.Equal(t, "tea", merged["keyword"])
	assert.EqualValues(t, 25, merged["limit"])
	assert.Equal(t, false, merged["enabled"], "untouched keys keep their defaults")

	// The override file was overwritten and is the durable source of truth.
	overrides, err := loadUserOverrides(dir, "budgeting")
	require.NoError(t, err)
	assert.Equal(t, "tea", overrides["keyword"])

	// A second call merges with the existing overrides rather than replacing them.
	merged, err = h.SetConfig(context.Background(), map[string]any{"enabled": true})
	require.NoError(t, err)
	assert.Equal(t, "tea", merged["keyword"])
	assert.Equal(t, true, merged["enabled"])
}

func TestHostSetConfigRejectsUnknownKeyWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	h := newHost(nil, nil, capSet{}, "budgeting", 0, testLogger(),
		newConfigStore(dir, "budgeting", map[string]any{"keyword": "coffee"}), nil)

	_, err := h.SetConfig(context.Background(), map[string]any{"nope": "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
	_, statErr := os.Stat(userConfigPath(dir, "budgeting"))
	assert.True(t, errors.Is(statErr, fs.ErrNotExist), "a rejected change must not write the file")
}

func TestHostFetchDeniedWithoutCapability(t *testing.T) {
	h, _ := newTestHost(t, CapTransactionsRead) // no net:fetch
	_, err := h.Fetch(context.Background(), FetchRequest{URL: "https://api.example.com/x"})
	assert.ErrorIs(t, err, ErrCapabilityDenied)
}

func TestHostFetchUnconfiguredWithCapabilityButNoGate(t *testing.T) {
	// Granted net:fetch but no gate built (no [net] block) — a clean error, not a
	// panic or an open socket.
	h := newHost(nil, nil, newCapSet([]Capability{CapNetFetch}), "p", 0, testLogger(), nil, nil)
	_, err := h.Fetch(context.Background(), FetchRequest{URL: "https://api.example.com/x"})
	assert.ErrorIs(t, err, ErrNetUnconfigured)
}

func TestHostSetConfigUnavailableWithoutPluginsDir(t *testing.T) {
	h := newHost(nil, nil, capSet{}, "budgeting", 0, testLogger(), nil, nil)
	_, err := h.SetConfig(context.Background(), map[string]any{"keyword": "tea"})
	assert.Error(t, err)
}
