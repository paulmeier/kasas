-- +goose Up
-- +goose StatementBegin
-- Tags become "labels": strict key->value pairs stored as a JSON object (e.g.
-- '{"category":"food","person":"dad"}') rather than a JSON array of strings.
-- This is a clean-slate migration: existing tag data is cleared, not converted.
-- The renamed column keeps its old DEFAULT '[]', but it is never used because
-- InsertTransaction writes an explicit '{}' (SQLite cannot change a STRICT
-- table's column default without rebuilding the table).
ALTER TABLE transactions RENAME COLUMN tags TO labels;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE transactions SET labels = '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions RENAME COLUMN labels TO tags;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE transactions SET tags = '[]';
-- +goose StatementEnd
