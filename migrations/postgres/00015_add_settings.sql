-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite settings table. Columns are string/int64 so sqlc generates
-- a Setting struct byte-identical to the SQLite backend's, keeping the pgstore
-- adapter a thin whole-struct cast. See the SQLite migration for semantics.
-- `updated_at` is unix seconds.
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      text NOT NULL,
    updated_at bigint NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE settings;
-- +goose StatementEnd
