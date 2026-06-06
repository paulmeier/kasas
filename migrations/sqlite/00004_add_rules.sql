-- +goose Up
-- +goose StatementBegin
-- Rules auto-apply labels to transactions whose fields match a saved query (the
-- kasas search syntax). `query` is the condition; `labels` is a JSON object of
-- key->value pairs to apply on a match (same storage form as transactions.labels).
-- Enabled rules run against every newly-synced transaction; any rule can also be
-- run on demand over existing transactions. Timestamps are unix seconds.
CREATE TABLE rules (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL DEFAULT '',
    query      TEXT NOT NULL,
    labels     TEXT NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_rules_enabled ON rules (enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE rules;
-- +goose StatementEnd
