-- name: CreateRule :one
-- A rule pairs a condition (a kasas search query) with an action: a JSON object
-- of labels and a JSON object of schema extensions to apply on a match. The API
-- validates the query and normalizes both before storing. RETURNING * yields the
-- generated id and timestamps.
INSERT INTO rules (name, query, labels, extensions, enabled, created_at, updated_at)
VALUES (
    sqlc.arg(name), sqlc.arg(query), sqlc.arg(labels), sqlc.arg(extensions),
    sqlc.arg(enabled), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetRule :one
SELECT * FROM rules WHERE id = sqlc.arg(id);

-- name: ListRules :many
SELECT * FROM rules ORDER BY id;

-- name: ListEnabledRules :many
-- The deterministic id order makes rule precedence predictable when several
-- rules write the same label key (later rules win).
SELECT * FROM rules WHERE enabled = 1 ORDER BY id;

-- name: UpdateRule :execrows
-- Replaces the editable fields of a rule. :execrows lets the caller detect a
-- missing id (0 rows affected).
UPDATE rules
SET name       = sqlc.arg(name),
    query      = sqlc.arg(query),
    labels     = sqlc.arg(labels),
    extensions = sqlc.arg(extensions),
    enabled    = sqlc.arg(enabled),
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: DeleteRule :execrows
DELETE FROM rules WHERE id = sqlc.arg(id);
