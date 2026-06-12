-- +goose Up
-- +goose StatementBegin
-- Mirrors the SQLite market_series table. Columns are string/int64 so sqlc
-- generates a MarketSeries struct byte-identical to the SQLite backend's, keeping
-- the pgstore adapter a thin whole-struct cast. See the SQLite migration for
-- semantics. This is a rebuildable TTL cache with no foreign keys into the ledger.
CREATE TABLE market_series (
    id       text PRIMARY KEY,
    provider text NOT NULL,
    symbol   text NOT NULL,
    kind     text NOT NULL,
    currency text NOT NULL,
    adjusted bigint NOT NULL DEFAULT 0,
    meta     text NOT NULL DEFAULT '{}'
);
-- +goose StatementEnd

-- +goose StatementBegin
-- Mirrors the SQLite market_points table. value is a decimal string, date an
-- ISO-8601 date string, fetched_at unix seconds. See the SQLite migration.
CREATE TABLE market_points (
    series_id  text NOT NULL,
    date       text NOT NULL,
    value      text NOT NULL,
    fetched_at bigint NOT NULL,
    PRIMARY KEY (series_id, date)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE market_points;
DROP TABLE market_series;
-- +goose StatementEnd
