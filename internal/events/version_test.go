package events

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func vsnap(id, payee string, labels map[string]string) TransactionPayload {
	return TransactionPayload{ID: id, AccountID: "acct-1", Amount: "-5.00", Payee: payee, Labels: labels}
}

func TestRecorderVersionPersistsButDoesNotPublish(t *testing.T) {
	store := testutil.NewStore(t)
	bus := NewBus()
	defer bus.Close()
	sub, cancel := bus.Subscribe()
	defer cancel()
	em := NewEmitter(bus)
	ctx := context.Background()

	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		return rec.Version(ctx, q, "tx-1", vsnap("tx-1", "Coffee", nil), ChangeImported)
	})
	require.NoError(t, err)

	rows, err := store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, ChangeImported, rows[0].ChangeKind)
	assert.NotZero(t, rows[0].ID)
	assert.Contains(t, rows[0].Data, "Coffee")

	// Versions are a durable record, not a live stream: nothing hits the bus.
	select {
	case ev := <-sub:
		t.Fatalf("a version must not be published to the bus, got %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestVersionNilEmitterIsNoOp(t *testing.T) {
	store := testutil.NewStore(t)
	var em *Emitter // nil == events/history disabled
	ctx := context.Background()

	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		if e := rec.Version(ctx, q, "tx-1", vsnap("tx-1", "Coffee", nil), ChangeImported); e != nil {
			return e
		}
		return rec.VersionChange(ctx, q, "tx-1", vsnap("tx-1", "A", nil), vsnap("tx-1", "B", nil), ChangeSynced)
	})
	require.NoError(t, err)

	rows, err := store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	assert.Empty(t, rows, "a nil emitter records no versions")
}

func TestVersionChangeSynthesizesBaselineOnce(t *testing.T) {
	store := testutil.NewStore(t)
	bus := NewBus()
	defer bus.Close()
	em := NewEmitter(bus)
	ctx := context.Background()

	// First change to a transaction with no history: writes a synthesized imported
	// baseline (the prior state) plus the change.
	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		return rec.VersionChange(ctx, q, "tx-1",
			vsnap("tx-1", "Old Merchant", nil),
			vsnap("tx-1", "New Merchant", nil),
			ChangeSynced)
	})
	require.NoError(t, err)

	rows, err := store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	require.Len(t, rows, 2, "baseline + change")
	assert.Equal(t, ChangeImported, rows[0].ChangeKind)
	assert.Contains(t, rows[0].Data, "Old Merchant")
	assert.Equal(t, ChangeSynced, rows[1].ChangeKind)
	assert.Contains(t, rows[1].Data, "New Merchant")

	// A second change does NOT synthesize another baseline.
	err = em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		return rec.VersionChange(ctx, q, "tx-1",
			vsnap("tx-1", "New Merchant", nil),
			vsnap("tx-1", "New Merchant", map[string]string{"category": "food"}),
			ChangeLabeled)
	})
	require.NoError(t, err)

	rows, err = store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	require.Len(t, rows, 3, "only the change is appended")
	assert.Equal(t, ChangeLabeled, rows[2].ChangeKind)
}
