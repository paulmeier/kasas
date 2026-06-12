# ADR 0006 — External market & reference data as a first-class source

- **Status:** Proposed
- **Date:** 2026-06-12
- **Related:** [Ingestion & Sources](../ingestion.md), [Data Model](../data-model.md),
  [ADR 0002](0002-plugin-network-capability.md), [ADR 0003](0003-marketplace-trust-tiers.md),
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
reserves slots beyond `pull` (`file`, `webhook`, `manual`, `enrichment`) with
the stated intent that new archetype interfaces land as they are built.

The decision being asked: should this data live in kasas at all, or in the
dashboard that wants to chart it? The dashboard could fetch quotes itself
(its main process has network access). But kasas is a **headless server with
many consumers** — API keys, webhooks, rules, plugins, MCP — and data only one
client can see is data the rest of the platform cannot react to. On the
sillview side the argument is even sharper: its renderer reaches exactly one
host through a brokered channel, and its user-created-widget design pins user
specs to *kasas paths, never hosts*; a second data API would erode that
security model. Both repos' records agree on the split: **kasas owns
ingestion, storage, and serving; the dashboard owns visualization.** This ADR
is the canonical record for the kasas half; sillview's ADR-0004 is canonical
for the consumption half.

## Decision

Add external market/reference data as a **first-class source** with its own
archetype, a dedicated storage namespace, and read-tier API routes.

### 1. A new archetype with a series-shaped seam

`Puller` returns an `ImportBatch` — accounts and transactions. A market source
has no business producing either, so it gets its own seam rather than abusing
the ledger-shaped one:

- a new archetype, e.g. `ArchetypeReference` — *"fetches world data on a
  schedule; writes no ledger rows"*;
- a `SeriesPuller` interface beside `Puller`: fetch configured series since a
  cursor, return points; the engine owns persistence, scheduling, `sync_log`
  rows, and completion events, exactly as it does for ledger sources.

The poller schedules it like any other source; the generic
`POST /api/v1/sources/{type}/sync` gives on-demand refresh for free; completion
emits an event so plugins and webhooks can react to fresh prices.

### 2. Storage: a `market_*` namespace, rebuildable by contract

```sql
-- World facts, not ledger facts: a rebuildable cache. No FKs into ledger tables.
CREATE TABLE market_series (
    id        TEXT PRIMARY KEY,   -- stable internal id, e.g. "sp500tr"
    provider  TEXT NOT NULL,      -- e.g. "stooq"
    symbol    TEXT NOT NULL,      -- provider-native symbol
    kind      TEXT NOT NULL,      -- index | fund | equity | fx | crypto
    currency  TEXT NOT NULL,      -- ISO code values are quoted in
    adjusted  INTEGER NOT NULL,   -- 1 = total-return / split-adjusted series
    meta      TEXT                -- JSON: display name, license note, …
);

CREATE TABLE market_points (
    series_id  TEXT NOT NULL,
    date       TEXT NOT NULL,     -- ISO-8601 date; daily close granularity
    value      TEXT NOT NULL,     -- decimal STRING — same discipline as money
    fetched_at INTEGER NOT NULL,
    PRIMARY KEY (series_id, date)
);
```

These tables live **in the existing database**, not a second SQLite file. A
separate file was seriously considered (it buys physical ledger purity and
delete-the-file rebuilds) and rejected: it breaks the sqlite ↔ Postgres parity
of the store abstraction — Postgres deployments would need a second database or
a sqlite sidecar — and doubles the store/migration plumbing for a cache whose
daily series run ~252 rows per year. The independence is enforced by
**contract** instead:

- no foreign keys in either direction between `market_*` and ledger tables;
- `kasas market reset` truncates the namespace; everything is rebuildable from
  provider + symbol + date range;
- `market_*` is excluded from data exports by default;
- market migrations stay additive and isolated so a bad one cannot threaten
  ledger data.

Values are **decimal strings** (a price is money per unit), and the granularity
is **daily closes only** — intraday multiplies quota cost, storage, and
provider complexity for a ledger that compares months, not minutes.

### 3. API: read-tier series routes

