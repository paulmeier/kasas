-- name: CreateSyncLog :one
INSERT INTO sync_log (started_at, status)
VALUES (sqlc.arg(started_at), sqlc.arg(status))
RETURNING *;

-- name: CompleteSyncLog :exec
UPDATE sync_log
SET completed_at = sqlc.arg(completed_at),
    status       = sqlc.arg(status),
    error        = sqlc.arg(error)
WHERE id = sqlc.arg(id);

-- name: ListSyncLogs :many
SELECT * FROM sync_log
ORDER BY started_at DESC
LIMIT sqlc.arg(row_limit);

-- name: LatestSyncLog :one
SELECT * FROM sync_log
ORDER BY started_at DESC
LIMIT 1;
