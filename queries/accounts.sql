-- name: UpsertAccount :exec
INSERT INTO accounts (id, org_id, name, currency, balance, balance_date, synced_at)
VALUES (
    sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(currency),
    sqlc.arg(balance), sqlc.arg(balance_date), sqlc.arg(synced_at)
)
ON CONFLICT (id) DO UPDATE SET
    org_id       = excluded.org_id,
    name         = excluded.name,
    currency     = excluded.currency,
    balance      = excluded.balance,
    balance_date = excluded.balance_date,
    synced_at    = excluded.synced_at;

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
