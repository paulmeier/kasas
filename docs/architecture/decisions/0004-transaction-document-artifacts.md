# ADR 0004 — Document & artifact association for transactions

- **Status:** Proposed
- **Date:** 2026-06-10
- **Related:** [Schema Extensions](../../features/schema-extensions.md), [Transaction Relationships](../../features/transaction-relationships.md), [Philosophy](../philosophy.md), [ADR 0002](0002-plugin-network-capability.md)

## Context

A recurring user need: a transaction is a single opaque charge — *"TARGET
$63.40"* — but the user has the **receipt**, and wants either the scanned
document attached to the transaction or its **line-item itemization** (paper
towels $4.99, detergent $12.00, …) recorded against it.

kasas has no concept of this today. The
[transaction model](../data-model.md) has no attachment field; there is **no
blob storage, no upload endpoint, no document type** anywhere in the core. The
[philosophy](../philosophy.md) is explicit about why: *"Lean on storage,
comprehensive on exposure. Reuse existing tables and a JSON column before adding
schema."* kasas owns the **ledger**, not a document store.

Three distinct sub-problems hide inside "attach the receipt," and conflating
them produces the wrong design:

1. **The blob** — the receipt's PDF/JPEG bytes.
2. **The association** — the link from those bytes to `txn:target-…`.
3. **The itemization** — the structured line items, which are *ledger-adjacent
   data*, not a scanned image.

Only #1 is a genuinely new concern. The platform already has primitives for #2
and #3.

## Decision

Keep the ledger lean: kasas **stores references and structured data, not
bytes**. The bytes live in a system built for documents
([paperless-ngx](https://docs.paperless-ngx.com/) is the motivating example, but
S3, Google Drive, or any DMS fit the same shape). This is a direct application
of *lean on storage, comprehensive on exposure*.

### 1. The blob — referenced, not stored

A receipt is recorded as a reference under a reserved
[schema-extension](../../features/schema-extensions.md) namespace, never as
ledger-owned bytes:

```jsonc
// extension: documents.receipt on txn:target-…
{
  "system": "paperless-ngx",
  "doc_id": 4821,
  "url": "https://paperless.lan/documents/4821",
  "sha256": "…",            // integrity of the referenced bytes
  "mime": "application/pdf",
  "added": "2026-06-10"
}
```

Extensions are the right home: they are
[arbitrary namespaced JSON that survives every re-sync](../../features/schema-extensions.md)
and is queryable across REST/MCP/dashboard. The ledger stays a ledger; the
document store stays the source of truth for bytes, OCR, and retention.

### 2. The association — extension now, relationship later

The reference in #1 *is* the association for the common one-receipt case. If a
richer many-to-many model is later wanted (one receipt spanning several charges,
several artifacts on one charge), introduce an **artifact** as a first-class
node and reuse [transaction relationships](../../features/transaction-relationships.md)
— extending the relationship target beyond txn→txn to txn→artifact — rather than
overloading extensions. This ADR does **not** commit to that; it reserves the
direction so the cheap path (extensions) does not foreclose it.

### 3. The itemization — its own extension namespace

Line items are structured ledger-adjacent data and have nothing to do with where
a scan lives. They go in their own namespace:

```jsonc
// extension: itemization.lines on txn:target-…
{
  "lines": [
    { "desc": "Paper towels", "amount": "4.99", "qty": 1 },
    { "desc": "Detergent",    "amount": "12.00", "qty": 1 }
  ],
  "source": "paperless-ocr"   // or "manual", "merchant-api", …
}
```

Amounts are [decimal strings](../philosophy.md), like every amount in kasas. A
plugin can validate `sum(lines) == txn.amount` entirely in-sandbox — no new
capability needed.

### 4. The mechanism — who moves the data

This is the crux, and it is **constrained by the plugin trust model**. A plugin
runs [sealed](../../features/plugins.md#sandbox-limits-v1) and is **read +
annotate**: it can `set_extension`, but it cannot reach paperless-ngx, and there
is no `transactions:create`. So the work splits:

- **Fetch + link** (needs the network) is done by either:
    - an **external connector** today — a small process that watches
      paperless-ngx, matches a document to a transaction (date + merchant +
      amount, or a correlation token), and writes the reference back via the
      [REST API](../../interfaces/rest-api.md) (`PATCH` the `documents.receipt`
      extension); **or**
    - a **`net:fetch` plugin** once [ADR 0002](0002-plugin-network-capability.md)
      lands — the same logic in-process, with `paperless.lan` on its declared
      allowlist, no separate service to run.
- **Present** (sealed, no network) is a plugin **dashboard page**: render the
  receipt link/preview and the itemization table from the `documents.*` /
  `itemization.*` extensions, and flag transactions that are *missing* a receipt
  via `OnSyncComplete`.

The split — **connector/`net:fetch` does I/O and writes; a sealed plugin
presents** — is the only shape compatible with the sandbox, and it composes
cleanly with the rest of the arc: [ADR 0002](0002-plugin-network-capability.md)
collapses the two halves into one installable plugin.

### What this ADR deliberately does **not** do

- **No blob storage in core**, no upload endpoint, no `artifacts` table. That
  remains an app-layer concern unless and until a future ADR argues receipts are
  *core ledger data*.
- **No `transactions:create` capability.** Whether plugins may *originate*
  transactions (vs. that staying a core [source](../ingestion.md)) is a separate,
  larger decision and is out of scope here.

## Consequences

**Gains**

- Receipts and itemization ship with **zero new core storage** — extensions and
  (optionally) relationships already exist and already span every surface.
- The document store owns what it is good at: OCR, full-text search, retention,
  a real document UI. kasas does not reinvent any of it.
- Reserved, namespaced contracts (`documents.*`, `itemization.*`) give plugins
  and apps a stable shape to read and write against immediately.

**Costs & mitigations**

- **Soft dependency on an external system** to resolve a link. *Mitigation:* the
  ledger stores a self-describing reference (system, id, url, hash); if the DMS
  is down only the *preview* is unavailable, the ledger is unaffected.
- **No bytes means no kasas-side guarantee the document still exists.**
  *Mitigation:* the stored `sha256` lets any reader verify integrity when it does
  fetch; a future connector can re-validate and flag dangling references.
- **The fetch half needs network**, which the sandbox forbids. *Mitigation:* the
  external-connector path works **today**; [ADR 0002](0002-plugin-network-capability.md)
  is what makes the single-plugin path possible.

## Alternatives considered

- **First-class artifacts in core** (an `artifacts` table, a multipart upload
  endpoint, a blob backend → local dir, then S3/GCS). Genuinely useful and the
  right call *if* receipts become a headline promise of kasas. Rejected for now:
  it contradicts the lean-ledger thesis and pulls a storage backend, GC, dedup,
  size/content-type handling, and thumbnailing into the core. Left open for a
  future ADR to revisit if demand warrants.
- **Store bytes inside an extension (base64).** Trivial, no external system.
  Rejected: extensions are capped (~8 KB/value) and are metadata, not a blob
  store — this would abuse the primitive and bloat the ledger.
- **Itemization folded into `documents.receipt`.** Fewer namespaces. Rejected:
  itemization is useful with no scan at all (manual or merchant-API sourced), so
  it earns its own namespace independent of where bytes live.
