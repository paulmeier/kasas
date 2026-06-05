-- +goose Up
-- +goose StatementBegin
-- User-defined tags for a transaction, stored as a JSON array of strings (e.g.
-- '["groceries","food"]'). text (not jsonb) keeps the generated struct
-- byte-identical to the SQLite backend's, which the pgstore adapter relies on.
-- Tags are never written by the poller, so re-syncs leave them intact.
ALTER TABLE transactions ADD COLUMN tags text NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions DROP COLUMN tags;
-- +goose StatementEnd
