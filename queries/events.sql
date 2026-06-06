-- name: InsertEvent :one
-- Appends one immutable event to the canonical stream. The generated id
-- (sequence) and the stored row are returned so the caller can broadcast the
-- just-committed event to live SSE subscribers. event_id is a caller-supplied
-- UUID (unique); data is a JSON object.
INSERT INTO events (event_id, event_type, entity_type, entity_id, occurred_at, data)
VALUES (
    sqlc.arg(event_id), sqlc.arg(event_type), sqlc.arg(entity_type),
    sqlc.arg(entity_id), sqlc.arg(occurred_at), sqlc.arg(data)
)
RETURNING *;

-- name: ListEventsAfter :many
-- The cursor read: every event whose sequence is greater than `after`, in stream
-- order, with optional exact-match filters (an empty string disables that filter).
-- Mirrors the ListTransactions optional-filter shape - the column is written first
-- so sqlc infers a non-null type for each bound parameter, keeping the generated
-- params struct byte-identical across SQLite and Postgres (row_limit aside, which
-- the pg adapter casts to int32). row_limit caps the page size.
SELECT * FROM events
WHERE id > sqlc.arg(after)
  AND (event_type = sqlc.arg(event_type) OR sqlc.arg(event_type) = '')
  AND (entity_type = sqlc.arg(entity_type) OR sqlc.arg(entity_type) = '')
  AND (entity_id = sqlc.arg(entity_id) OR sqlc.arg(entity_id) = '')
ORDER BY id
LIMIT sqlc.arg(row_limit);

-- name: ListRecentEvents :many
-- The most recent events, newest first. Powers "what just happened" views (the
-- dashboard live feed) that want the tail of the stream without first discovering
-- its head. Callers that want chronological order reverse the result.
SELECT * FROM events ORDER BY id DESC LIMIT sqlc.arg(row_limit);

-- name: GetEventBySequence :one
SELECT * FROM events WHERE id = sqlc.arg(id);

-- name: DeleteEventsBefore :execrows
-- Retention prune: drops events that occurred before the cutoff (unix seconds).
-- :execrows reports how many rows were removed, for logging.
DELETE FROM events WHERE occurred_at < sqlc.arg(cutoff);
