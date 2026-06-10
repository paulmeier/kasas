-- +goose Up
-- +goose StatementBegin
-- Settings are persisted configuration overrides set from the dashboard, REST
-- API, or MCP. Each row overrides one config key (e.g. "plugins.enabled",
-- "plaid.client_id") on top of the config file / KASAS_ environment at startup,
-- so a change made from the dashboard is permanent across restarts. Only keys in
-- the internal/settings registry are honored at boot; unknown rows are ignored
-- (forward compatibility). Secret-valued settings are NOT stored here -- they go
-- to the secret store (Vault or the local secrets file) like source credentials.
-- `value` is the string form of the setting (booleans "true"/"false", durations
-- "6h", the CSV folder profiles as JSON). `updated_at` is unix seconds.
CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE settings;
-- +goose StatementEnd
