-- +goose Up
-- +goose StatementBegin
-- Transaction provenance: which ingestion path this row came in through. Unlike
-- every other provenance field (source_transaction_id == id, imported_at/last_seen
-- from synced_at + the version log, transformations from transaction_versions),
-- source cannot be reconstructed from a transaction's contents after the fact, so
-- it is recorded at insert time by whoever ingests the row. Today the only path is
-- the SimpleFIN poller; a future bridge stamps its own source.
-- A constant default is required to add a NOT NULL column to a STRICT table, and it
-- backfills every existing row correctly (all current data is SimpleFIN). Source is
-- never written by UpdateTransactionFromSync, so re-syncs leave it intact: the
-- lineage is immutable, like labels and extensions.
ALTER TABLE transactions ADD COLUMN source TEXT NOT NULL DEFAULT 'simplefin';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions DROP COLUMN source;
-- +goose StatementEnd
