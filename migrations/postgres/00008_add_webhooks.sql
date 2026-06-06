-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite webhooks table. Columns are all int64/string so sqlc generates
-- a Webhook struct byte-identical to the SQLite backend's, keeping the pgstore
-- adapter a thin whole-struct cast. `event_types` is text (a JSON array string, not
-- jsonb) to match. See the SQLite migration for column semantics. Timestamps are
-- unix seconds.
CREATE TABLE webhooks (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    url             text NOT NULL,
    secret          text NOT NULL,
    event_types     text NOT NULL DEFAULT '[]',
    enabled         bigint NOT NULL DEFAULT 1,
    created_at      bigint NOT NULL,
    updated_at      bigint NOT NULL,
    last_status     bigint NOT NULL DEFAULT 0,
    last_error      text NOT NULL DEFAULT '',
    last_attempt_at bigint NOT NULL DEFAULT 0,
    last_success_at bigint NOT NULL DEFAULT 0
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_webhooks_enabled ON webhooks (enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE webhooks;
-- +goose StatementEnd
