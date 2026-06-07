-- name: UpsertAccount :exec
-- source is the provenance of the account ("simplefin" for synced, "manual" for a
-- user-created one) and is a bound argument, not a literal, so the manual-account
-- writer can stamp its own. It is intentionally NOT in the DO UPDATE SET list: like
-- transactions.source, provenance is immutable, so a re-sync of an existing account
-- never rewrites it.
INSERT INTO accounts (id, org_id, name, currency, balance, balance_date, synced_at, source)
VALUES (
    sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(currency),
    sqlc.arg(balance), sqlc.arg(balance_date), sqlc.arg(synced_at), sqlc.arg(source)
)
ON CONFLICT (id) DO UPDATE SET
    org_id       = excluded.org_id,
    name         = excluded.name,
    currency     = excluded.currency,
    balance      = excluded.balance,
    balance_date = excluded.balance_date,
    synced_at    = excluded.synced_at;

-- name: UpdateAccount :execrows
-- Updates a manual account's user-owned fields (the org and source are immutable
-- provenance, set once at creation). :execrows lets the caller detect a missing id
-- (0 rows affected). The manual-only gate is enforced in the API, not here.
UPDATE accounts
SET name         = sqlc.arg(name),
    currency     = sqlc.arg(currency),
    balance      = sqlc.arg(balance),
    balance_date = sqlc.arg(balance_date),
    synced_at    = sqlc.arg(synced_at)
WHERE id = sqlc.arg(id);

-- name: DeleteAccount :execrows
-- Deletes one account. Its transactions are removed by the ON DELETE CASCADE on
-- transactions.account_id; the API enumerates and emits a transaction.deleted for
-- each cascaded row (and cleans their history/relationships) before calling this,
-- since the cascade is invisible to the event stream. :execrows reports whether the
-- account existed. The manual-only gate is enforced in the API.
DELETE FROM accounts WHERE id = sqlc.arg(id);

-- name: ListAccounts :many
SELECT * FROM accounts
ORDER BY name;

-- name: ListAccountsByOrg :many
SELECT * FROM accounts
WHERE org_id = sqlc.arg(org_id)
ORDER BY name;

-- name: GetAccount :one
SELECT * FROM accounts
WHERE id = sqlc.arg(id);
