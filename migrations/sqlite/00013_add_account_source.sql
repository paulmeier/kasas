-- +goose Up
-- +goose StatementBegin
-- Account provenance: which ingestion path created this account, mirroring the
-- transactions.source column (migration 00011). It is the marker that lets kasas
-- tell user-created ("manual") accounts apart from bridge-owned ("simplefin")
-- ones, so only manual accounts can be edited or deleted through the API while
-- synced accounts stay owned by the poller. Like transactions.source it cannot be
-- reconstructed after the fact, so it is stamped at insert by whoever creates the
-- row.
-- A constant default is required to add a NOT NULL column to a STRICT table, and it
-- backfills every existing row correctly (all current accounts are SimpleFIN).
-- Source is never written by UpsertAccount's conflict-update path, so re-syncs
-- leave it intact: the lineage is immutable, like labels and extensions.
ALTER TABLE accounts ADD COLUMN source TEXT NOT NULL DEFAULT 'simplefin';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE accounts DROP COLUMN source;
-- +goose StatementEnd
