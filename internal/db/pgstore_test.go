package db_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/migrations"
)

// newPostgresStore connects to the database named by KASAS_TEST_POSTGRES_DSN,
// resets its schema, applies the postgres migrations, and returns a Store. The
// test is skipped when the env var is unset, so the default suite needs no
// database.
func newPostgresStore(t *testing.T) db.Store {
	t.Helper()
	dsn := os.Getenv("KASAS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set KASAS_TEST_POSTGRES_DSN to run Postgres integration tests")
	}

	database, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, database.Ping())
	t.Cleanup(func() { _ = database.Close() })

	// Start each run from a pristine schema.
	_, err = database.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
	require.NoError(t, err)

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("postgres"))
	require.NoError(t, goose.Up(database, "postgres"))

	return db.NewPostgresStore(database)
}

func TestPostgresStore(t *testing.T) {
	store := newPostgresStore(t)
	ctx := context.Background()
	fx := testutil.Seed(t, store)

	t.Run("accounts ordered by name", func(t *testing.T) {
		accts, err := store.ListAccounts(ctx)
		require.NoError(t, err)
		require.Len(t, accts, 2)
		assert.Equal(t, "Checking", accts[0].Name)
		assert.Equal(t, "1000.00", accts[0].Balance)
	})

	t.Run("transactions ordered and date-filtered", func(t *testing.T) {
		all, err := store.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 100})
		require.NoError(t, err)
		assert.Equal(t, fx.TxIDsByDateDesc, txIDs(all))

		page, err := store.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 2, RowOffset: 2})
		require.NoError(t, err)
		assert.Equal(t, []string{"tx-4", "tx-1"}, txIDs(page))

		win, err := store.ListTransactions(ctx, db.ListTransactionsParams{
			Since: testutil.Date2024Jun, Until: testutil.Date2024Jun, RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-2", "tx-4"}, txIDs(win))

		byAcct, err := store.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
			AccountID: fx.CheckingID, RowLimit: 100,
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"tx-3", "tx-2", "tx-1"}, txIDs(byAcct))
	})

	t.Run("pending mapped, nullable sync log fields", func(t *testing.T) {
		pendingTxn, err := store.GetTransaction(ctx, "tx-2")
		require.NoError(t, err)
		assert.Equal(t, int64(1), pendingTxn.Pending)

		latest, err := store.LatestSyncLog(ctx)
		require.NoError(t, err)
		assert.Equal(t, "success", latest.Status)
		assert.True(t, latest.CompletedAt.Valid)
	})

	t.Run("insert is idempotent", func(t *testing.T) {
		p := db.InsertTransactionParams{ID: "tx-3", AccountID: fx.CheckingID, Amount: "1", Date: 1, SyncedAt: 1}
		n, err := store.InsertTransaction(ctx, p)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n, "duplicate id is a no-op")
	})

	t.Run("foreign keys enforced", func(t *testing.T) {
		err := store.UpsertAccount(ctx, db.UpsertAccountParams{
			ID: "x", OrgID: "ghost", Name: "n", Currency: "USD", Balance: "0", BalanceDate: 1, SyncedAt: 1,
		})
		require.Error(t, err)
	})

	t.Run("RunInTx commits on success", func(t *testing.T) {
		require.NoError(t, store.RunInTx(ctx, func(q db.Querier) error {
			return q.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: "tx-org", Name: "Tx Org"})
		}))
		got, err := store.GetOrganization(ctx, "tx-org")
		require.NoError(t, err)
		assert.Equal(t, "Tx Org", got.Name)
	})

	t.Run("RunInTx rolls back on error", func(t *testing.T) {
		sentinel := errors.New("boom")
		err := store.RunInTx(ctx, func(q db.Querier) error {
			require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: "rollback-org", Name: "Nope"}))
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)

		_, err = store.GetOrganization(ctx, "rollback-org")
		require.ErrorIs(t, err, sql.ErrNoRows)
	})

	// Exercises the Postgres JSON label SQL (labels::jsonb ->> / -). This is the
	// only place the pg label queries run against a real Postgres.
	t.Run("labels: default, filter, list, delete", func(t *testing.T) {
		tx4, err := store.GetTransaction(ctx, "tx-4")
		require.NoError(t, err)
		assert.JSONEq(t, "{}", tx4.Labels) // InsertTransaction wrote the '{}' literal

		set := func(id, labels string) {
			n, err := store.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: id, Labels: labels})
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
		}
		set("tx-1", `{"category":"food","tag":"coffee"}`)
		set("tx-2", `{"category":"rent"}`)
		set("tx-3", `{"tag":"coffee"}`)

		byKey, err := store.FilterTransactionsByLabelKey(ctx, db.FilterTransactionsByLabelKeyParams{
			LabelKey: "category", RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-1", "tx-2"}, txIDs(byKey))

		byValue, err := store.FilterTransactionsByLabelValue(ctx, db.FilterTransactionsByLabelValueParams{
			LabelKey: "tag", LabelValue: "coffee", RowLimit: 100,
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tx-1", "tx-3"}, txIDs(byValue))

		labeled, err := store.ListLabeledTransactions(ctx)
		require.NoError(t, err)
		ids := make([]string, len(labeled))
		for i, r := range labeled {
			ids[i] = r.ID
		}
		assert.ElementsMatch(t, []string{"tx-1", "tx-2", "tx-3"}, ids)

		// Delete by value drops only the matching key (one value per key).
		n, err := store.DeleteLabelByValue(ctx, db.DeleteLabelByValueParams{LabelKey: "tag", LabelValue: "coffee"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), n)
		tx1, err := store.GetTransaction(ctx, "tx-1")
		require.NoError(t, err)
		assert.JSONEq(t, `{"category":"food"}`, tx1.Labels)

		// Delete by key removes it everywhere.
		n, err = store.DeleteLabelByKey(ctx, "category")
		require.NoError(t, err)
		assert.Equal(t, int64(2), n)
		tx1, err = store.GetTransaction(ctx, "tx-1")
		require.NoError(t, err)
		assert.JSONEq(t, `{}`, tx1.Labels)
	})

	// Exercises the Postgres rules adapter (whole-struct casts, RETURNING, the
	// identity id, and the enabled filter) against a real Postgres.
	t.Run("rules: CRUD and enabled filter", func(t *testing.T) {
		created, err := store.CreateRule(ctx, db.CreateRuleParams{
			Name: "Coffee", Query: "description:coffee", Labels: `{"category":"coffee"}`,
			Enabled: 1, CreatedAt: 10, UpdatedAt: 10,
		})
		require.NoError(t, err)
		require.NotZero(t, created.ID, "identity id is returned")
		assert.Equal(t, "Coffee", created.Name)
		assert.Equal(t, int64(1), created.Enabled)

		disabled, err := store.CreateRule(ctx, db.CreateRuleParams{
			Query: "amount:>0", Labels: `{"flow":"in"}`, Enabled: 0, CreatedAt: 11, UpdatedAt: 11,
		})
		require.NoError(t, err)

		got, err := store.GetRule(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "description:coffee", got.Query)

		all, err := store.ListRules(ctx)
		require.NoError(t, err)
		assert.Len(t, all, 2)

		enabled, err := store.ListEnabledRules(ctx)
		require.NoError(t, err)
		require.Len(t, enabled, 1)
		assert.Equal(t, created.ID, enabled[0].ID)

		n, err := store.UpdateRule(ctx, db.UpdateRuleParams{
			ID: created.ID, Name: "Coffee shops", Query: "payee:cafe",
			Labels: `{"category":"coffee"}`, Enabled: 0, UpdatedAt: 12,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		updated, err := store.GetRule(ctx, created.ID)
		require.NoError(t, err)
		assert.Equal(t, "payee:cafe", updated.Query)
		assert.Equal(t, int64(0), updated.Enabled)

		// Both rules are now disabled.
		enabled, err = store.ListEnabledRules(ctx)
		require.NoError(t, err)
		assert.Empty(t, enabled)

		n, err = store.DeleteRule(ctx, disabled.ID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
		_, err = store.GetRule(ctx, disabled.ID)
		require.ErrorIs(t, err, sql.ErrNoRows)

		// Deleting an unknown id affects no rows.
		n, err = store.DeleteRule(ctx, 999999)
		require.NoError(t, err)
		assert.Equal(t, int64(0), n)
	})
}
