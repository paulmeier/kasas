-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite api_keys table. Columns are all int64/string so sqlc generates
-- an ApiKey struct byte-identical to the SQLite backend's, keeping the pgstore
-- adapter a thin whole-struct cast. See the SQLite migration for column semantics.
-- Timestamps are unix seconds.
CREATE TABLE api_keys (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         text NOT NULL DEFAULT '',
    prefix       text NOT NULL,
    key_hash     text NOT NULL,
    scope        text NOT NULL DEFAULT 'read',
    created_at   bigint NOT NULL,
    last_used_at bigint NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_api_keys_key_hash ON api_keys (key_hash);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys (prefix);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE api_keys;
-- +goose StatementEnd
