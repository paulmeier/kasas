# ADR 0005 — Plugin-originated transactions via the source seam (`source:provide`)

- **Status:** Proposed
- **Date:** 2026-06-11
- **Related:** [Plugins](../../features/plugins.md), [Ingestion & Sources](../ingestion.md), [ADR 0002](0002-plugin-network-capability.md), [ADR 0003](0003-marketplace-trust-tiers.md), [ADR 0004](0004-transaction-document-artifacts.md), [Philosophy](../philosophy.md)

## Context

Every plugin capability shipped so far lets a plugin **read or annotate** a
transaction that already exists — `transactions:read`, `labels:write`,
`extensions:write`, a `ui:page`, and (ADR 0002) `net:fetch` to enrich from the
outside. None of them can put a **new row** in the ledger.
[ADR 0004](0004-transaction-document-artifacts.md) hit this wall head-on and
drew the boundary deliberately: *"No `transactions:create` capability. Whether
plugins may originate transactions (vs. that staying a core source) is a
separate, larger decision and is out of scope here."*
[ADR 0003](0003-marketplace-trust-tiers.md) named the very same capability as the
*example* of an Unlisted, sideload-only surface (`a future transactions:create`).
This ADR is that deferred decision.

Two distinct user needs press on it — and, as in
[ADR 0004](0004-transaction-document-artifacts.md), conflating them produces the
wrong design:

1. **Ingest from a provider kasas doesn't ship.** A bank, a card, a budgeting
   export, a niche API reachable behind [`net:fetch`](0002-plugin-network-capability.md) —
   the user wants those transactions *in the ledger*, pulled on the sync
   schedule, deduped and labeled like every other source. Today that requires
   either a first-party Go source compiled into the binary or an external process
   driving the [REST API](../../interfaces/rest-api.md).
2. **Originate a derived row in reaction to an event.** A budgeting plugin that
   books a month-end "rollover" adjustment; a plugin that splits one charge into
   several; a synthetic interest-accrual line. The plugin watches the event
   stream and wants to write *one new transaction* back.

Both reduce to the same primitive — **introduce a transaction kasas did not
already have** — and the architecture already has a hard, load-bearing answer for
who is allowed to do that.

