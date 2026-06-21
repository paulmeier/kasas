# ADR 0007 — Soft-delete: reversible transaction hiding

- **Status:** Proposed (scoping only — not yet built)
- **Date:** 2026-06-21
- **Related:** [Data Model](../data-model.md),
  [Manual entry](../../features/manual-entry.md),
  [Event Stream](../../features/event-stream.md),
  [Transaction History](../../features/transaction-history.md),
  [Transaction Relationships](../../features/transaction-relationships.md),
  [Plugins](../../features/plugins.md) (the `OnTransactionDelete` hook, whose
  contract this ADR shifts),
  [ADR 0005](0005-plugin-originated-transactions.md) (uninstall purge — the
  teardown path this ADR preserves as a hard delete)

## Context

Deleting a transaction today is a **hard delete**, and it is the one operation in
kasas that erases rather than appends. `ledger.DeleteTransactionTx` does three
destructive things inside one transaction:

1. **strips inbound relationship edges** — edges are stored only on the *subject*
   (outbound) side as a JSON column, so an edge pointing *at* the deleted row lives
   on a *different* row; the delete scans every other transaction and removes the
   danglers, emitting `relationship.removed` for each;
2. **deletes the row's history versions** (`transaction_versions` has no FK
   cascade), by hand;
3. **deletes the row** and emits `transaction.deleted`.

Two things are wrong with this for a system that calls itself a ledger.

**It violates the append-only spine.** The [event stream](../../features/event-stream.md),
the immutable [history](../../features/transaction-history.md), and
[provenance](../../features/transaction-provenance.md) are all built on "nothing is
erased; state moves forward." Hard delete is the exception that erases the subject
of all three. The by-hand inbound-edge strip and version deletion exist *only*
because the row is about to vanish — they are the fragile cost of erasure.

**It is available exactly where it matters least, and unavailable where it matters
most.** Only **manual** transactions can be deleted; **synced** rows are read-only
(`409`). But a manual row is user-created and usually deleted seconds after a typo —
the "keep it for history" argument is weak there. The argument is *strongest* for
synced rows, the actual record of money that moved — and those can't be touched at
all. So a user who wants to pull **noise** out of their views and analysis
(internal transfers that double-count, a refund netted against its purchase, a
duplicate the institution posted twice) has no mechanism: they can't hard-delete a
synced row, and they wouldn't want to erase the record even if they could.

The reframe that decides this ADR: **the valuable capability is not "erase" — it is
"exclude from views and analysis, reversibly, while keeping the ledger record."**
Erasure is a narrow teardown need (uninstall a source, delete an account); exclusion
is the everyday need, and it is missing.

A deliberately-rejected narrowing is worth naming up front, because it is the
tempting one: *soft-delete only the manual rows we already delete.* That pays the
full read-path filtering cost below but delivers undo only on the data where
integrity matters least — mechanism without payoff. This ADR is the broader version
or nothing (see Alternatives).

## Decision

Add **soft-delete**: a transaction can be marked deleted (hidden from views and
analysis, reversibly) instead of erased, on **any** row regardless of source. Hard
deletion survives only as a **teardown** primitive.

### 1. Storage: one nullable `deleted_at` column — a user-owned annotation

```sql
ALTER TABLE transactions ADD COLUMN deleted_at INTEGER;  -- NULL = live; unix seconds = hidden
```

