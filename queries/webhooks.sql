-- name: InsertWebhook :one
-- Registers a webhook endpoint. `event_types` is a JSON array string of subscribed
-- types ('[]'/'["*"]' = all). RETURNING * yields the generated id and timestamps.
INSERT INTO webhooks (url, secret, event_types, enabled, created_at, updated_at)
VALUES (
    sqlc.arg(url), sqlc.arg(secret), sqlc.arg(event_types), sqlc.arg(enabled),
    sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetWebhook :one
SELECT * FROM webhooks WHERE id = sqlc.arg(id);

-- name: ListWebhooks :many
SELECT * FROM webhooks ORDER BY id;

-- name: ListEnabledWebhooks :many
-- The dispatcher loads this set on each event and filters by type in Go.
SELECT * FROM webhooks WHERE enabled = 1 ORDER BY id;

-- name: UpdateWebhook :execrows
-- Replaces the editable fields. :execrows lets the caller detect a missing id.
UPDATE webhooks
SET url         = sqlc.arg(url),
    event_types = sqlc.arg(event_types),
    enabled     = sqlc.arg(enabled),
    updated_at  = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateWebhookSecret :execrows
-- Rotates the signing secret (and bumps updated_at).
UPDATE webhooks
SET secret = sqlc.arg(secret), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdateWebhookDeliveryStatus :exec
-- Records the outcome of the most recent delivery attempt on the webhook row (the
-- lean alternative to a per-delivery table). last_success_at is only advanced on a
-- 2xx; the caller passes the existing value otherwise.
UPDATE webhooks
SET last_status     = sqlc.arg(last_status),
    last_error      = sqlc.arg(last_error),
    last_attempt_at = sqlc.arg(last_attempt_at),
    last_success_at = sqlc.arg(last_success_at)
WHERE id = sqlc.arg(id);

-- name: DeleteWebhook :execrows
DELETE FROM webhooks WHERE id = sqlc.arg(id);
