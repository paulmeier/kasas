// Package dbmigrate moves a kasas ledger from SQLite to Postgres. It is the
// one-time data-copy behind the `kasas migrate-postgres` command and the
// dashboard's "Migrate to Postgres" action.
//
// The copy is faithful and self-contained: it opens the target Postgres
// database, applies the same embedded schema migrations kasas uses at startup,
// refuses to touch a database that already holds kasas data, then copies every
// row of every table — preserving primary keys — inside a single transaction
// and advances the identity sequences so future inserts don't collide. The
// SQLite source is only ever read.
//
// It works at the database/sql level rather than through the db.Store layer on
// purpose: the Store's insert methods generate new identity ids, which would
// break the id-preserving copy the event stream and transaction history rely on.
// Every kasas column is text or bigint (the Postgres schema deliberately mirrors
// SQLite's — see the migrations/postgres comments), so values scanned from
// SQLite pass straight into Postgres with only a []byte→string normalization.
package dbmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"

	"github.com/paulmeier/kasas/migrations"
)

// tables lists every data table to copy, parents before children so the foreign
// keys (accounts→organizations, transactions→accounts, market_points→market_series)
// are satisfied as the copy proceeds. goose_db_version is intentionally excluded:
// the destination gets its own migration history when we apply the schema.
var tables = []string{
	"organizations",
	"accounts",
	"transactions",
	"sync_log",
	"rules",
	"events",
	"transaction_versions",
	"api_keys",
	"webhooks",
	"plugins",
	"settings",
	"market_series",
	"market_points",
}

// identityTables are the tables whose Postgres id is GENERATED ALWAYS AS
// IDENTITY. Copying their rows needs OVERRIDING SYSTEM VALUE (an explicit value
// into a GENERATED ALWAYS column is otherwise rejected), and after the copy
// their identity sequence is advanced past the highest copied id so the next
// insert continues the sequence instead of colliding.
var identityTables = map[string]bool{
	"sync_log":             true,
	"rules":                true,
	"events":               true,
	"transaction_versions": true,
	"api_keys":             true,
	"webhooks":             true,
	"plugins":              true,
}

// TableResult is the number of rows copied for one table.
type TableResult struct {
	Table string `json:"table"`
	Rows  int64  `json:"rows"`
}

// Report summarizes a completed migration.
type Report struct {
	Tables []TableResult `json:"tables"`
	Total  int64         `json:"total_rows"`
}

// MigrateToPostgres copies every row from the SQLite database src into a fresh
// Postgres database at dsn. It opens dsn, applies the kasas schema migrations,
// refuses to proceed if the destination already holds kasas data, then copies
// all tables (preserving ids) and advances the identity sequences. src is only
// read; on any error the destination's data is left untouched (the copy runs in
// one transaction that rolls back).
func MigrateToPostgres(ctx context.Context, src *sql.DB, dsn string, logger *slog.Logger) (*Report, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dst, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	defer dst.Close()
	if err := dst.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return copyAll(ctx, src, dst, logger)
}

// copyAll applies the schema to dst, verifies it is empty, and copies src into
// it. It is separated from MigrateToPostgres so tests can drive it with an
// already-open destination.
func copyAll(ctx context.Context, src, dst *sql.DB, logger *slog.Logger) (*Report, error) {
	if err := applyMigrations(ctx, dst); err != nil {
		return nil, fmt.Errorf("apply postgres schema: %w", err)
	}
	if err := ensureEmpty(ctx, dst); err != nil {
		return nil, err
	}

	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin postgres transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	report := &Report{}
	for _, table := range tables {
		n, err := copyTable(ctx, src, tx, table)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", table, err)
		}
		report.Tables = append(report.Tables, TableResult{Table: table, Rows: n})
		report.Total += n
		if n > 0 {
			logger.Info("migrated table", "table", table, "rows", n)
		}
	}

	// Advance each identity sequence past the highest copied id, inside the same
	// transaction, so the first insert after the migration continues the sequence.
	for _, table := range tables {
		if !identityTables[table] {
			continue
		}
		if err := resetIdentity(ctx, tx, table); err != nil {
			return nil, fmt.Errorf("reset %s id sequence: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit postgres transaction: %w", err)
	}
	logger.Info("migration complete", "tables", len(report.Tables), "rows", report.Total)
	return report, nil
}

// applyMigrations brings the destination Postgres database up to the current
// kasas schema using the same embedded goose migrations as startup.
func applyMigrations(ctx context.Context, dst *sql.DB) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, dst, "postgres")
}

// ensureEmpty refuses to migrate into a database that already carries kasas data,
// which would otherwise fail on primary-key conflicts partway through (or, worse,
// silently interleave two ledgers). A pristine database — freshly created, or one
// where kasas only ever created its empty schema — passes.
func ensureEmpty(ctx context.Context, dst *sql.DB) error {
	for _, table := range tables {
		var hasRow bool
		q := fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s)", quoteIdent(table))
		if err := dst.QueryRowContext(ctx, q).Scan(&hasRow); err != nil {
			return fmt.Errorf("check %s is empty: %w", table, err)
		}
		if hasRow {
			return fmt.Errorf("destination table %q already contains data — migrate into an empty Postgres database", table)
		}
	}
	return nil
}

// copyTable streams every row of one table from SQLite into the Postgres
// transaction, preserving all column values (ids included). Columns are read
// dynamically from the source result set, so a column added by a future
// migration is copied without changing this code, as long as both dialects have
// it (they always do — the schema is dialect-paired).
func copyTable(ctx context.Context, src *sql.DB, tx *sql.Tx, table string) (int64, error) {
	rows, err := src.QueryContext(ctx, "SELECT * FROM "+quoteIdent(table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return 0, err
	}

	stmt, err := tx.PrepareContext(ctx, insertStmt(table, cols))
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	var n int64
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		// modernc/sqlite may hand back TEXT columns as []byte; the pgx driver would
		// route []byte to a bytea parameter and reject it on a text column, so make
		// it a string. Every kasas column is text or bigint, so this is always safe.
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return n, err
		}
		n++
	}
	return n, rows.Err()
}

// insertStmt builds the parameterized INSERT for a table's columns. Identity
// tables get OVERRIDING SYSTEM VALUE so the source's ids can be written into the
// GENERATED ALWAYS AS IDENTITY column.
func insertStmt(table string, cols []string) string {
	quoted := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}
	overriding := ""
	if identityTables[table] {
		overriding = " OVERRIDING SYSTEM VALUE"
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s)%s VALUES (%s)",
		quoteIdent(table), strings.Join(quoted, ", "), overriding, strings.Join(placeholders, ", "),
	)
}

// resetIdentity advances a table's identity sequence so the next generated id is
// one past the highest copied id (or leaves it at the start when the table was
// empty). setval's third argument (is_called) is true only when rows exist, so an
// empty table keeps nextval == 1.
func resetIdentity(ctx context.Context, tx *sql.Tx, table string) error {
	q := fmt.Sprintf(
		`SELECT setval(
			pg_get_serial_sequence('%s', 'id'),
			(SELECT COALESCE(MAX(id), 1) FROM %s),
			(SELECT COUNT(*) > 0 FROM %s)
		)`,
		table, quoteIdent(table), quoteIdent(table),
	)
	_, err := tx.ExecContext(ctx, q)
	return err
}

// quoteIdent double-quotes a SQL identifier (doubling any embedded quote). The
// table and column names here are fixed schema identifiers, but quoting keeps
// the generated SQL correct and defends against a future name that needs it.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
