-- +goose Up
-- +goose StatementBegin
-- User-defined tags for a transaction, stored as a JSON array of strings (e.g.
-- '["groceries","food"]'). A constant default is required to add a NOT NULL
-- column to a STRICT table. Tags are never written by the poller, so re-syncs
-- (INSERT ... ON CONFLICT DO NOTHING) leave them intact.
ALTER TABLE transactions ADD COLUMN tags TEXT NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions DROP COLUMN tags;
-- +goose StatementEnd
