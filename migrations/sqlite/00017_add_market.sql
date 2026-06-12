-- +goose Up
-- +goose StatementBegin
-- External market/reference data: a rebuildable TTL cache, NOT ledger facts (ADR
-- 0006). These tables hold time series about the world (benchmark indices, fund
-- NAVs, FX, crypto) fetched on demand from a market provider and cached with a
-- daily TTL. They carry NO foreign keys into the ledger tables in either
-- direction: market data is independent of accounts/transactions and is wiped by
-- "kasas market reset" at any time, so nothing may depend on its retention.
--
-- market_series is one configured series: a stable internal id (e.g. "spy"), the
-- provider it is fetched from, the provider-native symbol, what kind of thing it
-- is, the currency its values are quoted in, whether it is a total-return /
-- split-adjusted series, and a JSON meta blob (display name, license note). meta
-- is NOT NULL DEFAULT '{}' to match the ledger's JSON-as-text convention.
CREATE TABLE market_series (
    id       TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    symbol   TEXT NOT NULL,
    kind     TEXT NOT NULL,
    currency TEXT NOT NULL,
    adjusted INTEGER NOT NULL DEFAULT 0,
    meta     TEXT NOT NULL DEFAULT '{}'
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
-- market_points is the cached daily-close window for a series. value is a decimal
-- STRING (a price is money per unit, so it gets the same discipline as money).
-- date is an ISO-8601 date string (daily granularity; ISO dates sort lexically).
-- fetched_at is unix seconds and is load-bearing: freshness is a read-time TTL
-- policy, so a refresh stamps every upserted point with the fetch time. Older
-- points simply remain (history accumulates opportunistically), but this is a
-- cache, not an archive.
CREATE TABLE market_points (
    series_id  TEXT NOT NULL,
    date       TEXT NOT NULL,
    value      TEXT NOT NULL,
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (series_id, date)
) STRICT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE market_points;
DROP TABLE market_series;
-- +goose StatementEnd
