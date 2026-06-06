-- +goose Up
-- +goose StatementBegin
-- API keys are per-consumer credentials for programmatic REST access, distinct
-- from the single admin dashboard token. Each external integration (a budgeting
-- app, a fraud detector, ...) gets its own key so access can be scoped and revoked
-- independently.
--
-- Only the SHA-256 hash of the secret is stored, never the secret itself: the full
-- key is shown to the operator exactly once at creation and is irrecoverable after
-- (verification hashes the presented bearer and looks it up by `key_hash`).
-- `prefix` is a short, non-secret leading fragment of the key, kept so the
-- dashboard can identify a key in a list without holding anything usable. `scope`
-- is 'read' (GET endpoints only) or 'read_write' (GET + mutations); provisioning
-- stays admin-only, so a key can never escalate. `created_at`/`last_used_at` are
-- unix seconds (last_used_at is 0 until the key is first presented).
CREATE TABLE api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL DEFAULT '',
    prefix       TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    scope        TEXT NOT NULL DEFAULT 'read',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
-- The hash is the verification lookup key (an O(1) unique-index hit per request).
CREATE UNIQUE INDEX idx_api_keys_key_hash ON api_keys (key_hash);
-- +goose StatementEnd

-- +goose StatementBegin
-- The prefix is shown in the dashboard and is unique enough to identify a key.
CREATE UNIQUE INDEX idx_api_keys_prefix ON api_keys (prefix);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE api_keys;
-- +goose StatementEnd
