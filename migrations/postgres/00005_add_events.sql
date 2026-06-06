-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite events table. Columns are all int64/string so sqlc generates
-- an Event struct byte-identical to the SQLite backend's, keeping the pgstore
-- adapter a thin whole-struct cast. `data` is text (not jsonb) to match. See the
-- SQLite migration for the column semantics. Timestamps are unix seconds.
CREATE TABLE events (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id    text NOT NULL,
    event_type  text NOT NULL,
    entity_type text NOT NULL,
    entity_id   text NOT NULL DEFAULT '',
    occurred_at bigint NOT NULL,
    data        text NOT NULL DEFAULT '{}'
);
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
CREATE INDEX idx_events_occurred_at ON events (occurred_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE events;
-- +goose StatementEnd