One nullable column, not a status table and not a reserved label/extension (which
can't be indexed or filtered cleanly across the SQLite↔Postgres split and would
pollute the user's label vocabulary). `NULL` means live; a timestamp means hidden,
and records *when*.

The load-bearing framing: **`deleted_at` is a user-owned annotation, parallel to
`labels`, `extensions`, and `relationships`** — all per-transaction columns the
*user* writes and the *poller never does*. That single fact resolves two things at
once:

- **It is writable on synced rows without breaking the read-only model.** The
  `409` on a synced row guards its *synced fields* (amount, date, payee — the
  institution owns those). It has never guarded user annotations: you can already
  label, extend, and relate a synced transaction. `deleted_at` joins that set.
  "Hide a synced transaction" is mechanically identical to "label a synced
  transaction."
- **Re-sync cannot resurrect a hidden row.** Every annotation column carries the
  same migration note — *"never written by the poller, so re-syncs"* leave it
  intact. `deleted_at` inherits that invariant: the poller's upsert must not touch
  it, so a hidden synced row **stays hidden across re-sync**. The frightening part
  of this design (resurrection) is solved by an existing, proven pattern, not new
  machinery.

### 2. Reads hide tombstones by default; an opt-in powers Trash + Restore

Default reads gain `WHERE deleted_at IS NULL`. A bounded set of query sites is
affected — the transaction list, list-by-account, the search feed, and the plugin
host's reads — each in **both dialect dirs**. An explicit opt-in
(`?deleted=only` / `?include_deleted=1`, exact surface TBD) powers a **Trash view**
and the **Restore** action; a single-row `GET` may fetch a hidden row so it can be
viewed and restored.

This filter is the ADR's real, permanent cost: it is cross-cutting (there is no
single read chokepoint), and the failure mode is a *missed* site leaking a hidden
row into a view or a sum. Mitigation is discipline, not architecture — keep the
predicate uniform, and pin it with tests at every surface (REST list/search, MCP
`list_transactions`/`search_transactions`, dashboard, plugin host `Search`).

### 3. History and events append, never erase

Soft-delete is a forward state transition, so it rides the immutability spine
instead of fighting it:

- **History:** append a new `deleted` version (a snapshot, like every other change
  kind) — do **not** delete `transaction_versions`. Restore appends a `restored`
  version. The lineage of a hidden-then-restored row is fully legible.
- **Events:** soft-delete emits `transaction.deleted` (the consumer-facing meaning
  "it left the active ledger" is preserved — the row simply still exists);
  **restore** emits a new `transaction.restored`. Hard teardown (Decision 6) emits
  a **distinct** `transaction.purged`, so a consumer replaying the stream can tell
  "hidden, restorable" from "gone for good" without inspecting the row.
- **Relationships:** stop stripping inbound edges — the row still exists, so nothing
  dangles. The whole fragile scan-and-strip from today's delete simply **disappears**
  on the soft path. What replaces it is a *display* question, not a data-integrity
  one (Decision 5).

### 4. Balances are untouched — exclusion affects views, not reported balances

Account balances are **static** (bank-reported `balance` + `balance_date`, never
derived from transactions), so hiding a transaction can never make a reported
balance lie. This yields a clean, honest user story:

> Excluding a transaction changes **what you look at** and **what the analysis
> counts** — never **what your bank says your balance is**.

Because the financial-analysis skills and subagent read through `list`/`search`,
the Decision-2 default filter excludes hidden rows from spending, cash-flow, and
net-worth analysis automatically, everywhere, with no analysis-specific code.

### 5. Relationships into hidden rows: mark, don't silently suppress