- `GET /api/v1/market/series` → `{ "series": [ … ] }`
- `GET /api/v1/market/series/{id}/points?since=…&until=…` → `{ "points": [ … ] }`

Named-key wrapping and tiering follow the existing
[REST conventions](../../interfaces/rest-api.md): readable with the dashboard
token or any API key, like other read-tier routes.

### 4. Providers: agnostic interface, user-owned keys, visible egress

- One small provider interface; the first concrete provider is a **follow-up
  decision**, so nothing here may prejudge it.
- API keys go through the existing runtime credential machinery, never
  `config.toml`.
- Series carry **internal ids** with provider symbols in `meta`, so a provider
  migration is a remap, not a data loss. (Providers die — IEX Cloud, 2024 — and
  free tiers are tight.)
- This adds a new egress class: kasas already calls banks; now it also calls a
  market provider. The only leak is ticker interest, but the host should be as
  visible as a plugin's [`net:fetch` allowlist](0002-plugin-network-capability.md)
  — declared in the source config, never silent.

### What this ADR deliberately does **not** do

- **No intraday, no securities master, no symbol search, no fundamentals.**
  kasas stores the series the user configured, full stop. Each of those would
  need its own ADR.
- **No valuation engine.** FX/crypto-to-base-currency conversion becomes a
  natural client of these tables, but where it computes and how it is labeled
  is a follow-up decision.
- **No holdings/positions model and no balance history.** Accounts keep one
  latest balance, so an honest "my fund vs the index" is, for now, a benchmark
  *overlay* — not performance attribution. Recording per-sync balance
  snapshots is the highest-value follow-up this ADR queues, and any
  total-return comparison methodology (time-weighted vs money-weighted, price
  vs total-return indices) is another.
- **No data redistribution.** Market data is licensed IP; virtually every
  provider prohibits redistributing even "free" data, and index levels are
  themselves licensed. A "datashare marketplace" where users exchange fetched
  datasets would make the project an unlicensed data vendor. The viable shape
  is sharing **connectors** — code that fetches from the original source under
  each user's own key — through the existing
  [marketplace trust tiers](0003-marketplace-trust-tiers.md). Datasets as
  code, never as data; that too is a follow-up decision.

## Consequences

- One copy of the data, visible to every consumer: dashboard widgets, plugins,
  webhooks, rules, MCP, and raw API users.
- The ingestion machinery is reused nearly wholesale; the ledger stays pure via
  the no-FK, rebuildable-cache contract.
- kasas permanently owns provider churn, quotas, and key UX — accepted, and
  bounded by the provider-agnostic interface and internal series ids.
- The migration chain now carries non-ledger tables (mitigated by the
  additive-and-isolated rule above).
- The first user-visible comparison is honest-but-modest until balance
  snapshots land; the dashboard copy must say "balance", never "return".

## Alternatives considered

- **The dashboard fetches it (sillview main process + local store).** Fastest
  to ship, and sillview's ADR-0002 had even sketched the egress allowlist for
  it. Rejected: traps the data in one client, duplicates scheduler/retry/
  cursor/credential machinery in TypeScript, and breaks the dashboard's own
  "specs name a path, never a host" security invariant. Recorded in detail in
  sillview's ADR-0004, which narrows that escape hatch accordingly.
- **A plugin fetches it via [`net:fetch`](0002-plugin-network-capability.md).**
  Zero core changes and a ready distribution channel — but a plugin has no
  series-shaped place to *put* the data. Its writes are labels/extensions, and
  even [ADR 0005's `source:provide` seam](0005-plugin-originated-transactions.md)
  produces *transactions*, not reference series. Stuffing daily closes into
  transaction extensions abuses the data model. Revisit if a `series:provide`
  seam ever lands — the engine-owned persistence model here is designed so a
  plugin-backed provider could slot in behind the same `SeriesPuller` shape.
- **A second SQLite database.** Rejected for dialect parity and plumbing;
  kept as the fallback if the cache ever develops write patterns that
  measurably hurt the ledger DB (WAL churn, backup time). See Decision 2.
- **Do nothing.** Benchmark comparison is a core personal-finance question;
  every consumer answering it ad hoc (widgets calling third-party APIs
  directly) reproduces exactly the trapped-data and licensing problems above,
  client by client.
