-- name: InsertTransaction :execrows
-- transactions.id is the SimpleFIN transaction ID, so re-syncing the same
-- transaction is a no-op. This keeps polling idempotent.
INSERT INTO transactions (
    id, account_id, amount, pending, date, description, payee, memo, synced_at
)
VALUES (
    sqlc.arg(id), sqlc.arg(account_id), sqlc.arg(amount), sqlc.arg(pending),
    sqlc.arg(date), sqlc.arg(description), sqlc.arg(payee), sqlc.arg(memo),
    sqlc.arg(synced_at)
)
ON CONFLICT (id) DO NOTHING;

-- name: ListTransactions :many
-- A bound of 0 disables that side of the date filter (so 0/0 returns all).
-- The column comparison is written first so sqlc infers an integer type for
-- the bound parameters from the `date` column.
SELECT * FROM transactions
WHERE (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: ListTransactionsByAccount :many
SELECT * FROM transactions
WHERE account_id = sqlc.arg(account_id)
  AND (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: GetTransaction :one
SELECT * FROM transactions
WHERE id = sqlc.arg(id);

-- name: CountTransactions :one
SELECT COUNT(*) FROM transactions;
