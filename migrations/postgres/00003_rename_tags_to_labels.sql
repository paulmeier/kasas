-- +goose Up
-- +goose StatementBegin
-- Tags become "labels": strict key->value pairs stored as a JSON object (e.g.
-- '{"category":"food","person":"dad"}') rather than a JSON array of strings.
-- This is a clean-slate migration: existing tag data is cleared, not converted.
-- text (not jsonb) keeps the generated struct byte-identical to the SQLite
-- backend's, which the pgstore adapter relies on; JSON querying is done with an
-- inline ::jsonb cast in the per-dialect label queries.
ALTER TABLE transactions RENAME COLUMN tags TO labels;
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE transactions SET labels = '{}';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE transactions ALTER COLUMN labels SET DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions ALTER COLUMN labels SET DEFAULT '[]';
-- +goose StatementEnd
-- +goose StatementBegin
UPDATE transactions SET labels = '[]';
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE transactions RENAME COLUMN labels TO tags;
-- +goose StatementEnd
