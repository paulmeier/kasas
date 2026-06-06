-- +goose Up
-- +goose StatementBegin
-- Schema extensions: arbitrary, app-owned namespaced metadata for a transaction,
-- stored as a JSON object whose values are arbitrary JSON (e.g.
-- '{"tax.category":"meal","forecast.recurring":true,"custom.myapp.score":88}').
-- This is parallel to labels (strict key->value strings), not a replacement.
-- text (not jsonb) keeps the generated struct byte-identical to the SQLite
-- backend's, which the pgstore adapter relies on. Extensions are never written by
-- the poller, so re-syncs leave them intact.
ALTER TABLE transactions ADD COLUMN extensions text NOT NULL DEFAULT '{}';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions DROP COLUMN extensions;
-- +goose StatementEnd
