# ADR 0006 — External market & reference data as a first-class source

- **Status:** Proposed
- **Date:** 2026-06-12
- **Related:** [Ingestion & Sources](../ingestion.md), [Data Model](../data-model.md),
  [ADR 0002](0002-plugin-network-capability.md),
  [ADR 0005](0005-plugin-originated-transactions.md), and sillview's
  [ADR-0004](https://paulmeier.github.io/sillview/architecture/decisions/0004-external-market-data-ownership-and-storage/)
  (the dashboard-side half of this decision; lands with sillview PR #18 —
  note each repo numbers its own ADR log)

## Context

The question arrived from the dashboard: *"how is my mutual fund doing compared
to the S&P 500?"* Answering it needs data that will never come from the user's
institutions — benchmark index levels, fund NAVs, FX rates. Call it **external
market/reference data**: time series about the *world*, where the ledger holds
facts about *your money*.

kasas has none of it. The [data model](../data-model.md) stores organizations,
accounts, transactions, labels, extensions, and relationships; amounts are exact
decimal strings; an account carries one `currency` code and one latest
`balance` + `balance_date`. There is no prices table, no securities or ticker
model, no FX, no valuation.

What kasas *does* have is a complete ingestion machine: the
[source seam](../ingestion.md) (`source.Source` + `Puller`), six pull sources,
a scheduled poller with cursors and a serialized sync, `sync_log`, the event
stream, and per-source runtime credentials. The `Archetype` enum already
reserves slots beyond `pull`.

Two deployment shapes matter here. kasas runs **bundled** inside the sillview
desktop app on localhost — and it runs as a **remote self-hosted server**
reached over the network by one or more dashboards plus other API clients
(API keys, webhooks, rules, plugins, MCP). The design must serve both.

The decision being asked: should this data live in kasas at all, or in the
dashboard that wants to chart it? The dashboard could fetch quotes itself (its
main process has network access). But data only one client can see is data the
rest of the platform cannot react to — and in the remote shape, client-side
fetching means every dashboard burns provider quota independently and every
desktop holds provider keys. On the sillview side the argument is even
sharper: its renderer reaches exactly one host through a brokered channel, and
its user-created-widget design pins user specs to *kasas paths, never hosts*.
Both repos' records agree on the split: **kasas owns fetching, caching, and
serving; the dashboard owns visualization.** This ADR is the canonical record
for the kasas half; sillview's ADR-0004 is canonical for the consumption half.

## Decision

Add external market/reference data as a first-class source — but **fetched on
demand through a server-side read-through cache**, not copied wholesale on a
schedule. The contract: data is fetched when someone actually looks at it,
cached with a TTL, and never accrues sync or backfill obligations.

### 1. On-demand fetch through a read-through cache

The API read path is the trigger:

- **Fresh cache** → serve from `market_*`.
- **Stale cache** → serve the cached points immediately and refresh in the
  background (*stale-while-revalidate*); when the refresh lands, emit a
  `market.updated` event — clients already subscribe to the
  [event stream](../../features/event-stream.md) and refetch.
- **Cold cache** (series never fetched) → fetch synchronously under a timeout;
  the caller sees one slower first render, then cache hits.

Mechanics the engine owns, because they protect a *shared* resource:

- **Single-flight per series** — five widgets across two dashboards asking for
  the same cold series collapse into one provider call.
- **Per-provider rate limiting and backoff**, enforced where the quota actually
  lives: at the server, not per client.
- Demand-driven does not mean unscheduled-only: the market source is still a
  registered source (archetype `reference`), so it inherits source config,
  runtime credentials, readiness in `/api/v1/sources`, and the generic
  `POST /api/v1/sources/{type}/sync` — which here means *warm the cache for the
  configured series*, an optional convenience logged to `sync_log`, not the
  primary path.

### 2. Storage: the cache **is** the `market_*` namespace

```sql
-- World facts, not ledger facts: a TTL cache. No FKs into ledger tables.
CREATE TABLE market_series (
    id        TEXT PRIMARY KEY,   -- stable internal id, e.g. "sp500tr"
    provider  TEXT NOT NULL,      -- e.g. "tiingo"
    symbol    TEXT NOT NULL,      -- provider-native symbol
    kind      TEXT NOT NULL,      -- index | fund | equity | fx | crypto
    currency  TEXT NOT NULL,      -- ISO code values are quoted in
    adjusted  INTEGER NOT NULL,   -- 1 = total-return / split-adjusted series
    meta      TEXT                -- JSON: display name, license/attribution, …
);

CREATE TABLE market_points (
    series_id  TEXT NOT NULL,
    date       TEXT NOT NULL,     -- ISO-8601 date; daily close granularity
    value      TEXT NOT NULL,     -- decimal STRING — same discipline as money
    fetched_at INTEGER NOT NULL,  -- load-bearing: freshness is a read-time policy
    PRIMARY KEY (series_id, date)
);
```

- **Freshness is a read-time policy.** A daily series is stale once a newer
  close should exist (per-series TTL, default ~24h). No background job decides
  freshness; the read path does.
- **Refreshes upsert; retention is best-effort.** A refresh replaces the
  requested window; older points simply remain, so historical depth
  *accumulates opportunistically* — but this is a cache, not an archive:
  `kasas market reset` may wipe it at any time and nothing is allowed to
  depend on retention. No backfill jobs, no gap repair.
- **Restatements self-heal.** Adjusted series are restated retroactively
  (splits, dividends); refreshing the viewed window replaces the affected
  points. A permanent copy would need restatement *detection* instead — one of
  the main reasons the copy model lost (see Alternatives).
- These tables live **in the existing database**, not a second SQLite file. A
  separate file was weighed and rejected: it breaks the sqlite ↔ Postgres
  parity of the store abstraction and doubles store/migration plumbing.
  Independence is enforced by contract instead: no foreign keys in either
  direction, excluded from data exports by default, and market migrations stay
  additive and isolated so a bad one cannot threaten ledger data.
- Values are **decimal strings** (a price is money per unit); granularity is
  **daily closes only**.

### 3. API: read-tier series routes

- `GET /api/v1/market/series` → `{ "series": [ … ] }`
- `GET /api/v1/market/series/{id}/points?since=…&until=…` → `{ "points": [ … ] }`
  (responses carry freshness, e.g. an `as_of` timestamp, so clients can show
  "as of yesterday's close" honestly)

Named-key wrapping and tiering follow the existing
[REST conventions](../../interfaces/rest-api.md): readable with the dashboard
token or any API key.

### 4. Providers: agnostic interface, server-owned keys, visible egress

- One small provider interface; the first concrete provider is a **follow-up
  decision**, so nothing here may prejudge it.
- API keys go through the existing runtime credential machinery, never
  `config.toml` — and they live **on the server**. Enabling/configuring
  providers is **admin-tier** like other source credential management: a
  dashboard connected with the dashboard token can manage providers from its
  Settings UI; a client holding only a read-only API key can consume series
  but not reconfigure.
- Series carry **internal ids** with provider symbols in `meta`, so a provider
  migration is a remap, not a data loss. (Providers die — IEX Cloud, 2024.)
- New egress class: kasas already calls banks; now it also calls a market
  provider — declared hosts, as visible as a plugin's
  [`net:fetch` allowlist](0002-plugin-network-capability.md), never silent.
- **Candidate input for the follow-up** (unvetted, recorded so it isn't lost):
  Alpha Vantage (25 req/day — viable *behind* a daily TTL cache, some
  mutual-fund coverage); Finnhub (generous rate limit; verify free access to
  historical daily candles); Market Data (verify fund coverage and history
  depth); Alpaca (brokerage-account, trading-oriented — poor fit); MarketStack
  (100 req/month, ~1 yr history — too thin); plus **Tiingo** (strong free EOD
  incl. mutual-fund NAV), **Stooq** (keyless EOD CSV — keyless-default
  candidate), **FRED** (official `SP500` series). Two findings already
  constrain the choice: the S&P 500 *index level* is licensed IP that free
  tiers often won't serve (an ETF proxy like SPY, or FRED, is the usual
  substitute), and mutual-fund NAV coverage varies sharply by provider.

### 5. One server, many dashboards

The remote deployment is what settles *where* the cache lives:

- **One cache, one quota.** N clients share the server's cache: the first
  viewer pays the fetch; everyone else hits cache; single-flight collapses
  simultaneous cold reads. Client-side caches invert this — every viewer
  burns quota independently and other consumers still see nothing.
- **Operator consent.** Egress originates from the *server's* network; where
  operator ≠ viewer, it is the operator who consents to the declared provider
  hosts, via server-side config — not the dashboard user.
- **Quota math holds at the server.** A handful of configured daily series ≈ a
  handful of provider calls per day *regardless of viewer count* — within even
  the smallest free tier. (Realtime would invert this too: five live widgets
  on a 1-minute TTL ≈ 300 calls/hour. Hence the non-goal below.)
- The bundled-local deployment is this same picture on localhost.

### What this ADR deliberately does **not** do

- **No realtime, no intraday.** On-demand ≠ realtime: the motivating
  comparison is a *daily NAV* question, and daily granularity is what makes
  the cache cheap and the free tiers sufficient. Streaming/WebSocket feeds are
  trading infrastructure, out of scope by decision.
- **No archive promises.** Retention is opportunistic; the cache is wipe-safe
  by contract. If durable history ever becomes a requirement (provider death,
  shrinking free-tier lookbacks), that is a deliberate new decision, not a
  drift.
- **No securities master, no symbol search, no fundamentals.** kasas stores
  the series the user configured, full stop.
- **No valuation engine.** FX/crypto-to-base-currency conversion becomes a
  natural client of these tables; where it computes is a follow-up decision.
- **No holdings/positions model and no balance history.** Accounts keep one
  latest balance, so an honest "my fund vs the index" is, for now, a benchmark
  *overlay* — not performance attribution. Per-sync balance snapshots are the
  highest-value follow-up this ADR queues.
- **No data redistribution.** Transient caching for the user's own display is
  within typical provider terms; *redistribution* is not — index levels are
  themselves licensed. Per-provider attribution requirements live in
  `market_series.meta`.

## Consequences

- Provider quota and egress are proportional to what users actually view; an
  unwatched dashboard costs nothing.
- No staleness management, no backfill, no gap repair; restatements self-heal
  on the next refresh of the viewed window.
- One copy of the data, visible to every consumer — dashboards, plugins,
  webhooks, rules, MCP, raw API users — at one shared quota.
- **Cold-cache latency is real:** the first render of a never-fetched series
  blocks on a provider round-trip (bounded by timeout); afterwards
  stale-while-revalidate keeps reads instant. Dashboards show a normal loading
  state.
- kasas permanently owns provider churn, quotas, and key UX — bounded by the
  provider-agnostic interface and internal series ids.
- The migration chain now carries non-ledger tables (additive-and-isolated
  rule above).
- The first user-visible comparison is honest-but-modest until balance
  snapshots land; dashboard copy must say "balance", never "return".

## Alternatives considered

- **Scheduled pull-and-persist (this ADR's own first draft).** A poller-driven
  `SeriesPuller` copying configured series on an interval, like the ledger
  sources. Rejected on reflection: the copy goes stale between syncs yet syncs
  whether or not anyone looks; backfill and gap-repair obligations accrue;
  restatements require detection logic; quota burns on idle servers. The
  demand-driven cache inverts all four. Scheduled *warming* survives as an
  optional convenience on the same machinery.
- **Client-side fetching and caching (e.g. Redis inside the desktop app).**
  Rejected: a cache daemon inside a DMG-shipped, single-process desktop app
  adds a second managed binary for exactly one reader — and the remote-kasas
  shape breaks it outright: every dashboard would fetch and cache
  independently (N× quota), provider keys would scatter to every client
  machine, and non-dashboard consumers would still see nothing. An embedded
  client cache wouldn't fix those last three; the placement is the problem,
  not the technology. (The dashboard's own record additionally rejects
  renderer-adjacent egress on security grounds — sillview ADR-0004.)
- **A plugin fetches it via [`net:fetch`](0002-plugin-network-capability.md).**
  Zero core changes and a ready distribution channel — but a plugin has no
  series-shaped place to *put* the data: its writes are labels/extensions, and
  even [ADR 0005's `source:provide` seam](0005-plugin-originated-transactions.md)
  produces *transactions*, not reference series. Revisit if a `series:provide`
  seam ever lands — the provider interface here is shaped so a plugin-backed
  provider could slot in behind it.
- **A second SQLite database.** Rejected for dialect parity and plumbing; kept
  as the fallback if the cache ever develops write patterns that measurably
  hurt the ledger DB. See Decision 2.
- **Do nothing.** Benchmark comparison is a core personal-finance question;
  every consumer answering it ad hoc reproduces the trapped-data, scattered-
  key, and licensing problems above, client by client.
