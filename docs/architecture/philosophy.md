# Philosophy

> kasas is a financial ledger that can power all kinds of apps.

That sentence is the whole design. Everything below is a consequence of taking it
seriously.

## What kasas is

kasas is a **headless financial data platform**. It does three things, and tries
to do them exceptionally well:

1. **Ingest** — connect to your bank through [SimpleFIN](https://www.simplefin.org/)
   and pull in organizations, accounts, and transactions on a schedule.
2. **Store** — keep that data in a durable, queryable ledger (SQLite or Postgres)
   with a clean, stable shape and a complete record of how it changed.
3. **Expose** — make it programmable through a REST API, an MCP server, a
   canonical event stream, webhooks, and sandboxed plugins.

It owns the **boring, load-bearing parts** of any personal-finance system:
authenticating to a bridge, deduplicating transactions, refreshing a pending
charge when it posts, preserving the metadata you added, recording an immutable
history, and emitting an ordered event for every change. These are the parts
that are tedious to build, easy to get subtly wrong, and identical across every
app that touches your money.

## What kasas is not

kasas is **not** a Mint replacement, a budgeting app, a tax tool, or a financial
planner. It has no opinion about your spending, no budgets, no projections, no
charts of your net worth over time.

That restraint is intentional. The moment a ledger grows a budgeting opinion, it
starts shaping its data model around that opinion, and everything else becomes a
second-class citizen. kasas stays **unopinionated about the application layer** so
that the application layer can be anything.

!!! quote "The thesis"
    Don't build another finance app. Build the **ledger that finance apps run
    on** — then let a hundred apps bloom on top of it, yours and everyone
    else's.

## The platform primitives

A backbone is only useful if it gives builders the right seams. kasas exposes
five, and the rest of this documentation is mostly about how each one works:

| Primitive | What it gives an app builder |
| --- | --- |
| **[Event stream](../features/event-stream.md)** | An append-only, replayable log of every change. Read *what changed and when* instead of re-diffing state — the substrate for sync engines, automations, and event-sourcing. |
| **[Webhooks](../features/webhooks.md)** | The event stream pushed outward, HMAC-signed, so an external service reacts to changes without polling. |
| **[Plugins](../features/plugins.md)** | The event stream consumed in-process, in a sandboxed VM, so logic that wants to live close to the data can. |
| **[Schema extensions](../features/schema-extensions.md)** | Arbitrary namespaced JSON any app attaches to a transaction — extend the model without a migration or coordination. |
| **[MCP server](../interfaces/mcp.md)** | The whole ledger as tools an AI agent can call directly. |

Around those sit the everyday building blocks — [labels](../features/labels.md),
[search](../features/search.md), [rules](../features/rules.md), and
[transaction history](../features/transaction-history.md) — each available
identically across [every surface](overview.md#three-surfaces-one-core).

## Design principles

These show up again and again in the internals.

- **Lean on storage, comprehensive on exposure.** Reuse existing tables and a
  JSON column before adding schema; but when a capability exists, wire it across
  *every* surface (REST, MCP, dashboard) with full parity. A feature that only
  exists in the UI isn't a platform feature.
- **The data you add is sacred.** A sync refreshes what the bank owns and never
  touches what you own. Your labels and extensions survive every re-sync,
  correction, and re-pull.
- **Every change is a fact.** Changes are recorded as durable events and
  immutable history snapshots, written in the *same database transaction* as the
  change itself, so the record can never disagree with reality.
- **Money is exact.** Amounts are stored and compared as decimal strings, never
  parsed into a float.
- **Boring to run.** A single static binary, no CGO, an embedded database, an
  embedded dashboard, graceful shutdown, Prometheus metrics, and structured
  logs. It should be unremarkable to self-host and forget.
- **Safe by construction.** Slow or crashing extensions (webhooks, plugins) run
  *after* commit and can never block or corrupt a sync; untrusted plugin code
  runs sandboxed and capability-gated; secrets are stored hashed or `0600`.

## Where to go next

- See the principles realized in the [System Overview](overview.md).
- See the shape of the data in the [Data Model](data-model.md).
- See the seams you build on in [Webhooks](../features/webhooks.md),
  [Plugins](../features/plugins.md), and the [Event Stream](../features/event-stream.md).
