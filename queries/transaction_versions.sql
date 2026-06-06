-- name: InsertTransactionVersion :one
-- Appends one immutable snapshot to a transaction's history. The generated id
-- (insert order) and the stored row are returned. There is no per-transaction
-- version number column: a transaction's versions are its rows ordered by id, and
-- the API assigns the ordinal (v1, v2, ...) on read. data is a JSON object.
INSERT INTO transaction_versions (transaction_id, change_kind, occurred_at, data)
VALUES (
    sqlc.arg(transaction_id), sqlc.arg(change_kind), sqlc.arg(occurred_at),
    sqlc.arg(data)
)
RETURNING *;

-- name: ListTransactionVersions :many
-- One transaction's full history, oldest first. ORDER BY id is the version order
-- (id is the monotonic insert sequence), so the caller assigns v1, v2, ... by
-- position and diffs each snapshot against the previous one.
SELECT * FROM transaction_versions
WHERE transaction_id = sqlc.arg(transaction_id)
ORDER BY id;

-- name: CountTransactionVersions :one
-- Whether a transaction has any history yet. Backs the lazy baseline: the first
-- time an existing transaction changes after this feature shipped, the writer
-- synthesizes a v1 "imported" snapshot from its prior state. COUNT(*) is used (not
-- an EXISTS/MAX) because sqlc infers it as int64 identically on SQLite and Postgres
-- (see CountTransactions), keeping the pgstore adapter a plain pass-through.
SELECT COUNT(*) FROM transaction_versions
WHERE transaction_id = sqlc.arg(transaction_id);

-- name: DeleteTransactionVersionsBefore :execrows
-- Retention prune: drops versions that occurred before the cutoff (unix seconds).
-- :execrows reports how many rows were removed, for logging. Only runs when
-- events.history_retention_days > 0; the default keeps history forever.
DELETE FROM transaction_versions WHERE occurred_at < sqlc.arg(cutoff);
