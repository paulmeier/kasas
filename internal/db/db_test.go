package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func TestUpsertOrganization(t *testing.T) {
	q := db.New(testutil.NewDB(t))
	ctx := context.Background()

	require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{
		ID: "org1", Domain: "a.com", Name: "Old Name", SfinUrl: "https://a",
	}))
	// Upserting the same id updates in place.
	require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{
		ID: "org1", Domain: "a.com", Name: "New Name", SfinUrl: "https://a",
	}))

	got, err := q.GetOrganization(ctx, "org1")
	require.NoError(t, err)
	assert.Equal(t, "New Name", got.Name)

	all, err := q.ListOrganizations(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func TestUpsertAccountUpdatesBalance(t *testing.T) {
	database := testutil.NewDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: "org1"}))

	require.NoError(t, q.UpsertAccount(ctx, db.UpsertAccountParams{
		ID: "acct1", OrgID: "org1", Name: "Checking", Currency: "USD", Balance: "10.00", BalanceDate: 1, SyncedAt: 1,
	}))
	require.NoError(t, q.UpsertAccount(ctx, db.UpsertAccountParams{
		ID: "acct1", OrgID: "org1", Name: "Checking", Currency: "USD", Balance: "20.00", BalanceDate: 2, SyncedAt: 2,
	}))

	got, err := q.GetAccount(ctx, "acct1")
	require.NoError(t, err)
	assert.Equal(t, "20.00", got.Balance)
	assert.Equal(t, int64(2), got.BalanceDate)
}

func TestInsertTransactionIsIdempotent(t *testing.T) {
	database := testutil.NewDB(t)
	q := db.New(database)
	ctx := context.Background()
	require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: "org1"}))
	require.NoError(t, q.UpsertAccount(ctx, db.UpsertAccountParams{ID: "acct1", OrgID: "org1", Name: "C", Currency: "USD", Balance: "0", BalanceDate: 1, SyncedAt: 1}))

	params := db.InsertTransactionParams{ID: "tx1", AccountID: "acct1", Amount: "-1.00", Date: 100, SyncedAt: 1}

	rows, err := q.InsertTransaction(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, int64(1), rows, "first insert affects one row")

	rows, err = q.InsertTransaction(ctx, params)
	require.NoError(t, err)
	assert.Equal(t, int64(0), rows, "duplicate insert is a no-op (ON CONFLICT DO NOTHING)")

	count, err := q.CountTransactions(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestForeignKeysEnforced(t *testing.T) {
	q := db.New(testutil.NewDB(t))
	// Inserting an account for a non-existent organization must fail.
	err := q.UpsertAccount(context.Background(), db.UpsertAccountParams{
		ID: "acct1", OrgID: "ghost", Name: "C", Currency: "USD", Balance: "0", BalanceDate: 1, SyncedAt: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "FOREIGN KEY")
}

func TestListTransactionsOrderingAndFilters(t *testing.T) {
	database := testutil.NewDB(t)
	q := db.New(database)
	fx := testutil.Seed(t, q)
	ctx := context.Background()

	t.Run("all, ordered date desc then id", func(t *testing.T) {
		got, err := q.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 100})
		require.NoError(t, err)
		ids := txIDs(got)
		assert.Equal(t, fx.TxIDsByDateDesc, ids)
	})

	t.Run("limit and offset", func(t *testing.T) {
		page1, err := q.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 2, RowOffset: 0})
		require.NoError(t, err)
		require.Len(t, page1, 2)
		assert.Equal(t, []string{"tx-3", "tx-2"}, txIDs(page1))

		page2, err := q.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 2, RowOffset: 2})
		require.NoError(t, err)
		assert.Equal(t, []string{"tx-4", "tx-1"}, txIDs(page2))
	})

	t.Run("since filter", func(t *testing.T) {
		got, err := q.ListTransactions(ctx, db.ListTransactionsParams{Since: testutil.Date2024Jun, RowLimit: 100})
		require.NoError(t, err)
		assert.Equal(t, []string{"tx-3", "tx-2", "tx-4"}, txIDs(got))
	})

	t.Run("since and until window", func(t *testing.T) {
		got, err := q.ListTransactions(ctx, db.ListTransactionsParams{
			Since: testutil.Date2024Jun, Until: testutil.Date2024Jun, RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-2", "tx-4"}, txIDs(got))
	})

	t.Run("by account", func(t *testing.T) {
		got, err := q.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
			AccountID: fx.CheckingID, RowLimit: 100,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"tx-3", "tx-2", "tx-1"}, txIDs(got))
	})
}

