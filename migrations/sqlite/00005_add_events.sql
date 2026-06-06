-- +goose Up
-- +goose StatementBegin
-- The canonical event stream: an append-only, ordered, immutable log of every
-- meaningful mutation (a transaction synced, a label applied, a rule run, ...).
-- It exists so external consumers can build sync engines, notifications,
-- automations, and CQRS / event-sourcing on top of kasas.
--
-- `id` is the monotonic sequence consumers page by - strictly increasing, but it
-- MAY have gaps (a rolled-back insert still consumes an autoincrement value), so
-- treat it as a cursor, not a count. `event_id` is a globally-unique UUID for
-- idempotent dedupe. `event_type` is a dotted entity.action, e.g.
-- transaction.created (named `event_type`, not `type`, because `type` is a
-- reserved word in sqlc's SQLite grammar - the JSON field stays `type`).
-- `entity_type` + `entity_id` identify the subject. `occurred_at` is unix seconds.
-- `data` is a self-contained JSON snapshot / details object, so a consumer (or a
-- *.deleted event whose entity is already gone) needs no follow-up query.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id    TEXT NOT NULL,
    event_type  TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL DEFAULT '',
    occurred_at INTEGER NOT NULL,
    data        TEXT NOT NULL DEFAULT '{}'
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX idx_events_event_id ON events (event_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_events_event_type ON events (event_type);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_events_entity ON events (entity_type, entity_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Supports the retention prune (DELETE ... WHERE occurred_at < cutoff).
CREATE INDEX idx_events_occurred_at ON events (occurred_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE events;
-- +goose StatementEnd
