-- SQLite-specific label queries. Labels are a JSON object (key->value) stored in
-- the TEXT `labels` column; these push filtering and deletion down to SQL via
-- SQLite's JSON1 functions.
--
-- Why json_extract with a built path rather than the `->>` operator: SQLite's
-- `->>` only resolves its right operand as an object label when that operand is a
-- string LITERAL. With a bound parameter it is treated as a JSON path and fails
-- to match, so we build a quoted path (`$."<key>"`) explicitly. The quotes also
-- make arbitrary keys (dots, spaces) safe; the API strips `"`/`\` from keys.
-- CAST(... AS TEXT) keeps the key parameter a non-null string (a bare `||`
-- operand is inferred nullable by sqlc).

-- name: FilterTransactionsByLabelKey :many
SELECT * FROM transactions
WHERE json_extract(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"') IS NOT NULL
  AND (account_id = sqlc.arg(account_id) OR sqlc.arg(account_id) = '')
  AND (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: FilterTransactionsByLabelValue :many
SELECT * FROM transactions
WHERE json_extract(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"') = sqlc.arg(label_value)
  AND (account_id = sqlc.arg(account_id) OR sqlc.arg(account_id) = '')
  AND (date >= sqlc.arg(since) OR sqlc.arg(since) = 0)
  AND (date <= sqlc.arg(until) OR sqlc.arg(until) = 0)
ORDER BY date DESC, id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: DeleteLabelByKey :execrows
-- Removes one key from every transaction that carries it.
UPDATE transactions
SET labels = json_remove(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"')
WHERE json_extract(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"') IS NOT NULL;

-- name: DeleteLabelByValue :execrows
-- Removes the key only from transactions where it holds the given value (one
-- value per key, so removing the key on a value match drops exactly that pair).
UPDATE transactions
SET labels = json_remove(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"')
WHERE json_extract(labels, '$."' || CAST(sqlc.arg(label_key) AS TEXT) || '"') = sqlc.arg(label_value);
