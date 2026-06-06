-- name: InsertTransaction :execrows
-- transactions.id is the SimpleFIN transaction ID, so re-syncing the same
-- transaction is a no-op. This keeps polling idempotent. labels is written as an
-- explicit empty object so new rows never depend on the column default (SQLite
-- can't cheaply change a STRICT table's default; see the 00003 migration).
INSERT INTO transactions (
    id, account_id, amount, pending, date, description, payee, memo, synced_at,
    labels
)
VALUES (
    sqlc.arg(id), sqlc.arg(account_id), sqlc.arg(amount), sqlc.arg(pending),
    sqlc.arg(date), sqlc.arg(description), sqlc.arg(payee), sqlc.arg(memo),
    sqlc.arg(synced_at), '{}'
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

-- name: UpdateTransactionLabels :execrows
-- Replaces the whole label set for one transaction. labels is a JSON object of
-- key->value pairs; the API normalizes it before storing. :execrows lets the
-- caller detect a missing id (0 rows affected). The poller never touches labels,
-- so this is the only writer.
UPDATE transactions SET labels = sqlc.arg(labels) WHERE id = sqlc.arg(id);

-- name: ListLabeledTransactions :many
-- Returns the (id, labels) of every transaction that carries at least one label.
-- The API explodes the JSON objects in Go to build the label vocabulary with
-- per-pair transaction counts. Done in Go (not SQL) to stay portable across
-- SQLite and Postgres: json_each (SQLite) and jsonb_each_text (Postgres) infer
-- different column types, which would break the byte-identical pgstore adapter.
-- Filtering and deletion, by contrast, are pushed down to SQL (see the
-- per-dialect queries/{sqlite,postgres}/labels.sql). ORDER BY makes the row order
-- deterministic.
SELECT id, labels FROM transactions WHERE labels <> '{}' ORDER BY id;