With inbound edges no longer stripped, an edge can point at a hidden row (A "refund
of" B, then B is hidden). The call: **keep the edge and mark the target as deleted**
in the UI (a "deleted" chip), rather than silently suppressing it — suppression
hides real lineage and is itself a kind of erasure. In default views that hide
deleted rows, an edge *into* a hidden row renders with that marker; the Trash view
shows the row itself. *(Open: whether a relationship `list` endpoint dereferences a
hidden target or only flags it.)*

### 6. Hard delete survives as a teardown primitive

Erasure does not go away — it narrows to where a row genuinely must vanish:

- **Source/plugin uninstall** (`ledger.PurgeSource`, [ADR 0005](0005-plugin-originated-transactions.md))
  — an uninstalled source must not leave orphaned tombstones forever; its rows
  vanish, the analog of disconnecting a bank.
- **Manual account deletion** — deleting the account removes its transactions.

These keep the existing destructive cleanup (versions + inbound edges) and emit
`transaction.purged`. So two delete semantics coexist, clearly separated: **soft
(`transaction.deleted`, restorable)** is the user's everyday action; **hard
(`transaction.purged`, permanent)** is system teardown.

### 7. Surfaces: full parity, familiar verb

Following kasas's expose-across-all-surfaces convention, soft-delete / restore /
Trash land across **REST + MCP + dashboard** together. The user-facing verb stays **Delete**
(implemented as Trash + Restore — reversible by default); **synced rows gain the
same Delete action**, where they returned `409` before, now meaning "hide, keep the
record." Soft-delete and restore ride the existing **write tier** used for
annotations and manual edits (`read_write`), not admin — they write a user-owned
column, exactly like labels.

### 8. Plugin hook contract shift

The `OnTransactionDelete` hook (added alongside this ADR on the same branch)
currently documents *"the row is already gone — react from the snapshot."* Under
soft-delete the row **still exists** when the hook fires, so `get_transaction(id)`
now succeeds and host writes against the id work. The hook fires on **soft**-delete;
its doc note relaxes accordingly. A future `OnTransactionRestore` (and possibly
`OnTransactionPurge`) is a natural follow-up, deferred here.

## What this ADR deliberately does **not** do

- **No partial/field-level hiding.** Exclusion is whole-transaction; a row is live
  or hidden.
- **No removal of hard delete.** Teardown (Decision 6) still erases; this is not a
  ban on deletion, it is a second, safer default.
- **No auto-exclusion rules.** kasas does not *decide* what is noise — no
  auto-hiding of detected transfers. That is a [rules](../../features/rules.md)/
  labels job; soft-delete is the manual mechanism a future rule action could drive.
- **No recycle-bin retention policy.** Tombstones persist until restored or
  hard-purged; "auto-empty Trash after N days" is a deliberate later decision, not
  assumed here.
- **No change to reported account balances.** They remain static and bank-sourced
  (Decision 4).
- **No reversal/void accounting.** This hides a row; it does not post an offsetting
  entry (see Alternatives).

## Consequences

- Delete becomes **append-only and reversible**; the fragile inbound-edge strip and
  by-hand version deletion vanish from the everyday path (they remain only on the
  rare teardown path).
- **Synced rows become hide-able for the first time** — the actually-valuable
  capability: remove transfers/dupes/noise from views and analysis *without* losing
  the record and *without* the source re-creating them.
- A **permanent read-path tax**: every default read must filter, in two dialects;
  the risk is a missed site leaking a hidden row. Bounded and test-pinned, but real
  and forever.
- **Two delete semantics** now exist (`transaction.deleted` soft vs
  `transaction.purged` hard). Stream consumers, docs, and the dashboard must keep
  them distinct.
- The **`OnTransactionDelete` contract shifts** (row present, not gone).
- One nullable column; no new table, no FKs, additive migration — lean storage,
  reusing the existing per-transaction annotation pattern rather than new schema
  weight.

## Alternatives considered

- **Do nothing — keep hard delete (Option A).** Defensible for manual typos: those
  rows are user-owned and fresh, and the real ledger (synced rows) is already
  immutable. But it forfeits the valuable capability (synced-noise exclusion),
  keeps the append-only violation, and keeps the fragile teardown on the common
  path. Acceptable, not good.
- **Soft-delete manual rows only (Option C).** The tempting narrowing. Pays the
  full Decision-2 read-path tax but delivers undo only where integrity matters
  least; synced noise stays unaddressable. Rejected as mechanism-without-payoff —
  if the filter cost is paid, it must buy the synced-row capability.
- **Accounting void / reversal entry.** Post an offsetting transaction instead of
  hiding. Correct for a double-entry ledger, but kasas is single-entry, and the
  user intent is "stop showing me this," not "reverse a posting." Heavier and the
  wrong mental model; rejected.
- **Status enum column or a `transaction_status` table.** Heavier than one nullable
  timestamp for a binary live/hidden state that also wants to record *when*.
  `deleted_at` is the lean encoding; revisit only if more states than
  live/hidden/purged ever appear.
- **Reserved label or extension (e.g. `kasas:deleted`).** Zero schema change, but
  can't be indexed or filtered cleanly across SQLite/Postgres, pollutes the user's
  label vocabulary, and muddies the annotation it sits beside. A first-class column
  is right.
