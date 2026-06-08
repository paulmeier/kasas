-- +goose Up
-- +goose StatementBegin
-- A rule's action can apply schema extensions (app-owned, namespaced JSON metadata,
-- see the 00009 migration) in addition to labels. `extensions` is a JSON object of
-- namespaced key->arbitrary-JSON-value pairs, the same storage form as
-- transactions.extensions. A constant default is required to add a NOT NULL column
-- to a STRICT table; existing rules apply no extensions until edited.
ALTER TABLE rules ADD COLUMN extensions TEXT NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE rules DROP COLUMN extensions;
-- +goose StatementEnd
