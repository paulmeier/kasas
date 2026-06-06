-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite rules table. Columns are chosen so sqlc generates a Rule
-- struct byte-identical to the SQLite backend's (all int64/string), keeping the
-- pgstore adapter a thin whole-struct cast. `labels` is TEXT (not jsonb) to match
-- transactions.labels. Timestamps are unix seconds.
CREATE TABLE rules (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL DEFAULT '',
    query      text NOT NULL,
    labels     text NOT NULL DEFAULT '{}',
    enabled    bigint NOT NULL DEFAULT 1,
    created_at bigint NOT NULL,
    updated_at bigint NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_rules_enabled ON rules (enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE rules;
-- +goose StatementEnd