[Ingestion](../ingestion.md) draws exactly one line: **a source produces data;
the engine writes it.** A source returns an
[`ImportBatch`](../ingestion.md#the-importbatch) and *never touches the database*;
everything load-bearing — the transactional persist, idempotent dedup,
[events](../../features/event-stream.md), [rules](../../features/rules.md)
auto-labeling, [history](../../features/transaction-history.md), and
[provenance](../../features/transaction-provenance.md) stamping — lives in the
engine, written once and shared by SimpleFIN, Teller, Plaid, CSV, and the on-chain
sources alike. The payoff is precisely the property we need for third-party code:
*a buggy source is contained — the worst it can do is return a batch the engine
rejects.* The ingestion roadmap already reserves the conclusion:
*"Plugin-provided sources ride the same contract in the future: a plugin becomes a
producer that returns an `ImportBatch`, never a direct writer."*

So the question is not "may a plugin create transactions: yes or no." It is
**"does a plugin write rows, or does it produce a batch the engine writes?"** — and
the answer the rest of the platform has already committed to is the second.

## Decision

A plugin may originate transactions, but **only as a producer through the existing
[source seam](../ingestion.md#source-vs-engine)** — never through a host method
that writes a row. Introduce one capability, **`source:provide`**, that lets a
plugin return an `ImportBatch`; the **engine** persists it exactly as it persists
SimpleFIN's, with the same dedup, events, rules, history, and provenance. There is
no `kasas.create_transaction` that writes directly, *by design*.

### 1. The capability and the producer hook

`source:provide` is the producer capability — it parallels
[`net:fetch`](0002-plugin-network-capability.md) as a declared, host-mediated
grant, but instead of egress it grants *the right to hand the engine a batch*. It
comes as a unit with a `[source]` manifest block (just as `net:fetch` comes with
`[net]`), so the install/enable prompt can make a specific claim — *"this plugin
adds a source: acme-card"* — and the plugin implements a **producer hook** that
returns an `ImportBatch` in its runtime-native shape (the same neutral structure
built-in sources build in Go):

```toml
capabilities = ["net:fetch", "source:provide"]
hooks        = ["OnFetch"]        # scheduled producer — the engine calls it on the sync schedule

[source]
type      = "acme-card"           # the source type shown on the Sources page
archetype = "pull"                # pull (scheduled); a reactive producer declares no
                                  # [source] and returns a batch from an event hook instead
```

Two trigger shapes, matching the
[archetypes](../ingestion.md#archetypes-not-providers) the engine already runs:

- **Scheduled producer** (`pull`-like). The engine calls the plugin's `OnFetch`
  hook on the **sync schedule** (and on-demand), passing the `since`/`cursor` the
  [poller](../../features/sync.md) already threads to every `Puller`. The plugin
  fetches (typically with [`net:fetch`](0002-plugin-network-capability.md) — a
  plugin pulling a remote provider needs both capabilities) and returns the batch.
  A `source:provide` plugin is, to the engine, just another source: it appears on
  the [Sources page](../ingestion.md#whats-wired-today), syncs with the rest, and
  reports connection status.
- **Reactive producer** (event-driven). An existing event hook —
  `OnTransactionCreate`, `OnSyncComplete` — may *return* a batch to introduce
  derived rows (the rollover/split/accrual case). The engine persists the returned
  batch through the same path after the hook completes.

In both cases the plugin returns data; it does not write.

```lua
-- a scheduled producer: the engine calls this on the sync schedule
function OnFetch(req)              -- req.since, req.cursor
  local r = kasas.fetch{ url = "https://api.acme.example/txns?since=" .. req.since }
  -- ... parse r.body ...
  return {
    source   = "acme-card",       -- a human label; the engine stamps the real one (see §3)
    accounts = {
      { id = "acme-1", name = "ACME Card",
        transactions = {
          { external_id = "9f2a", amount = "-12.50", date = "2026-06-11",
            description = "Blue Bottle", payee = "Blue Bottle Coffee" },
        } },
    },
  }
end
```

### 2. The engine owns persistence — the plugin inherits the platform

The returned batch goes through the **same `internal/poller` persist** as every
built-in source. The plugin gets, for free and *without the ability to opt out*:

- **Idempotent dedup** on `(source, external_id)`, so a producer that re-emits the
  same row on every run inserts it once — the engine's reconciliation, not the
  plugin's bookkeeping, makes re-runs safe.
- **`transaction.created` events**,
  [history](../../features/transaction-history.md) baselines, and
  [rules](../../features/rules.md) auto-labeling — a plugin-produced row is a
  first-class ledger row, indistinguishable downstream from a SimpleFIN one, and
  flows onward to webhooks and other plugins.
- **Atomic persist.** Each row is committed the same way; a malformed row is
  rejected by the engine, not half-written.

A buggy or hostile `source:provide` plugin therefore **cannot** skip dedup,
double-emit an event, forge history, or corrupt an existing row — the same
containment that makes a buggy *built-in* source safe, now extended to third-party
code. This is the whole reason to route creation through the producer seam rather
than a write method.

### 3. Provenance: the engine stamps `plugin:<name>`, the plugin cannot forge it

The engine writes the [`transactions.source`](../data-model.md) stamp itself, as
it does for every source — and for a plugin it stamps **`plugin:<name>`**, derived
from the registered plugin, *not* from the `source` field the plugin returned. A
plugin cannot claim to be `simplefin`, cannot impersonate another source's rows,
and cannot overwrite the stamp on re-sync (provenance is immutable at insert).
Plugin-originated rows are thus a first-class, **attributable** category in the
[provenance](../../features/transaction-provenance.md) view: every reader can see
exactly which plugin put a row in the ledger.

Two consequences fall out of the stamp, both deliberate:

- **Plugin rows are not manual rows.**
  [Manual entry](../../features/manual-entry.md) gates edits and deletes on
  `source = 'manual'`; a `plugin:<name>` row is therefore **read-only to the
  manual-edit API** (a `409`, like a synced row). The plugin owns its rows through
  re-emission and dedup, the same way SimpleFIN owns its rows through re-sync — not
  through ad-hoc edits.
- **Accounts ride the same path.** A producer's `ImportAccount`s are created and
  stamped `plugin:<name>` through the existing account path
  ([manual entry](../../features/manual-entry.md) already added `accounts.source`),
  so a plugin source gets its own accounts without a new code path.

### 4. Trust tier: this is the top of the ladder

`source:provide` writes to the ledger's **core**, not just its annotations — it is
the most powerful capability kasas exposes, strictly above `net:fetch`. It is
exactly the capability [ADR 0003](0003-marketplace-trust-tiers.md) named as the
inhabitant of the **Unlisted / Trusted** tier (*"a future `transactions:create`"*):
**never auto-listed**, sideload-or-manual-review only, with the strongest "you are
on your own" posture. Enabling stays
[admin-only](../../features/plugins.md#enabling-is-opt-in-admin-only) like every
plugin, and — as ADR 0003 reasons for higher tiers — **WASM is the recommended
runtime**, since its isolation is enforced by the memory model. A scheduled
producer that also declares `net:fetch` is **doubly gated**: it carries both the
egress allowlist/grant flow of [ADR 0002](0002-plugin-network-capability.md) *and*
the top trust tier here.

### What this ADR deliberately does **not** do

- **No `kasas.create_transaction` host write.** There is no host method that
  inserts a row from inside the VM. Creation is *only* "return a batch the engine
  persists." A raw create-write is the [rejected alternative](#alternatives-considered)
  below — it would dissolve the source/engine line and forfeit dedup, provenance,
  and containment in one move.
- **No plugin-chosen provenance.** The engine stamps `plugin:<name>`; the plugin's
  `source` field is a human label, not the trust-bearing stamp.
- **No mutation of other sources' rows.** `source:provide` lets a plugin
  *originate* `plugin:<name>` rows; it does not let it edit or delete a bank's
  rows. Annotating an existing transaction stays `labels:write` /
  `extensions:write`, exactly as today.
- **No new storage.** A plugin source reuses the `ImportBatch`, the poller engine,
  the provenance stamp, and the Sources surface — no new table, no new write path.
  (*Lean on storage, comprehensive on exposure.*)

## Consequences

**Gains**

- Plugins can finally close the loop from "read and annotate" to "ingest and
  originate" — an unsupported bank, a card export, a derived adjustment —
  **in-process**, without a first-party Go source in the binary or an external REST
  driver.
- Every load-bearing guarantee (dedup, events, rules, history, provenance) is
  **inherited from the engine**, written once; a plugin source is as contained as
  a built-in one.
- Plugin-originated rows are **attributable** (`plugin:<name>`) and
  **immutable to manual edit**, so the ledger stays honest about where data came
  from.
- Composes cleanly with the arc: a remote-pulling producer is
  [`net:fetch`](0002-plugin-network-capability.md) **+** `source:provide`,
  surfaced at the top [trust tier](0003-marketplace-trust-tiers.md), and is the
  mechanism [ADR 0004](0004-transaction-document-artifacts.md)'s "fetch + link"
  half could use to *create* a transaction it discovered, not merely annotate one.

**Costs & mitigations**

- **The most dangerous capability yet.** A producer puts rows in the ledger.
  *Mitigation:* it is contained by the engine (no raw write), stamped and
  attributable, top-tier and never auto-listed, admin-enabled, WASM-recommended —
  the same defense-in-depth the arc applies to egress, one notch higher.
- **Reactive producers risk feedback loops** — a row created by
  `OnTransactionCreate` could re-trigger `OnTransactionCreate`. *Mitigation:*
  idempotent `(source, external_id)` dedup gives every producer a fixed point, and
  the engine does **not** re-dispatch a plugin's *own* produced rows back into that
  same plugin's reactive hook; a runaway producer is still bounded by the
  `plugins.hook_timeout` and the per-plugin queue.
- **A plugin source can emit junk** (wrong amounts, bad dates). *Mitigation:* it is
  exactly as bad as a buggy built-in source and no worse — the engine validates and
  rejects malformed rows, dedup bounds duplicates, and provenance makes the
  offending plugin's rows findable and removable.
- **`OnUninstall` must clean produced rows.** Unlike labels/extensions, the rows
  *are* ledger entries. *Mitigation:* `plugin:<name>` provenance makes "delete this
  plugin's transactions" a precise query the
  [cleanup hook](../../features/plugins.md#uninstalling-the-cleanup-hook) can run;
  the registry gate can require it for `source:provide` plugins.

## Alternatives considered

- **A raw `transactions:create` host method** (`kasas.create_transaction(...)` that
  writes a row from the VM). The obvious shape, and the one ADR 0003 named.
  **Rejected:** it crosses the one line ingestion refuses to cross — it makes the
  plugin the *writer*, not a producer — and in doing so forfeits engine-owned
  dedup, the atomic persist, provenance stamping, and containment. The producer
  seam delivers the same user capability while keeping every one of those
  guarantees.
- **Keep creation core-only.** Status quo: only first-party Go sources and the
  manual API originate rows. **Rejected:** it permanently forecloses community
  ingestion sources and pushes every "kasas doesn't support my bank" case into an
  external process the operator must run and wire to the REST API — the exact
  friction the plugin system exists to remove.
- **A general "manual-write" capability** that lets a plugin call the
  [manual-entry](../../features/manual-entry.md) API in-process (creating
  `source='manual'` rows). **Rejected:** it would let a plugin's rows masquerade as
  hand-entered and become silently user-editable, erasing attribution; producer
  rows are honestly stamped `plugin:<name>` and own their lifecycle through dedup,
  not manual edits.
- **A new `producer` archetype distinct from the source SDK.** **Rejected:**
  unnecessary — a plugin producer *is* a [`pull`/`enrichment`](../ingestion.md#archetypes-not-providers)
  source expressed in a sandboxed runtime; it reuses `ImportBatch`, the poller, and
  the Sources surface rather than inventing a parallel path.
