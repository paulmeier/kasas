-- name: InsertApiKey :one
-- Stores a new API key. Only the SHA-256 hash of the secret is persisted (the full
-- key is shown to the caller once and never stored); `prefix` is a non-secret
-- fragment for display. RETURNING * yields the generated id.
INSERT INTO api_keys (name, prefix, key_hash, scope, created_at, last_used_at)
VALUES (
    sqlc.arg(name), sqlc.arg(prefix), sqlc.arg(key_hash), sqlc.arg(scope),
    sqlc.arg(created_at), sqlc.arg(last_used_at)
)
RETURNING *;

-- name: GetApiKeyByHash :one
-- The per-request verification lookup: hash the presented bearer and find its key.
SELECT * FROM api_keys WHERE key_hash = sqlc.arg(key_hash);

-- name: ListApiKeys :many
-- Newest first, for the dashboard list (the secret is never returned, only metadata).
SELECT * FROM api_keys ORDER BY id DESC;

-- name: DeleteApiKey :execrows
-- Revoke a key by id. :execrows lets the caller detect a missing id (0 rows).
DELETE FROM api_keys WHERE id = sqlc.arg(id);

-- name: UpdateApiKeyLastUsed :exec
-- Best-effort touch of the last-used timestamp after a successful verification.
UPDATE api_keys SET last_used_at = sqlc.arg(last_used_at) WHERE id = sqlc.arg(id);
