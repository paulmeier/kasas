-- name: InsertTransaction :execrows
-- transactions.id is the SimpleFIN transaction ID, so re-syncing the same
-- transaction is a no-op. This keeps polling idempotent. labels and extensions
-- are written as explicit empty objects so new rows never depend on the column
-- default (SQLite can't cheaply change a STRICT table's default; see the 00003
-- and 00009 migrations). source is the provenance of the row (which ingestion
-- path produced it) and is a bound argument, not a literal, so a future bridge
-- stamps its own; the poller passes "simplefin".
INSERT INTO transactions (
    id, account_id, amount, pending, date, description, payee, memo, synced_at,
    source, labels, extensions
)
VALUES (
    sqlc.arg(id), sqlc.arg(account_id), sqlc.arg(amount), sqlc.arg(pending),
    sqlc.arg(date), sqlc.arg(description), sqlc.arg(payee), sqlc.arg(memo),
    sqlc.arg(synced_at), sqlc.arg(source), '{}', '{}'
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

-- name: UpdateTransactionFromSync :execrows
-- Refreshes the bridge-owned fields of an existing transaction on re-sync (e.g. a
-- pending charge that has now posted, or a corrected amount). labels, extensions,
-- and source are intentionally NOT in the SET list: user metadata is never
-- clobbered, and source is immutable provenance set once at insert. The poller
-- calls this only when InsertTransaction reports the row already existed
-- (ON CONFLICT DO NOTHING affected 0 rows).
UPDATE transactions
SET account_id  = sqlc.arg(account_id),
    amount      = sqlc.arg(amount),
    pending     = sqlc.arg(pending),
    date        = sqlc.arg(date),
    description = sqlc.arg(description),
    payee       = sqlc.arg(payee),
    memo        = sqlc.arg(memo),
    synced_at   = sqlc.arg(synced_at)
WHERE id = sqlc.arg(id);

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

-- name: UpdateTransactionExtensions :execrows
-- Replaces the whole schema-extensions object for one transaction. extensions is
-- a JSON object of namespaced key->arbitrary-JSON-value pairs; the API normalizes
-- it before storing. :execrows lets the caller detect a missing id (0 rows
-- affected). The poller never touches extensions, so this is the only writer.
UPDATE transactions SET extensions = sqlc.arg(extensions) WHERE id = sqlc.arg(id);

-- name: ListExtendedTransactions :many
-- Returns the (id, extensions) of every transaction carrying at least one
-- extension. The API explodes the JSON objects in Go to build the extension
-- vocabulary (one entry per distinct key, with a transaction count). Done in Go,
-- like ListLabeledTransactions, to stay portable across SQLite and Postgres
-- (json_each vs jsonb_each infer different column types, which would break the
-- byte-identical pgstore adapter). ORDER BY makes the row order deterministic.
SELECT id, extensions FROM transactions WHERE extensions <> '{}' ORDER BY id;
