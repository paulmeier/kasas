-- +goose Up
-- +goose StatementBegin
-- Transaction relationships: explicit directed edges from one transaction to
-- another, stored as a JSON ARRAY of {"kind","target"} objects on the subject
-- (the "from" side), e.g.
-- '[{"kind":"refund_of","target":"txn_123"},{"kind":"transfer_to","target":"txn_456"}]'.
-- Each edge is asserted outbound from this row; the inbound direction ("who
-- points at me?") is derived by scanning, like the search matcher already does.
-- This is parallel to labels and extensions (per-transaction JSON), not a
-- replacement. A constant default is required to add a NOT NULL column to a
-- STRICT table. Relationships are never written by the poller, so re-syncs
-- (INSERT ... ON CONFLICT DO NOTHING / UpdateTransactionFromSync) leave them intact.
ALTER TABLE transactions ADD COLUMN relationships TEXT NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transactions DROP COLUMN relationships;
-- +goose StatementEnd
