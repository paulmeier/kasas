package db

import (
	"context"
	"database/sql"
)

// Store is the data layer the rest of the application depends on. Both the
// SQLite and Postgres backends implement it, so handlers and the poller are
// backend-agnostic.
type Store interface {
	Querier
	// RunInTx runs fn inside a transaction, passing a Querier bound to it. The
	// transaction commits if fn returns nil and rolls back otherwise.
	RunInTx(ctx context.Context, fn func(Querier) error) error
	Ping(ctx context.Context) error
	Close() error
}

// SQLiteStore is the SQLite-backed Store (modernc.org/sqlite).
type SQLiteStore struct {
	*Queries
	db *sql.DB
}

// NewSQLiteStore wraps a *sql.DB opened with the "sqlite" driver.
func NewSQLiteStore(database *sql.DB) *SQLiteStore {
	return &SQLiteStore{Queries: New(database), db: database}
}

// RunInTx implements Store.
func (s *SQLiteStore) RunInTx(ctx context.Context, fn func(Querier) error) error {
	return runInTx(ctx, s.db, func(tx *sql.Tx) error { return fn(s.WithTx(tx)) })
}

// Ping implements Store.
func (s *SQLiteStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements Store.
func (s *SQLiteStore) Close() error { return s.db.Close() }

var _ Store = (*SQLiteStore)(nil)

// runInTx begins a transaction, runs fn, and commits or rolls back. It is
// shared by both backends since both use database/sql.
func runInTx(ctx context.Context, database *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
