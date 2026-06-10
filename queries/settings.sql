-- name: ListSettings :many
SELECT * FROM settings
ORDER BY key;

-- name: UpsertSetting :exec
INSERT INTO settings (key, value, updated_at)
VALUES (sqlc.arg(key), sqlc.arg(value), sqlc.arg(updated_at))
ON CONFLICT (key) DO UPDATE SET
    value      = excluded.value,
    updated_at = excluded.updated_at;

-- name: DeleteSetting :execrows
DELETE FROM settings WHERE key = sqlc.arg(key);
