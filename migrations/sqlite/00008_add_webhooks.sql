-- +goose Up
-- +goose StatementBegin
-- Webhooks push the canonical event stream outward: kasas POSTs each matching
-- event to a registered endpoint, HMAC-signed, so external apps can react to
-- changes without polling. They are the outbound counterpart to the pull stream
-- (REST cursor + SSE); a consumer that misses a delivery (downtime, a dead
-- endpoint) reconciles via GET /api/v1/events?after=<cursor>, which is why
-- delivery is intentionally best-effort and there is no durable per-delivery queue.
--
-- `url` is the destination. `secret` is the HMAC signing secret, stored in
-- plaintext because signing needs it and the operator must be able to reveal it to
-- configure the receiver (same trust model as the stored dashboard token /
-- SimpleFIN access URL). `event_types` is a JSON array of subscribed types; '[]' or
-- '["*"]' means all types (matched in Go, not SQL). `enabled` gates delivery.
-- The last_* columns record delivery health for the dashboard without a separate
-- table: `last_status` is the most recent HTTP status (0 = never delivered),
-- `last_error` the most recent failure message, and last_attempt_at/last_success_at
-- are unix seconds (0 = never).
CREATE TABLE webhooks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    url             TEXT NOT NULL,
    secret          TEXT NOT NULL,
    event_types     TEXT NOT NULL DEFAULT '[]',
    enabled         INTEGER NOT NULL DEFAULT 1,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    last_status     INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    last_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
-- The dispatcher loads the enabled set on each event, so index that predicate.
CREATE INDEX idx_webhooks_enabled ON webhooks (enabled);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE webhooks;
-- +goose StatementEnd
