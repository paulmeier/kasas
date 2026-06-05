-- +goose Up
-- +goose StatementBegin
CREATE TABLE organizations (
    id       TEXT PRIMARY KEY,
    domain   TEXT NOT NULL DEFAULT '',
    name     TEXT NOT NULL DEFAULT '',
    sfin_url TEXT NOT NULL DEFAULT ''
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE accounts (
    id           TEXT PRIMARY KEY,
    org_id       TEXT NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    currency     TEXT NOT NULL,
    balance      TEXT NOT NULL,
    balance_date INTEGER NOT NULL,
    synced_at    INTEGER NOT NULL
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE transactions (
    id          TEXT PRIMARY KEY,
    account_id  TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    amount      TEXT NOT NULL,
    pending     INTEGER NOT NULL DEFAULT 0,
    date        INTEGER NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    payee       TEXT NOT NULL DEFAULT '',
    memo        TEXT NOT NULL DEFAULT '',
    synced_at   INTEGER NOT NULL
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE sync_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at   INTEGER NOT NULL,
    completed_at INTEGER,
    status       TEXT NOT NULL,
    error        TEXT
) STRICT;
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
