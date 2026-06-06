-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite plugins table. Columns are all int64/string so sqlc generates
-- a Plugin struct byte-identical to the SQLite backend's, keeping the pgstore
-- adapter a thin whole-struct cast. `granted_capabilities` and `config` are text
-- (JSON strings, not jsonb) to match. See the SQLite migration for column
-- semantics. Timestamps are unix seconds.
CREATE TABLE plugins (
    id                   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                 text NOT NULL,
    runtime              text NOT NULL DEFAULT '',
    version              text NOT NULL DEFAULT '',
    enabled              bigint NOT NULL DEFAULT 0,
    granted_capabilities text NOT NULL DEFAULT '[]',
    config               text NOT NULL DEFAULT '{}',
    created_at           bigint NOT NULL,
    updated_at           bigint NOT NULL,
    last_status          bigint NOT NULL DEFAULT 0,
    last_error           text NOT NULL DEFAULT '',
    last_run_at          bigint NOT NULL DEFAULT 0,
    last_success_at      bigint NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_plugins_name ON plugins (name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE plugins;
-- +goose StatementEnd
