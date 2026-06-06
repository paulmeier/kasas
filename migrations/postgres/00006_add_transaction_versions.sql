-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite transaction_versions table. Columns are all int64/string so
-- sqlc generates a TransactionVersion struct byte-identical to the SQLite backend's,
-- keeping the pgstore adapter a thin whole-struct cast. `data` is text (not jsonb)
-- to match. See the SQLite migration for the column semantics. Timestamps are unix
-- seconds.
CREATE TABLE transaction_versions (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id text NOT NULL,
    change_kind    text NOT NULL,
    occurred_at    bigint NOT NULL,
    data           text NOT NULL DEFAULT '{}'
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_transaction_versions_txn ON transaction_versions (transaction_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_transaction_versions_occurred_at ON transaction_versions (occurred_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE transaction_versions;
-- +goose StatementEnd
