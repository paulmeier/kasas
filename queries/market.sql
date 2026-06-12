-- name: UpsertMarketSeries :exec
INSERT INTO market_series (id, provider, symbol, kind, currency, adjusted, meta)
VALUES (
    sqlc.arg(id),
    sqlc.arg(provider),
    sqlc.arg(symbol),
    sqlc.arg(kind),
    sqlc.arg(currency),
    sqlc.arg(adjusted),
    sqlc.arg(meta)
)
ON CONFLICT (id) DO UPDATE SET
    provider = excluded.provider,
    symbol   = excluded.symbol,
    kind     = excluded.kind,
    currency = excluded.currency,
    adjusted = excluded.adjusted,
    meta     = excluded.meta;

-- name: GetMarketSeries :one
SELECT id, provider, symbol, kind, currency, adjusted, meta
FROM market_series
WHERE id = sqlc.arg(id);

-- name: ListMarketSeries :many
SELECT id, provider, symbol, kind, currency, adjusted, meta
FROM market_series
ORDER BY id;

-- name: DeleteMarketSeries :execrows
DELETE FROM market_series WHERE id = sqlc.arg(id);

-- name: UpsertMarketPoint :exec
INSERT INTO market_points (series_id, date, value, fetched_at)
VALUES (sqlc.arg(series_id), sqlc.arg(date), sqlc.arg(value), sqlc.arg(fetched_at))
ON CONFLICT (series_id, date) DO UPDATE SET
    value      = excluded.value,
    fetched_at = excluded.fetched_at;

-- name: ListMarketPoints :many
-- Points for a series within an optional [since, until] date window. An empty
-- bound means unbounded on that side; ISO-8601 dates sort lexically.
SELECT series_id, date, value, fetched_at
FROM market_points
WHERE series_id = sqlc.arg(series_id)
  AND (sqlc.arg(since) = '' OR date >= sqlc.arg(since))
  AND (sqlc.arg(until) = '' OR date <= sqlc.arg(until))
ORDER BY date;

-- name: LatestMarketPoint :one
-- The newest-dated cached point for a series. Returns sql.ErrNoRows when the
-- series has never been fetched (cold cache); otherwise its date is the series'
-- "as of" and its fetched_at is the last refresh time (a refresh upserts the
-- whole window with one timestamp), which the read path uses for TTL freshness.
-- Avoids a COALESCE(MAX(...)) aggregate, whose sqlc type inference differs across
-- SQLite and Postgres and would break the struct-identity pgstore adapter.
SELECT series_id, date, value, fetched_at
FROM market_points
WHERE series_id = sqlc.arg(series_id)
ORDER BY date DESC
LIMIT 1;

-- name: CountMarketPoints :one
SELECT COUNT(*) FROM market_points WHERE series_id = sqlc.arg(series_id);

-- name: DeleteMarketSeriesPoints :exec
DELETE FROM market_points WHERE series_id = sqlc.arg(series_id);

-- name: TruncateMarketPoints :exec
DELETE FROM market_points;

-- name: TruncateMarketSeries :exec
DELETE FROM market_series;
