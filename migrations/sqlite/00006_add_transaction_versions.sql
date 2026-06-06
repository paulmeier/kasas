-- +goose Up
-- +goose StatementBegin
-- The immutable transaction history: an append-only, full-snapshot record of what
-- a transaction looked like at each meaningful change (imported, synced from the
-- bridge, labeled). It answers "why does this transaction look different today than
-- last month" without replaying anything, because every row is a complete snapshot.
--
-- This is distinct from the events table on purpose. Events are a prunable,
-- fine-grained change log (a label.applied carries only the changed key); versions
-- are durable, coarse, whole-transaction snapshots meant to be kept. They share the
-- same transactional recorder (internal/events) but never each other's retention.
--
-- `id` is the monotonic insert order; a transaction's versions are exactly its rows
-- ordered by id, so there is no separate per-transaction version number (the API
-- assigns v1, v2, ... as the ordinal on read). `change_kind` is one of imported,
-- synced, or labeled. `occurred_at` is unix seconds. `data` is the full
-- TransactionPayload JSON snapshot at this version (the same shape embedded in
-- transaction.* events), so a consumer needs no follow-up query.
CREATE TABLE transaction_versions (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    transaction_id TEXT NOT NULL,
    change_kind    TEXT NOT NULL,
    occurred_at    INTEGER NOT NULL,
    data           TEXT NOT NULL DEFAULT '{}'
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
-- Supports the per-transaction timeline read (WHERE transaction_id ORDER BY id).
CREATE INDEX idx_transaction_versions_txn ON transaction_versions (transaction_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Supports the retention prune (DELETE ... WHERE occurred_at < cutoff).
CREATE INDEX idx_transaction_versions_occurred_at ON transaction_versions (occurred_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE transaction_versions;
-- +goose StatementEnd
