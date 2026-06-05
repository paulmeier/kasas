package db

import (
	"context"
	"database/sql"

	"github.com/paulmeier/kasas/internal/db/pg"
)

// PostgresStore is the Postgres-backed Store. It adapts the pg-generated
// queries to the canonical db types, which are structurally identical to the
// pg ones (enforced by the conversions below failing to compile if they drift).
type PostgresStore struct {
	pgQuerier
	db *sql.DB
}

// NewPostgresStore wraps a *sql.DB opened with the "pgx" driver.
func NewPostgresStore(database *sql.DB) *PostgresStore {
	return &PostgresStore{pgQuerier: pgQuerier{q: pg.New(database)}, db: database}
}

// RunInTx implements Store.
func (s *PostgresStore) RunInTx(ctx context.Context, fn func(Querier) error) error {
	return runInTx(ctx, s.db, func(tx *sql.Tx) error {
		return fn(pgQuerier{q: s.q.WithTx(tx)})
	})
}

// Ping implements Store.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements Store.
func (s *PostgresStore) Close() error { return s.db.Close() }

var _ Store = (*PostgresStore)(nil)

// pgQuerier implements Querier over the pg-generated queries.
type pgQuerier struct {
	q *pg.Queries
}

var _ Querier = pgQuerier{}

func (a pgQuerier) CompleteSyncLog(ctx context.Context, arg CompleteSyncLogParams) error {
	return a.q.CompleteSyncLog(ctx, pg.CompleteSyncLogParams(arg))
}

func (a pgQuerier) CountTransactions(ctx context.Context) (int64, error) {
	return a.q.CountTransactions(ctx)
}

func (a pgQuerier) CreateSyncLog(ctx context.Context, arg CreateSyncLogParams) (SyncLog, error) {
	row, err := a.q.CreateSyncLog(ctx, pg.CreateSyncLogParams(arg))
	return SyncLog(row), err
}

func (a pgQuerier) GetAccount(ctx context.Context, id string) (Account, error) {
	row, err := a.q.GetAccount(ctx, id)
	return Account(row), err
}

func (a pgQuerier) GetOrganization(ctx context.Context, id string) (Organization, error) {
	row, err := a.q.GetOrganization(ctx, id)
	return Organization(row), err
}

func (a pgQuerier) GetTransaction(ctx context.Context, id string) (Transaction, error) {
	row, err := a.q.GetTransaction(ctx, id)
	return Transaction(row), err
}

func (a pgQuerier) InsertTransaction(ctx context.Context, arg InsertTransactionParams) (int64, error) {
	return a.q.InsertTransaction(ctx, pg.InsertTransactionParams(arg))
}

func (a pgQuerier) UpdateTransactionTags(ctx context.Context, arg UpdateTransactionTagsParams) (int64, error) {
	return a.q.UpdateTransactionTags(ctx, pg.UpdateTransactionTagsParams(arg))
}

func (a pgQuerier) ListTaggedTransactions(ctx context.Context) ([]ListTaggedTransactionsRow, error) {
	rows, err := a.q.ListTaggedTransactions(ctx)
	return mapSlice(rows, func(r pg.ListTaggedTransactionsRow) ListTaggedTransactionsRow {
		return ListTaggedTransactionsRow(r)
	}), err
}

func (a pgQuerier) LatestSyncLog(ctx context.Context) (SyncLog, error) {
	row, err := a.q.LatestSyncLog(ctx)
	return SyncLog(row), err
}

func (a pgQuerier) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := a.q.ListAccounts(ctx)
	return mapSlice(rows, func(r pg.Account) Account { return Account(r) }), err
}

func (a pgQuerier) ListAccountsByOrg(ctx context.Context, orgID string) ([]Account, error) {
	rows, err := a.q.ListAccountsByOrg(ctx, orgID)
	return mapSlice(rows, func(r pg.Account) Account { return Account(r) }), err
}

func (a pgQuerier) ListOrganizations(ctx context.Context) ([]Organization, error) {
	rows, err := a.q.ListOrganizations(ctx)
	return mapSlice(rows, func(r pg.Organization) Organization { return Organization(r) }), err
}

func (a pgQuerier) ListSyncLogs(ctx context.Context, rowLimit int64) ([]SyncLog, error) {
	rows, err := a.q.ListSyncLogs(ctx, int32(rowLimit))
	return mapSlice(rows, func(r pg.SyncLog) SyncLog { return SyncLog(r) }), err
}

func (a pgQuerier) ListTransactions(ctx context.Context, arg ListTransactionsParams) ([]Transaction, error) {
	rows, err := a.q.ListTransactions(ctx, pg.ListTransactionsParams{
		Since:     arg.Since,
		Until:     arg.Until,
		RowOffset: int32(arg.RowOffset),
		RowLimit:  int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) ListTransactionsByAccount(ctx context.Context, arg ListTransactionsByAccountParams) ([]Transaction, error) {
	rows, err := a.q.ListTransactionsByAccount(ctx, pg.ListTransactionsByAccountParams{
		AccountID: arg.AccountID,
		Since:     arg.Since,
		Until:     arg.Until,
		RowOffset: int32(arg.RowOffset),
		RowLimit:  int32(arg.RowLimit),
	})
	return mapSlice(rows, func(r pg.Transaction) Transaction { return Transaction(r) }), err
}

func (a pgQuerier) UpsertAccount(ctx context.Context, arg UpsertAccountParams) error {
	return a.q.UpsertAccount(ctx, pg.UpsertAccountParams(arg))
}

func (a pgQuerier) UpsertOrganization(ctx context.Context, arg UpsertOrganizationParams) error {
	return a.q.UpsertOrganization(ctx, pg.UpsertOrganizationParams(arg))
}

func mapSlice[T, U any](in []T, conv func(T) U) []U {
	out := make([]U, len(in))
	for i := range in {
		out[i] = conv(in[i])
	}
	return out
}