func TestSyncLogLifecycle(t *testing.T) {
	q := db.New(testutil.NewDB(t))
	ctx := context.Background()

	entry, err := q.CreateSyncLog(ctx, db.CreateSyncLogParams{StartedAt: 1000, Status: "running"})
	require.NoError(t, err)
	assert.Equal(t, "running", entry.Status)
	assert.False(t, entry.CompletedAt.Valid)
	assert.False(t, entry.Error.Valid)

	require.NoError(t, q.CompleteSyncLog(ctx, db.CompleteSyncLogParams{
		CompletedAt: sql.NullInt64{Int64: 1005, Valid: true},
		Status:      "error",
		Error:       sql.NullString{String: "boom", Valid: true},
		ID:          entry.ID,
	}))

	latest, err := q.LatestSyncLog(ctx)
	require.NoError(t, err)
	assert.Equal(t, entry.ID, latest.ID)
	assert.Equal(t, "error", latest.Status)
	assert.True(t, latest.CompletedAt.Valid)
	assert.Equal(t, int64(1005), latest.CompletedAt.Int64)
	assert.Equal(t, "boom", latest.Error.String)

	logs, err := q.ListSyncLogs(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, logs, 1)
}

func TestLatestSyncLogEmpty(t *testing.T) {
	q := db.New(testutil.NewDB(t))
	_, err := q.LatestSyncLog(context.Background())
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestLabelQueries exercises the SQLite JSON label SQL: the '{}' insert default,
// per-key/value filtering, the labeled-rows listing, and per-key/value deletion.
func TestLabelQueries(t *testing.T) {
	q := db.New(testutil.NewDB(t))
	fx := testutil.Seed(t, q)
	ctx := context.Background()

	// Freshly inserted transactions default to an empty object: InsertTransaction
	// writes the literal '{}', not the column's legacy '[]' default.
	tx4, err := q.GetTransaction(ctx, "tx-4")
	require.NoError(t, err)
	assert.Equal(t, "{}", tx4.Labels)

	set := func(id, labels string) {
		t.Helper()
		n, err := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: id, Labels: labels})
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
	}
	set("tx-1", `{"category":"food","tag":"coffee"}`)
	set("tx-2", `{"category":"rent"}`)
	set("tx-3", `{"tag":"coffee"}`)

	t.Run("filter by key present", func(t *testing.T) {
		got, err := q.FilterTransactionsByLabelKey(ctx, db.FilterTransactionsByLabelKeyParams{
			LabelKey: "category", RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-1", "tx-2"}, txIDs(got))
	})

	t.Run("filter by key and value", func(t *testing.T) {
		got, err := q.FilterTransactionsByLabelValue(ctx, db.FilterTransactionsByLabelValueParams{
			LabelKey: "tag", LabelValue: "coffee", RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-1", "tx-3"}, txIDs(got))
	})

	t.Run("filter composes with account", func(t *testing.T) {
		got, err := q.FilterTransactionsByLabelKey(ctx, db.FilterTransactionsByLabelKeyParams{
			LabelKey: "tag", AccountID: fx.CheckingID, RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-1", "tx-3"}, txIDs(got))
	})

	t.Run("ListLabeledTransactions skips empty", func(t *testing.T) {
		rows, err := q.ListLabeledTransactions(ctx)
		require.NoError(t, err)
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
		}
		assert.ElementsMatch(t, []string{"tx-1", "tx-2", "tx-3"}, ids)
	})

	t.Run("delete by value drops only the matching key", func(t *testing.T) {
		n, err := q.DeleteLabelByValue(ctx, db.DeleteLabelByValueParams{LabelKey: "tag", LabelValue: "coffee"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), n) // tx-1 and tx-3

		tx1, err := q.GetTransaction(ctx, "tx-1")
		require.NoError(t, err)
		assert.Equal(t, map[string]string{"category": "food"}, decodeJSON(t, tx1.Labels)) // tag removed, category kept

		tx3, err := q.GetTransaction(ctx, "tx-3")
		require.NoError(t, err)
		assert.Empty(t, decodeJSON(t, tx3.Labels))
	})

	t.Run("delete by key removes the key everywhere", func(t *testing.T) {
		n, err := q.DeleteLabelByKey(ctx, "category")
		require.NoError(t, err)
		assert.Equal(t, int64(2), n) // tx-1 and tx-2

		tx1, err := q.GetTransaction(ctx, "tx-1")
		require.NoError(t, err)
		assert.Empty(t, decodeJSON(t, tx1.Labels))
	})
}

func decodeJSON(t *testing.T, s string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, json.Unmarshal([]byte(s), &out))
	return out
}

func txIDs(txns []db.Transaction) []string {
	ids := make([]string, len(txns))
	for i, tx := range txns {
		ids[i] = tx.ID
	}
	return ids
}
