package dbmigrate

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func TestInsertStmt(t *testing.T) {
	// A plain (non-identity) table: explicit columns, positional placeholders, no
	// OVERRIDING clause.
	assert.Equal(t,
		`INSERT INTO "organizations" ("id", "name") VALUES ($1, $2)`,
		insertStmt("organizations", []string{"id", "name"}),
	)
	// An identity table gets OVERRIDING SYSTEM VALUE so the source id can be
	// written into the GENERATED ALWAYS AS IDENTITY column.
	assert.Equal(t,
		`INSERT INTO "events" ("id", "event_id") OVERRIDING SYSTEM VALUE VALUES ($1, $2)`,
		insertStmt("events", []string{"id", "event_id"}),
	)
}

func TestQuoteIdent(t *testing.T) {
	assert.Equal(t, `"plain"`, quoteIdent("plain"))
	assert.Equal(t, `"we""ird"`, quoteIdent(`we"ird`))
}

// TestMigrateToPostgres exercises the full copy against a real Postgres, gated on
// KASAS_TEST_POSTGRES_DSN (like the pgstore integration test). It seeds a SQLite
// ledger — including identity-keyed tables — migrates it, and verifies the row
// counts, that ids are preserved, and that identity sequences continue past the
// highest copied id.
func TestMigrateToPostgres(t *testing.T) {
	dsn := os.Getenv("KASAS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set KASAS_TEST_POSTGRES_DSN to run Postgres integration tests")
	}
	ctx := context.Background()

	// Start the destination from a pristine schema; MigrateToPostgres applies the
	// migrations itself. This connection is reused afterward to assert the result.
	dst, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, dst.Ping())
	t.Cleanup(func() { _ = dst.Close() })
	_, err = dst.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
	require.NoError(t, err)

	// Build a SQLite source with the base fixtures plus identity-keyed rows.
	srcDB := testutil.NewDB(t)
	src := db.NewSQLiteStore(srcDB)
	fx := testutil.Seed(t, src) // orgs, accounts, 4 transactions, 1 sync_log

	rule, err := src.CreateRule(ctx, db.CreateRuleParams{
		Name: "Coffee", Query: "description:coffee", Labels: `{"category":"coffee"}`,
		Extensions: "{}", Enabled: 1, CreatedAt: 10, UpdatedAt: 10,
	})
	require.NoError(t, err)

	for i, et := range []string{"transaction.created", "label.applied", "account.created"} {
		_, err := src.InsertEvent(ctx, db.InsertEventParams{
			EventID: et, EventType: et, EntityType: "transaction", EntityID: "tx-1", OccurredAt: int64(1000 + i), Data: "{}",
		})
		require.NoError(t, err)
	}

	ver, err := src.InsertTransactionVersion(ctx, db.InsertTransactionVersionParams{
		TransactionID: "tx-1", ChangeKind: "imported", OccurredAt: 1000, Data: `{"id":"tx-1"}`,
	})
	require.NoError(t, err)

	require.NoError(t, src.UpsertSetting(ctx, db.UpsertSettingParams{Key: "sync.interval", Value: "12h", UpdatedAt: 1}))

	// Migrate.
	report, err := MigrateToPostgres(ctx, srcDB, dsn, nil)
	require.NoError(t, err)

	counts := map[string]int64{}
	for _, tr := range report.Tables {
		counts[tr.Table] = tr.Rows
	}
	assert.Equal(t, int64(1), counts["organizations"])
	assert.Equal(t, int64(2), counts["accounts"])
	assert.Equal(t, int64(4), counts["transactions"])
	assert.Equal(t, int64(1), counts["sync_log"])
	assert.Equal(t, int64(1), counts["rules"])
	assert.Equal(t, int64(3), counts["events"])
	assert.Equal(t, int64(1), counts["transaction_versions"])
	assert.Equal(t, int64(1), counts["settings"])
	assert.Equal(t, int64(0), counts["webhooks"])

	// Verify the copied data on Postgres.
	pg := db.NewPostgresStore(dst)

	accts, err := pg.ListAccounts(ctx)
	require.NoError(t, err)
	assert.Len(t, accts, 2)

	txns, err := pg.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 100})
	require.NoError(t, err)
	assert.Equal(t, fx.TxIDsByDateDesc, txIDs(txns))

	// Ids are preserved (not regenerated): the rule keeps its source id.
	gotRule, err := pg.GetRule(ctx, rule.ID)
	require.NoError(t, err)
	assert.Equal(t, "Coffee", gotRule.Name)

	// The identity sequence continues past the highest copied id: the next rule
	// gets an id greater than the migrated one (no PK collision, no reuse).
	next, err := pg.CreateRule(ctx, db.CreateRuleParams{
		Query: "amount:>0", Labels: "{}", Extensions: "{}", Enabled: 1, CreatedAt: 20, UpdatedAt: 20,
	})
	require.NoError(t, err)
	assert.Greater(t, next.ID, rule.ID)

	// Same for the events sequence.
	ev, err := pg.InsertEvent(ctx, db.InsertEventParams{
		EventID: "post-migrate", EventType: "rule.created", EntityType: "rule", EntityID: "x", OccurredAt: 2000, Data: "{}",
	})
	require.NoError(t, err)
	assert.Greater(t, ev.ID, int64(0))

	// And the transaction_versions sequence.
	nextVer, err := pg.InsertTransactionVersion(ctx, db.InsertTransactionVersionParams{
		TransactionID: "tx-1", ChangeKind: "synced", OccurredAt: 1001, Data: "{}",
	})
	require.NoError(t, err)
	assert.Greater(t, nextVer.ID, ver.ID)
}

// TestMigrateToPostgresRefusesNonEmpty verifies the guard: migrating into a
// database that already holds kasas data fails instead of colliding.
func TestMigrateToPostgresRefusesNonEmpty(t *testing.T) {
	dsn := os.Getenv("KASAS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set KASAS_TEST_POSTGRES_DSN to run Postgres integration tests")
	}
	ctx := context.Background()

	dst, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, dst.Ping())
	t.Cleanup(func() { _ = dst.Close() })
	_, err = dst.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
	require.NoError(t, err)

	srcDB := testutil.NewDB(t)
	testutil.Seed(t, db.NewSQLiteStore(srcDB))

	// First migration succeeds.
	_, err = MigrateToPostgres(ctx, srcDB, dsn, nil)
	require.NoError(t, err)

	// A second one refuses, because the destination is no longer empty.
	_, err = MigrateToPostgres(ctx, srcDB, dsn, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already contains data")
}

func txIDs(txns []db.Transaction) []string {
	ids := make([]string, len(txns))
	for i, t := range txns {
		ids[i] = t.ID
	}
	return ids
}
