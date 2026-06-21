package ledger

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

// TestPurgeSourceRemovesOnlyMatchingRows verifies that PurgeSource deletes every
// account + transaction stamped with the target source (the uninstall path for a
// source:provide plugin, ADR 0005), leaves other sources untouched, and emits a
// deletion event for each removed row.
func TestPurgeSourceRemovesOnlyMatchingRows(t *testing.T) {
	ctx := context.Background()
	store := testutil.NewStore(t)

	// simplefin fixtures (must survive the purge).
	fx := testutil.Seed(t, store)

	// Plugin source rows: one account, two transactions, stamped plugin:demo.
	const src = "plugin:demo"
	require.NoError(t, store.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: src, Name: "Demo"}))
	require.NoError(t, store.UpsertAccount(ctx, db.UpsertAccountParams{
		ID: "plugin:demo:acct-1", OrgID: src, Name: "Demo Card", Currency: "USD",
		Balance: "0.00", BalanceDate: 1700000000, SyncedAt: 1700000000, Source: src,
	}))
	for _, id := range []string{"plugin:demo:tx-1", "plugin:demo:tx-2"} {
		_, err := store.InsertTransaction(ctx, db.InsertTransactionParams{
			ID: id, AccountID: "plugin:demo:acct-1", Amount: "-1.00", Date: 1700000000,
			Description: "x", SyncedAt: 1700000000, Source: src,
		})
		require.NoError(t, err)
	}

	emitter := events.NewEmitter(events.NewBus())
	res, err := PurgeSource(ctx, store, emitter, src)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Accounts)
	assert.Equal(t, 2, res.Transactions)

	// The plugin rows are gone.
	_, err = store.GetAccount(ctx, "plugin:demo:acct-1")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	_, err = store.GetTransaction(ctx, "plugin:demo:tx-1")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// The simplefin rows are untouched.
	_, err = store.GetAccount(ctx, fx.CheckingID)
	require.NoError(t, err)
	_, err = store.GetTransaction(ctx, "tx-1")
	require.NoError(t, err)

	// A deletion event was emitted for each removed row (2 txn + 1 account).
	evs, err := store.ListRecentEvents(ctx, 50)
	require.NoError(t, err)
	var txnDeleted, acctDeleted int
	for _, e := range evs {
		switch e.EventType {
		case events.TypeTransactionDeleted:
			txnDeleted++
		case events.TypeAccountDeleted:
			acctDeleted++
		}
	}
	assert.Equal(t, 2, txnDeleted, "one transaction.deleted per purged transaction")
	assert.Equal(t, 1, acctDeleted, "one account.deleted for the purged account")
}
