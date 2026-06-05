-- +goose Up
-- +goose StatementBegin
CREATE TABLE organizations (
    id       text PRIMARY KEY,
    domain   text NOT NULL DEFAULT '',
    name     text NOT NULL DEFAULT '',
    sfin_url text NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE accounts (
    id           text PRIMARY KEY,
    org_id       text NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name         text NOT NULL,
    currency     text NOT NULL,
    balance      text NOT NULL,
    balance_date bigint NOT NULL,
    synced_at    bigint NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE transactions (
    id          text PRIMARY KEY,
    account_id  text NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    amount      text NOT NULL,
    pending     bigint NOT NULL DEFAULT 0,
    date        bigint NOT NULL,
    description text NOT NULL DEFAULT '',
    payee       text NOT NULL DEFAULT '',
    memo        text NOT NULL DEFAULT '',
    synced_at   bigint NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE sync_log (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    started_at   bigint NOT NULL,
    completed_at bigint,
    status       text NOT NULL,
    error        text
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_accounts_org_id ON accounts (org_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_transactions_account_id ON transactions (account_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_transactions_date ON transactions (date DESC);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_sync_log_started_at ON sync_log (started_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE sync_log;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE transactions;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE accounts;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE organizations;
-- +goose StatementEnd
