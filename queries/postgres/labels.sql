-- Postgres-specific label queries. The `labels` column is TEXT (to keep the
-- generated struct byte-identical to the SQLite backend's); JSON querying is done
-- with an inline ::jsonb cast. `->>` extracts a key's value as text (NULL when
-- the key is absent), and the `-` operator removes a key. The query names match
-- the SQLite versions in queries/sqlite/labels.sql so both backends expose the
-- same Querier methods.

-- name: FilterTransactionsByLabelKey :many
SELECT * FROM transactions
WHERE labels::jsonb ->> sqlc.arg(label_key) IS NOT NULL
  AND (account_id = sqlc.arg(account_id) OR sqlc.arg(account_id) = '')
  AND (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: FilterTransactionsByLabelValue :many
SELECT * FROM transactions
WHERE labels::jsonb ->> sqlc.arg(label_key) = sqlc.arg(label_value)
  AND (account_id = sqlc.arg(account_id) OR sqlc.arg(account_id) = '')
  AND (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: DeleteLabelByKey :execrows
-- Removes one key from every transaction that carries it.
UPDATE transactions
SET labels = (labels::jsonb - sqlc.arg(label_key)::text)::text
WHERE labels::jsonb ->> sqlc.arg(label_key) IS NOT NULL;

-- name: DeleteLabelByValue :execrows
-- Removes the key only from transactions where it holds the given value (one
-- value per key, so removing the key on a value match drops exactly that pair).
UPDATE transactions
SET labels = (labels::jsonb - sqlc.arg(label_key)::text)::text
WHERE labels::jsonb ->> sqlc.arg(label_key) = sqlc.arg(label_value);
