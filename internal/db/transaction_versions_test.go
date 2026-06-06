package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func insertVersion(t *testing.T, store db.Store, txnID, kind string, occurredAt int64) db.TransactionVersion {
	t.Helper()
	v, err := store.InsertTransactionVersion(context.Background(), db.InsertTransactionVersionParams{
		TransactionID: txnID,
		ChangeKind:    kind,
		OccurredAt:    occurredAt,
		Data:          `{"id":"` + txnID + `"}`,
	})
	require.NoError(t, err)
	return v
}

func TestInsertTransactionVersionAssignsSequence(t *testing.T) {
	store := testutil.NewStore(t)
	a := insertVersion(t, store, "tx-1", "imported", 1000)
	b := insertVersion(t, store, "tx-1", "labeled", 1001)
	assert.Equal(t, int64(1), a.ID)
	assert.Equal(t, a.ID+1, b.ID, "id is monotonic")
	assert.Equal(t, "imported", a.ChangeKind)
}

func TestListTransactionVersionsOrderedByID(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	insertVersion(t, store, "tx-1", "imported", 1000)
	insertVersion(t, store, "tx-2", "imported", 1001) // a different transaction
	insertVersion(t, store, "tx-1", "labeled", 1002)
	insertVersion(t, store, "tx-1", "synced", 1003)

	rows, err := store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	require.Len(t, rows, 3, "only tx-1's versions")
	assert.Equal(t, "imported", rows[0].ChangeKind)
	assert.Equal(t, "labeled", rows[1].ChangeKind)
	assert.Equal(t, "synced", rows[2].ChangeKind)
	assert.Less(t, rows[0].ID, rows[1].ID, "ascending insert order")
	assert.Less(t, rows[1].ID, rows[2].ID)
}

func TestCountTransactionVersions(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()

	n, err := store.CountTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "no versions yet")

	insertVersion(t, store, "tx-1", "imported", 1000)
	insertVersion(t, store, "tx-1", "labeled", 1001)
	insertVersion(t, store, "tx-2", "imported", 1002)

	n, err = store.CountTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}

func TestDeleteTransactionVersionsBefore(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	insertVersion(t, store, "tx-1", "imported", 1000)
	insertVersion(t, store, "tx-1", "labeled", 5000)

	n, err := store.DeleteTransactionVersionsBefore(ctx, 2000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the older version is pruned")

	remaining, err := store.ListTransactionVersions(ctx, "tx-1")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "labeled", remaining[0].ChangeKind, "the newest survives")
}
