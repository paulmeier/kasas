// Package testutil provides shared helpers for kasas tests: an isolated,
// migrated SQLite database and a deterministic set of fixtures.
package testutil

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/migrations"
)

// Fixed transaction timestamps (unix seconds) used by Seed, exposed so tests
// can assert date-range filtering.
const (
	Date2024Jan int64 = 1704067200 // 2024-01-01T00:00:00Z
	Date2024Jun int64 = 1717200000 // 2024-06-01T00:00:00Z
	Date2024Dec int64 = 1733011200 // 2024-12-01T00:00:00Z

	syncedAt int64 = 1735000000
)

// Fixtures describes the rows inserted by Seed.
type Fixtures struct {
	OrgID      string
	CheckingID string
	SavingsID  string
	// TxIDs in descending date order across all accounts (matches the API's
	// default ordering: date DESC, id).
	TxIDsByDateDesc []string
}

// NewDB returns an isolated, fully migrated SQLite database. It is closed
// automatically when the test ends.
func NewDB(t testing.TB) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.Up(database, "sqlite"))
	return database
}

// NewStore returns a SQLite-backed db.Store on a fresh migrated database.
func NewStore(t testing.TB) db.Store {
	t.Helper()
	return db.NewSQLiteStore(NewDB(t))
}

// Seed inserts one organization, two accounts, four transactions (one pending),
// and one completed sync_log row. It returns identifiers for assertions. It
// accepts a Querier so it works with either backend or a transaction.
func Seed(t testing.TB, q db.Querier) Fixtures {
	t.Helper()
	ctx := context.Background()

	fx := Fixtures{OrgID: "acme.example", CheckingID: "acct-checking", SavingsID: "acct-savings"}

	require.NoError(t, q.UpsertOrganization(ctx, db.UpsertOrganizationParams{
		ID: fx.OrgID, Domain: "acme.example", Name: "Acme Bank", SfinUrl: "https://sfin.acme.example",
	}))

	require.NoError(t, q.UpsertAccount(ctx, db.UpsertAccountParams{
		ID: fx.CheckingID, OrgID: fx.OrgID, Name: "Checking", Currency: "USD",
		Balance: "1000.00", BalanceDate: Date2024Dec, SyncedAt: syncedAt, Source: "simplefin",
	}))
	require.NoError(t, q.UpsertAccount(ctx, db.UpsertAccountParams{
		ID: fx.SavingsID, OrgID: fx.OrgID, Name: "Savings", Currency: "USD",
		Balance: "5000.00", BalanceDate: Date2024Dec, SyncedAt: syncedAt, Source: "simplefin",
	}))

	txns := []db.InsertTransactionParams{
		{ID: "tx-1", AccountID: fx.CheckingID, Amount: "-12.34", Pending: 0, Date: Date2024Jan, Description: "Coffee", Payee: "Cafe", SyncedAt: syncedAt, Source: "simplefin"},
		{ID: "tx-2", AccountID: fx.CheckingID, Amount: "-56.78", Pending: 1, Date: Date2024Jun, Description: "Books", Payee: "Store", Memo: "gift", SyncedAt: syncedAt, Source: "simplefin"},
		{ID: "tx-3", AccountID: fx.CheckingID, Amount: "100.00", Pending: 0, Date: Date2024Dec, Description: "Deposit", Payee: "Employer", SyncedAt: syncedAt, Source: "simplefin"},
		{ID: "tx-4", AccountID: fx.SavingsID, Amount: "250.00", Pending: 0, Date: Date2024Jun, Description: "Transfer", Payee: "Self", SyncedAt: syncedAt, Source: "simplefin"},
	}
	for _, tx := range txns {
		_, err := q.InsertTransaction(ctx, tx)
		require.NoError(t, err)
	}
	// date DESC, id: tx-3 (Dec), tx-2 (Jun), tx-4 (Jun), tx-1 (Jan)
	fx.TxIDsByDateDesc = []string{"tx-3", "tx-2", "tx-4", "tx-1"}

	logEntry, err := q.CreateSyncLog(ctx, db.CreateSyncLogParams{StartedAt: syncedAt, Status: "running"})
	require.NoError(t, err)
	require.NoError(t, q.CompleteSyncLog(ctx, db.CompleteSyncLogParams{
		CompletedAt: sql.NullInt64{Int64: syncedAt + 5, Valid: true},
		Status:      "success",
		ID:          logEntry.ID,
	}))

	return fx
}
