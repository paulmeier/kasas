# Market data

**Market/reference data** is time series about the *world* — benchmark indices,
fund NAVs, FX rates, crypto prices — as opposed to the ledger's facts about *your
money*. It answers questions the user's institutions never will, like *"how is my
mutual fund doing compared to the S&P 500?"*

kasas owns **fetching, caching, and serving** this data through a server-side
**read-through cache**: a series is fetched from a provider only when someone
actually looks at it, cached with a daily TTL, and never copied wholesale on a
schedule. This is the backend half of
[ADR 0006](../architecture/decisions/0006-external-market-reference-data.md); the
desktop dashboard ([sillview](https://github.com/paulmeier/sillview)) owns the
charting.

Source: [`internal/market`](https://github.com/paulmeier/kasas/tree/main/internal/market)
(the cache + provider interface) and
[`internal/market/alphavantage`](https://github.com/paulmeier/kasas/tree/main/internal/market/alphavantage)
(the first provider).

## How it works

Market data is a [`reference` archetype](../architecture/ingestion.md#archetypes-not-providers)
source — the first one that does **not** produce ledger transactions. Instead of
being [pulled](sync.md) on a schedule, it is driven by the **API read path**:

- **Fresh cache** → served straight from `market_*`.
- **Stale cache** (a newer daily close should exist) → the cached points are served
  *immediately* and a refresh runs in the background
  (*stale-while-revalidate*); when it lands, a `market.updated`
  [event](event-stream.md) fires and subscribed clients refetch.
- **Cold cache** (a series never fetched) → one synchronous fetch under a timeout,
  then served. The first view of a series is a little slower; everything after is a
  cache hit.

Mechanics the cache owns because they protect a *shared* resource:

- **Single-flight per series** — five widgets across two dashboards asking for the
  same cold series collapse into **one** provider call.
- **Per-provider rate limiting** — enforced at the server, where the quota actually
  lives, not per client.
- **Restatements self-heal.** Adjusted (total-return) series are restated
  retroactively for splits and dividends; refreshing the viewed window just replaces
  the affected points.

### Series, not securities

kasas stores exactly the **series the user configured** — no securities master, no
symbol search, no fundamentals. Each series has a **stable internal id** (e.g.
`spy`), the **provider-native symbol** (`SPY`), a **kind**, the **currency** values
are quoted in, and an optional display name:

| `kind` | What it is | Provider mapping (Alpha Vantage) |
| --- | --- | --- |
| `equity` | A stock or ETF | `TIME_SERIES_DAILY` |
| `fund` | A mutual fund (by symbol) | `TIME_SERIES_DAILY` |
| `index` | A benchmark, usually via an ETF proxy (e.g. `SPY` for the S&P 500) | `TIME_SERIES_DAILY` |
| `fx` | A currency pair — symbol like `EUR/USD` | `FX_DAILY` |
| `crypto` | A digital currency — symbol like `BTC`, quoted in `currency` | `DIGITAL_CURRENCY_DAILY` |

Set `adjusted: true` to request a **total-return / split-adjusted** series where the
provider supports it (often a premium feature). The internal id means a provider
migration is a *remap*, not a data loss — providers die.

!!! info "Daily closes only"
    Intraday and realtime are out of scope by decision: the motivating question is a
    *daily* one, and daily granularity is what keeps the cache cheap and the free
    tiers sufficient. The dashboard shows an honest "as of yesterday's close".

## Providers & keys

The provider sits behind a small, provider-agnostic interface, so the first pick is
not load-bearing. The first provider is **Alpha Vantage**
([free key](https://www.alphavantage.co/support/#api-key)).

The provider **API key is a runtime source credential** — set it from the dashboard
**Market** (or **Sources**) page, or
`PUT /api/v1/sources/market/credential`, and it is stored in the secret store,
**never `config.toml`**. Configuring providers and defining series is **admin-tier**,
like other source credential management: a dashboard holds the dashboard token and
can manage them; a client holding only a read-only API key can *consume* series but
not reconfigure them.

!!! warning "Free-tier quota & egress"
    Alpha Vantage's free tier is **~25 requests/day** — ample *behind* the daily
    cache, since a handful of configured series is a handful of calls per day no
    matter how many viewers there are. kasas now makes a new class of outbound call
    (to `www.alphavantage.co`); that host is declared on the source and shown on the
    **Sources** page, as visible as a [plugin's `net:fetch`
    allowlist](../architecture/decisions/0002-plugin-network-capability.md), never
    silent.

## It is a cache, not an archive

The `market_*` tables are a **rebuildable TTL cache** with **no foreign keys** into
the ledger in either direction. Historical depth accumulates opportunistically as
windows are refreshed, but nothing may depend on its retention:

```
kasas market reset
```

wipes every cached series and point. Your **definitions** survive (they live in the
`market.series` setting) and rebuild on the next read. The cache is excluded from
ledger concerns by contract, so a bad market migration can never threaten ledger
data.

## Configuration

```toml
[market]
provider         = "alphavantage"  # the data provider
ttl              = "24h"           # freshness window before a read refreshes a series
refresh_interval = "0s"            # 0 = on-demand only (recommended); set e.g. "12h" to warm on a schedule
# api_url        = ""              # override the provider base URL (proxies/self-hosting)
# series         = ""              # usually managed at runtime; a JSON array seeds series declaratively
```

The provider key is **not** here — it is a runtime credential. Series are normally
managed from the dashboard or the API and persisted in the database; `market.series`
is the declarative seed for reproducible deployments.

`ttl`, `refresh_interval`, `provider`, and `api_url` are editable from the
**Settings** page and take effect on the next [restart](../getting-started/configuration.md);
defining and removing series takes effect **immediately**.

## Interfaces

Reads are **read-tier** (the dashboard token or any API key); managing series and the
provider key is **admin-tier**.

### REST

| Method & path | Tier | Purpose |
| --- | --- | --- |
| `GET /api/v1/market/series` | read | List configured series with cache freshness (`as_of`, point count, `fresh`). |
| `GET /api/v1/market/series/{id}/points?since=&until=` | read | A series' daily closes (read-through), with `as_of`. |
| `POST /api/v1/market/series` | admin | Define a series (`id`, `symbol`, `kind`, `currency`, `adjusted`, `name`). |
| `DELETE /api/v1/market/series/{id}` | admin | Remove a series and clear its cache. |
| `PUT /api/v1/sources/market/credential` | admin | Set the provider API key. |
| `POST /api/v1/sources/market/sync` | write | Warm the cache for every configured series (optional). |

A global sync (`POST /api/v1/sync`, the dashboard's "Sync all") **skips** the market
source: it is a read-through cache, not a pull source, so a bulk sync never fetches
series nothing is displaying. Market data warms only on access (a widget reading
points), on its `refresh_interval` if one is set, or via the explicit per-source sync
above.

Values are **decimal strings**, the same discipline as money (a price is money per
unit).

### MCP

`list_market_series`, `get_market_points`, `add_market_series`, and
`remove_market_series` mirror the REST surface for agents.

### Dashboard

The **Market** page lists configured series with their freshness, sets the provider
key, defines/removes series, warms the cache, and shows a sparkline + recent closes
per series. The rich "growth of $10k" benchmark comparison lives in the desktop
dashboard.

## What this is **not** (yet)

Per ADR 0006, deliberately out of scope: realtime/intraday, a securities master or
symbol search, a valuation/FX-conversion engine, account **balance history** (so a
benchmark is an honest *overlay* — the chart says "balance", never "return", until
snapshots land), and data **redistribution** (transient caching for your own display
is within typical provider terms; redistribution is not).
