-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite rule-extensions column: a rule's action can apply schema
-- extensions (app-owned, namespaced JSON metadata, see the 00009 migration) in
-- addition to labels. text (not jsonb) keeps the generated Rule struct
-- byte-identical to the SQLite backend's, which the pgstore adapter relies on.
-- Same storage form as transactions.extensions.
ALTER TABLE rules ADD COLUMN extensions text NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE rules DROP COLUMN extensions;
-- +goose StatementEnd
