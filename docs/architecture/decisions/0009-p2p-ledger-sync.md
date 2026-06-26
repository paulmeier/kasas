# ADR 0009 — Selective peer-to-peer ledger sharing

- **Status:** Proposed (scoping only — not yet built)
- **Date:** 2026-06-26
- **Related:** [ADR 0008](0008-inbound-webhook-source.md) (the inbound-webhook
  source + the HMAC scheme this generalizes to a peer — the direct parent),
  [Webhooks](../../features/webhooks.md) (the *outbound* dispatcher and `whsec_`
  signing reused symmetrically),
  [Ingestion architecture](../ingestion.md) (archetypes + the source contract a
  peer rides),
  [Transaction Provenance](../../features/transaction-provenance.md) (the
  `transactions.source` stamp this ADR extends to `peer:<id>`),
  [Labels](../../features/labels.md) (the key:value tag mechanism that carries the
  `shared_by` attribution),
  [Search](../../features/search.md) (the one query language a share selector is
  written in),
  [Rules](../../features/rules.md) (a rule's `Query` *is* a selector, so "share a
  rule" is free),
  [Event Stream](../../features/event-stream.md),
  [ADR 0002](0002-plugin-network-capability.md) (the netfetch SSRF gate that
  refuses `100.64.0.0/10` — the Tailscale interaction this ADR must reconcile),
  [Data Model](../data-model.md)

## Context

kasas is already a **source-agnostic ledger**: ingestion models *how data arrives*,
the engine owns persist/dedup/events/rules/history, and sources never touch the DB.
[ADR 0008](0008-inbound-webhook-source.md) proved the load-bearing fact this ADR
builds on: **a kasas instance's outbound webhook can feed another kasas instance's
inbound-webhook source unchanged.** One kasas can already shove data into another,
one-way, authenticated by a symmetric HMAC.

What is missing is everything that makes that a *relationship* rather than a fire
hose:

- **Selectivity (R1).** "Feed another kasas" today means "feed it whatever the
  sender's outbound dispatcher emits." There is no way to say *share exactly these
  transactions / these accounts / everything tagged `category:gift` / everything my
  "shared with dad" rule matches.*
- **Subscription (R2).** There is no notion of a *subscriber* — a ledger that opts
  in to a curated feed of another's data and stays in sync as new rows match.
- **Peer-to-peer, no mandatory central server (R3).** kasas is self-hosted, usually
  on a homelab box, frequently reachable only across a Tailscale tailnet. Any sharing
  design that *requires* a central directory or relay violates the ethos.
- **Provenance without corruption (R4).** When Alice's transaction lands in Bob's
  ledger, Bob must keep *who originated it* **and** record *which ledger it arrived
  from* — without the two colliding under the `id`-keyed dedup, and without a row Bob
  received being re-exportable as if Bob authored it.

The reframe that decides this ADR: **a "share" is a saved
[search](../../features/search.md) query, and a "subscription" is just adding a
[source](../ingestion.md).** R1's four selection modes — a set of transactions,
accounts, labels/tags, a rule — all collapse onto **one search-query string**,
because the grammar already parses `id:`, `account:`, `label:k=v`, and a rule's
`Query` field *is* such a string. And "subscribe to a peer" is structurally "add a
`peer` [`Puller`](../ingestion.md) source pointed at that share's feed," so the
engine's persist/dedup/events/rules/history come **for free**. The hard parts are not
moving bytes — they are **trust-establishment / discovery** (two homelab boxes behind
NAT) and **provenance-without-loops**.

## Decision

Build **selective peer sharing** as **one pluggable share contract** layered over the
existing source seam and the [ADR 0008](0008-inbound-webhook-source.md) HMAC. The wire
only ever carries *(a stored share referenced by id)* or *(a signed `ImportBatch`)*,
authenticated by the `whsec_` HMAC over `"<timestamp>.<body>"` (`hmac.Equal`, ±5-min
freshness). **Pull is the primary topology; push is a supported secondary; a signed
capability-link is the bootstrap.**

### 1. The share definition: one search query, LIVE by default (R1)

A **Share** is `{id, peer_id, name, mode, selector, rule_id, enabled,
last_evaluated_at, last_status}`. Its `selector` is **one
[search](../../features/search.md) query string** — never four bespoke selector
types — because the grammar already expresses every R1 mode:

| R1 mode | selector |
|---|---|
| a set of transactions | `id:abc OR id:def` |
| accounts (+ their future txns) | `acct_id:8f3a OR acct_id:9b21` |
| labels / tags | `label:category=food` (or the `category:food` shorthand) |
| a rule | the rule's existing `Query`, referenced by `rule_id` |

"**Share a rule**" therefore needs **zero** new machinery: store `rule_id`, resolve
`rules.Get(id).Query` at evaluation (tracking the rule as it evolves), with the stored
`selector` as a snapshot fallback if the rule is later deleted.

A share is **`live` by default** — R2's "subscribed to their ledger" means an *ongoing*
relationship, so a standing query that keeps matching new rows is the correct default.
**`static`** is offered for "share exactly these N transactions, frozen": it is
implemented as a `live` share whose selector is the materialized id-disjunction
(`id:a OR id:b ...`), so there is exactly **one evaluator**, not two. The *same* compiled
query is reused by the pull feed (over the full ledger), the push dispatcher (against
one changed txn), and backfill — `search.Parse` + `q.Match`, the contract both
topologies read.

**Grammar prerequisite — `acct_id:` (a 3-line addition).** Today the `account:` field
matches the **mutable** `AccountName` by **substring** (so `account:check` also matches
"Checking Plus", and a rename silently changes the matched set). For "share this account
and its future transactions" to be stable, add a reserved `acct_id:` field doing **exact**
match on `Record.AccountID` (already populated everywhere). Rename-proof, exact, and
future-inclusive. This benefits search/rules app-wide, not just sharing.

### 2. Subscription topology: pull primary, push secondary, link to bootstrap (R2/R3)

**PRIMARY — pull, subscriber-initiated.** The publisher serves
`GET /api/v1/peer/feed/{share}?cursor=&since=&limit=` (open, HMAC-authed, **not**
dashboard-token-gated, inert-until-secret, 1 MiB cap, per-token rate-limited),
returning a paginated `ImportBatch` + `next_cursor`. A subscription is **one
[`MultiCredentialed`](../ingestion.md) entry** on a new first-party `peer` source —
`{peerID, baseURL, share-slug, feed-token}`, the exact Teller/Plaid multi-fan-out
idiom (list masked / append / remove-by-id). The engine schedules it as a normal
`Puller` (`Fetch(ctx, since, cursor)`), persists+passes back the cursor, and
dedup/events/rules/history come free.

Pull is primary because it makes the **subscriber** the consenting party (the true
reading of R2 — a subscriber cannot be force-fed) and it solves the most common
homelab NAT shape: a **fully-firewalled subscriber works** (only outbound GETs), and
the publisher's reachability rides whatever Tailscale/tunnel it already runs.

**SECONDARY — push, for low latency.** Once paired, the publisher's outbound
dispatcher (a thin specialization of the [webhooks](../../features/webhooks.md)
`Dispatcher` riding the events bus) HMAC-signs and POSTs a one-txn `ImportBatch` to
the subscriber's inbound `peer` [`Receiver`](../ingestion.md)
(`POST /api/v1/sources/peer/ingest`, the generalized
[ADR 0008](0008-inbound-webhook-source.md) endpoint); best-effort with retry/backoff,
the durable catch-up being an idempotent pull/backfill re-run. **Push requires the
receiver reachable; pull requires the publisher reachable** — offering both covers
either firewall topology. The wire payload and HMAC trust layer are **identical**
across pull and push, which is what makes the transport pluggable.

**Bidirectional = two independent one-way relationships**, each side defining its own
share targeting the other. There is no CRDT merge and no single "mutual subscribe" verb.

### 3. Provenance & origin-tagging — three redundant encodings (R4)

On the **receiving** ledger, per shared transaction — honoring mig-00011 ("`source`
records the ingestion *path* and cannot be reconstructed after the fact") and the
`id`-keyed dedup:

1. **`transactions.source = "peer:<senderLedgerId>"`** — per-sender, **not** a flat
   `peer`, **not** the original originator. The literal ingestion path into *this*
   ledger is the peer transfer, so `source` must name it. `source` is the **provenance
   stamp and loop-guard key**; it does **not** itself partition the dedup namespace
   (dedup is physically `ON CONFLICT (id)` on the primary key). Disjointness is
   delivered by claim 2's namespaced id: two peers forwarding the same upstream id land
   as two correct rows, and a peer row can never clobber the receiver's own `simplefin`
   row, because each gets a distinct `peer:<id>:<origExtId>` id.
2. **`ImportTxn.ExternalID = "peer:<senderLedgerId>:<originalExternalId>"`** — there is
   **no separate `external_id` column**; the source-namespaced external id *becomes* the
   transaction `id` (PK), exactly as the webhook source sets `t.ExternalID` → persisted
   as `InsertTransaction.ID`. Idempotent on re-delivery.
3. **Original originator preserved** in the existing `extensions` column under reserved
   namespace **`kasas.peer`** =
   `{origin_source, origin_external_id, sender_ledger_id, shared_at}`. Extensions is the
   only re-sync-immutable home once `source` is taken by the peer stamp.
4. **The human-facing R4 tag** = [label](../../features/labels.md)
   **`shared_by:<senderLedgerId>`** (labels *are* the key:value tag mechanism R4 asks
   for; a ≤50-char fingerprint fits), with optional `origin_source:<original>` so the
   originator is queryable too.

So R4 is honored **three ways**: `source` (machine/dedup truth), `extension` (the
structured preserved originator), `label` (the human tag).

**Anti-forgery (mandatory, with a v1 honesty caveat).** `senderLedgerId` is **derived
from which per-peer secret verified the delivery**, never trusted from the body — a
body-claimed id that disagrees is rejected. But the binding is only as strong as the
shared secret: in v1, `shared_by:<id>` is an **operator-chosen label proving *which
secret* signed, not a cryptographically proven identity** — a leaked secret lets a third
party impersonate that peer until rotation (Decision 7). The ed25519-fingerprint id
(adopted as the *format* now) is the path to proven, non-repudiable attribution.

**Shared accounts** are peer-namespaced (`account.source = "peer:<id>"`), read-only, and
**excluded from net-worth by default**. Balances/totals never cross the wire, and that is
**enforced at a named seam, not asserted**: because the payload is an `ImportBatch` whose
`ImportAccount` carries `Balance`/`BalanceDate`, the publisher's feed builder **zeroes
those fields** before signing, and the receiving `peer` source **ignores any non-zero
peer-account balance** — belt-and-suspenders so a shared account never ships its live
balance.

> **Rejected:** the pull-stance instinct to keep `source = originator` and namespace only
> the id. It violates mig-00011 (on the receiver the path *is* the peer transfer), makes
> "is this mine or foreign?" unanswerable, and breaks the loop-guard, which keys off a
> `source:peer:` match. We graft its correct half — namespacing the id — and do both.

### 4. Loop / echo / re-share prevention (layers 1+2 mandatory)

1. **Dedup backstop (free).** The engine dedups on the row `id`; a re-pushed/echoed row
   carries the identical `peer:<id>:<origExtId>` id and no-ops. (This is why backfill is
   idempotent.)
2. **Origin-not-mine guard (the structural fix).** Every share's effective selector is
   server-side AND-ed with `NOT (source:peer: OR source:webhook:)` so a ledger may only
   **export rows it authored** — killing A→B→A echo and forbidding transitive B→C
   re-share. (The new `source` field is **substring** match, like `description`/`payee`,
   so the bare `peer:` prefix matches any `peer:<id>`; no glob operator exists or is
   added.) **You may only share what you authored**; a peer-sourced row is categorically
   ineligible for re-sharing.
3. **Hop-path (optional hardening).** A derived `[]ledgerId` carried in
   `kasas.peer.hop_path`; the receiver refuses any row whose path already contains its own
   id (belt-and-suspenders against id-rewriting).
4. **Content-addressed seen-set (federation only).** Meaningful only in the deferred
   federation mode.

**Two engine prerequisites — verified in-tree, missed by every survey stance:**

- **GAP A — search has no `source` field.** `predFromTerm` enumerates
  `description/payee/memo/account/id/amount/date/synced/pending/label/ext/rel/related`
  only, and `search.Record` has no `Source`. As written, `source:peer:` parses as a
  *label* shorthand and the loop-guard **silently fails open**. Add a first-class
  **substring** `source` field to `Record` + a `case "source"` in `predFromTerm` (≈6
  lines), populated in every record builder. Enables real `source:peer:alice` queries
  **and** makes the guard expressible.
- **GAP A, defensive.** Because an operator could author a selector that evades the
  clause, the guard MUST **also** be enforced in Go on the batch-stamped `source` before
  serving (AST injection after parse + a Go re-check), not solely via the query.
- **GAP B — `ImportTxn.Extensions`/`Labels` are not persisted.** The persister inserts
  `"{}","{}"` and never reads `ImportTxn.Extensions`; the only path to born-row
  labels/extensions is rule auto-labeling. Extend the persister to fold
  `ImportTxn.Extensions` and a **new** `ImportTxn.Labels` into the born row **before** the
  `transaction.created` snapshot, so the peer source can stamp `shared_by` / `kasas.peer`
  structurally (not via a deletable system rule).

**Compromised-peer teardown** = `DELETE WHERE source='peer:<id>'` → `peer.purged` event
(mirroring plugin-source auto-purge-on-uninstall, [ADR 0005](0005-plugin-originated-transactions.md)).

### 5. Discovery & transport: one HMAC payload, several substrates (R5)

There is **no central discovery server** (R3) — a deliberate non-feature for a private
single-user ledger; global discoverability is a *misfeature*. Establishment is
out-of-band.

- **Establishment (the bootstrap, R5c)** = a copy-pasteable, QR-encodable **feed-invite**
  — `base64url({baseURL, share-slug, fingerprint, pairToken})` minted via
  `webhooks.GenerateSecret()` — handed out-of-band (Signal/paste/QR). The subscriber pastes
  it to create the `peer` credential entry. This **is** the capability-link of the
  alternatives matrix, used as the relationship primitive and the simplest one-way share.
  **The default invite is single-use and short-TTL**: it carries a `pairToken`, not the
  long-lived `feedToken`, and the subscriber redeems it at `POST /api/v1/sources/peer/pair`
  to receive the actual feed token over the authenticated channel — so a leaked or
  shoulder-surfed invite expires/burns rather than handing out a standing exfiltration
  credential. A raw-`feedToken` invite remains available for the throwaway one-shot case but
  is **not** the recommended default.
- **Recommended substrate** for two homelab/Unraid instances = **HTTPS over Tailscale
  MagicDNS** (stable `peer-ledger.tailnet.ts.net` name, WireGuard hole-punch + DERP relay,
  zero port-forwarding). Plain HTTPS works for publicly-reachable peers; a shared-folder
  drop (riding the existing Google-Drive file source + HMAC) is the async double-NAT
  fallback.
- **SSRF reconciliation ([ADR 0002](0002-plugin-network-capability.md)) — mandatory.** The
  `peer` source is **first-party** dialing an **operator-typed** host (same trust class as
  SimpleFIN reaching a bank), so it does **not** ride the plugin `net:fetch` gate (which
  refuses `100.64.0.0/10` by default — exactly the Tailscale case). But the gate's
  **validate-then-dial logic must be extracted into a shared, injectable helper**, not
  left plugin-only: the `peer` client reuses that helper so it gets the **same DNS-pin
  *at the dial*** (closing the DNS-rebinding hole where a first-party client re-resolves a
  validated name to an internal IP), redirect re-validation, https-only, and
  timeout/size caps. **Not** a blanket bypass: a **dedicated per-peer egress allowance** —
  the exact **host *and* port** granted when the operator added the peer — is the only
  address it may dial. The CGNAT refusal is lifted **for that granted host:port only**,
  stored in the peers store, **never** widening `net_grants`.

The full method comparison (R5) is below.

### 6. Storage — lean-first

**Publisher side: one new table `peer_shares`** (migration 00018, multi-dialect sqlc,
ASCII-only SQL comments to dodge the em-dash bug):

```sql
CREATE TABLE peer_shares (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,  -- BIGSERIAL on pg
    peer_id     INTEGER NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    mode        TEXT NOT NULL DEFAULT 'live',        -- live | static
    selector    TEXT NOT NULL,                       -- a search query string
    rule_id     INTEGER,                             -- nullable reference to rules(id)
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    last_evaluated_at INTEGER,
    last_status TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX idx_peer_shares_peer ON peer_shares (peer_id);
```

A share earns a table (not a `rules`-row, not a settings scalar) because it is a
**listed, per-peer-revocable relational entity** with `mode`/`peer_id`/health columns a
rule lacks — the same precedent that gave `rules` and `webhooks` their own tables. The
**feed token lives in `vault.SecretStore` keyed by the row** (not in the table) —
matching the [ADR 0008](0008-inbound-webhook-source.md) secret-in-vault precedent this
ADR cites for the subscriber side; the row holds only a non-secret token *id*. **Never
plaintext in a column.**

**Subscriber side: no new table.** Subscriptions are `MultiCredentialed` entries
(`{baseURL, slug, peerID, label}` + feed token in `vault.SecretStore`), the cursor rides
the engine's existing per-source cursor — the
[ADR 0008](0008-inbound-webhook-source.md) "secret in vault, no migration" precedent.
Receiver-side R4 needs **zero** new columns (rides `source` + labels + extensions).

> **Rejected (the lean variant, named):** reuse the `rules` table for shares. It lacks
> `peer_id`/`mode`/health and conflates "auto-label my own txns" with "expose rows to a
> peer." A new table is the honest call.

### 7. Security

- **Auth, bidirectional.** Reuse the [ADR 0008](0008-inbound-webhook-source.md) HMAC but
  make it cut both ways. **Pull** request auth signs a **canonical request**:
  `sha256=` + `webhooks.Sign(feedToken, ts, method + "\n" + path + "\n" + sortedQuery)` —
  `cursor`/`since` MUST be inside the signed bytes or a MITM can replay an old page or widen
  the window. **Push** delivery auth is unchanged (sign over `"<timestamp>.<body>"`). Verify
  with `hmac.Equal`, ±5-min freshness, **coarse 401** on every failure (no which-check-failed
  leak).
- **Attribution bound to auth.** `source=peer:<id>` / `shared_by:<id>` derive from *which
  secret verified*, never self-asserted (Decision 3).
- **Selective-disclosure hard invariant.** The subscriber references a share **by
  id/slug** and **never** passes a selector on the wire; the publisher evaluates
  `(storedSelector AND originGuard)` **server-side** and serves only matched rows' fields.
  No widening is possible.
- **Privacy / redaction — default-deny on every PII-bearing field.** Per-share policy
  `{include_memo:bool=false, include_extensions:allowlist=empty,
  include_labels:allowlist=empty}` — memo, extensions, **and labels** all **default OFF**.
  Labels are an **explicit per-share allowlist of keys**, *not* a boolean: one label-share
  must never ship a transaction's *entire* label set (a user's `person:`/`note:` labels are
  PII). The receiver-stamped `shared_by`/`origin_source` are added by the receiver and are
  never governed by the publisher's allowlist. Balances/totals never cross.
- **Malicious / leaked-token peer.** 1 MiB body cap + per-batch row/account caps +
  **per-peer/per-token token-bucket rate limit** (reuse the netfetch limiter) on the open
  feed/ingest endpoints (the in-Go matcher scans all txns per pull — a CPU-DoS vector for a
  leaked token). The receiver re-stamps `source`/the namespaced id, so a sender can never
  spoof provenance or collide a namespace. **Leaked-bearer exfiltration is the sharpest
  feed-side risk** (a stolen feed token silently drains the share until noticed): mitigated
  by (1) the **single-use, short-TTL pairing invite as the default bootstrap** so the
  long-lived feed token never travels the human channel (below), (2) **logging the puller's
  source identity/IP on every feed hit** into the in-memory egress ring (the netfetch
  pattern) so an unexpected puller is visible, and (3) rotate/revoke as the recourse.
- **Trust model (v1).** Shared-per-peer HMAC = **integrity, not identity**: a leaked secret
  allows impersonation until **rotate/revoke** (the only recourse, surfaced across all
  surfaces). **ed25519-TOFU-pinning is the named upgrade**, and its **fingerprint-as-peer-id
  format is adopted now** as the value of `peer:<id>`/`shared_by:<id>`, so attribution is
  upgradeable from *asserted* to *proven* **without a wire break**.
- **Tier placement.** *admin:* add/remove peer, reveal/rotate secret, share CRUD +
  redaction policy, egress-host grant (503 on an unsecured instance → the dashboard degrades
  to a calm empty+hint, per the disabled-subsystem-routes convention). *read_write:* trigger
  push/pull, follow/unfollow. *read:* list peers/shares masked, view status. *open
  (token-authed, untiered):* the feed/ingest endpoints.

### 8. Surfaces — full REST + MCP + dashboard parity

- **REST publisher (admin):** `GET/POST /api/v1/peer/shares`,
  `GET/PATCH/DELETE /api/v1/peer/shares/{slug}`, `POST .../rotate`, `GET .../invite` (mint
  the feed-invite + QR), `POST .../preview` (run the origin-guarded selector → a count +
  sample, so the operator sees **exactly** what they would expose before sharing). **Open
  feed:** `GET /api/v1/peer/feed/{share}`. **Push:** `POST .../push`,
  `POST /api/v1/peer/{id}/test`.
- **REST receiver:** `POST /api/v1/sources/peer/ingest` (push inbound) +
  `POST/GET/DELETE /api/v1/sources/peer/credentials` (subscribe via paste-invite / list
  masked / unsubscribe+auto-purge) + `POST /api/v1/sources/peer/sync`.
- **MCP:** `list/create/update/delete/rotate/preview/invite peer_share` (publisher) +
  `subscribe/list/unsubscribe peer` + `push_share` — 1:1 with REST.
- **Dashboard:** a **Peers** page (sibling to Sources/Events) — "Shares I publish" (create
  from the existing search box, rule picker, mode toggle, preview count, copy-invite+QR,
  rotate/revoke, last-served + subscriber label) and "Ledgers I follow" (masked
  subscription list, add-via-paste-invite, per-row remove, last-sync status), reusing the
  reveal-on-interaction pattern; degrades calmly to empty+hint on an unsecured instance.
- **Events** (noun.verb, ride the bus → SSE/outbound-webhooks/Events page free):
  `peer.added`, `peer.removed`, `peer.share.created/updated/deleted`, `peer.share.served`,
  `peer.subscribed`, `peer.unsubscribed`, `peer.purged`. A peer **pull rides the engine
  path**, so it still fires the canonical **`sync.completed`** (with `source=peer:<id>`) —
  consumers that already watch `sync.completed` need no change; `peer.synced` is **not**
  added (it would duplicate `sync.completed`). Shared rows still emit the normal
  `transaction.created` + `label.applied` (`shared_by`) + `extension.set` (`kasas.peer`).

## Methods to establish a sync (compared)

R5 asks for multiple methods to (a) discover peers, (b) share data, (c) establish a sync.
The chosen design offers a layered set over **one** HMAC payload contract; the rejected
options are named with one-line reasons.

| Method | Establishes sync by | Pros | Cons | Verdict |
|---|---|---|---|---|
| **Mutual-HMAC over HTTPS** (the spine) | Out-of-band base-URL + `whsec_` secret exchange; every payload signed via `webhooks.Sign` over `"<ts>.<body>"`, verified `hmac.Equal` + ±5-min freshness. | Zero new deps; reuses `Sign`/`GenerateSecret`/`Receiver` verbatim ([ADR 0008](0008-inbound-webhook-source.md) cashed); trivially lean; transport-agnostic. | No in-protocol discovery (out-of-band); shared secret = integrity not identity; best-effort delivery. | **ADOPT** — the universal trust+payload layer under both pull and push. |
| **Pull-feed-as-a-source** (PRIMARY) | Subscriber adds a `peer` `Puller` at `GET /peer/feed/{share}` (signed canonical request); engine schedules `Fetch`, persists+passes back cursor. | Subscribe == adding a source (truest R2); subscriber consents, needs **zero inbound reachability**; maximal engine reuse; self-throttles to the poller. | Poll-bound latency; the **publisher** must be reachable; an insert-stable cursor over a Go-filtered set is genuinely new engineering. | **ADOPT as PRIMARY** — the conceptual default subscription model. |
| **Push via outbound webhook** (SECONDARY) | Dispatcher rides the events bus, matches changed txns against each share, POSTs a signed one-txn `ImportBatch` to the peer's `Receiver`. | Low-latency; thin specialization of the shipped dispatcher + the ADR 0008 receiver; durable catch-up is a free idempotent re-pull. | Requires the **receiver** reachable; live per-event re-evaluation adds bus hot-path cost; best-effort, no durable queue. | **OFFER (secondary)** — enable once paired for a reachable receiver wanting immediacy. |
| **Signed capability-URL share-link** (bootstrap) | Mint an opaque, optionally-expiring signed URL `{baseURL, slug, token, peerId, fingerprint}`; handed out-of-band (paste/QR); recipient pastes it to create the peer credential. | Leanest establishment (a share is a string); recipient needs no inbound reachability; revocable; great QR/phone UX. | Bearer capability (whoever holds it gets the data — must expire/single-use over a confidential channel); weakest identity; pull-only. | **ADOPT** as the establishment method + simplest one-way share (a sub-mode of pull). |
| **HTTPS over Tailscale** (MagicDNS substrate) | Same HMAC-over-HTTPS, addressed to a `*.tailnet.ts.net` MagicDNS name; WireGuard hole-punch + DERP relay; tailnet WhoIs cross-verifies identity. | Solves home-NAT with **zero port-forwarding** (the Unraid reality); strong server-verifiable identity free; no central server kasas mandates. | Assumes a shared tailnet; **the [ADR 0002](0002-plugin-network-capability.md) gate refuses `100.64.0.0/10`** → needs a per-peer egress allowance; narrows audience vs plain HTTPS. | **OFFER** as the recommended NAT+identity substrate under HTTPS (carve the per-peer egress allowance). |
| **ed25519 signed-object federation** (Follow/inbox/outbox) | Per-ledger ed25519 keypairs; pairing TOFU-pins pubkeys; a subscription is a durable Follow; signed content-addressed objects pushed/pulled; libp2p/WebFinger as the discovery upgrade. | Cryptographic peer **identity** (`shared_by` becomes *unforgeable* — the one design proving R4); real discovery; Follow maps onto R2. | Materially heavier than kasas's lean ethos for a 2-instance sync; full key lifecycle; the libp2p win is deferred so v1 discovery is still out-of-band; lowest buildability. | **OFFER as DEFERRED** stronger-identity upgrade — adopt its fingerprint-as-peer-id NOW; build the full federation later (likely ADR 0010). |
| **Shared-folder / Google-Drive drop** (async store-and-forward) | Publisher writes a signed `ImportBatch` into a shared cloud folder; the subscriber's existing Drive file-import source ingests it after HMAC-verifying. | Best NAT traversal (**neither** side needs inbound reachability); reuses the existing Drive source + HMAC; tolerates offline peers. | Poll latency; the relay sees metadata and (unless encrypted) content → must encrypt the body; self-asserted originator unless keyed per-peer. | **OFFER** as a secondary async transport for the double-NAT case. |
| **libp2p (Kademlia DHT + relays)** | Global PeerID lookup via DHT + mDNS + circuit-relay; open a `/kasas/share` stream. | Discovery + NAT + mutual identity in one mature pure-Go stack. | Massively heavy for 2 peers; leans on third-party bootstrap/relays (soft central server + privacy); **global discoverability is a misfeature** here. | **REJECT** — enormous surface; its discovery is a misfeature for a private ledger. |
| **ActivityPub / WebFinger** | WebFinger resolves `acct:user@domain` → actor; Follow = subscribe; POST signed Activities to the inbox. | The Follow/subscribe model maps onto R2; HTTP Signatures are close in spirit. | Built for **public** social content (JSON-LD weight, LD-Signature fiddliness); assumes public DNS, **no NAT story**; shoehorns `ImportBatch` into ActivityStreams. | **REJECT the protocol** — borrow only the Follow/subscribe wording. |
| **Nostr / SSB / Matrix** | Publish signed events to relays (Nostr) / gossip signed feeds via pubs (SSB) / model a share as a federated room (Matrix). | Dial-out-to-relay solves double-NAT free; strong keypair identity; SSB's append-only feed mirrors kasas's event stream. | A relay/pub/homeserver sees (encrypted) financial metadata (soft no-central-server violation); social-broadcast-shaped vs selective; non-stdlib crypto / no mature Go path (SSB/Matrix); Matrix's homeserver is too heavy. | **REJECT** — keep only Nostr's dial-out-to-relay trick as a double-NAT footnote; SSB's feed is already mirrored by the events table. |

## What this ADR deliberately does **not** do

- **No central discovery server or directory (R3).** Peer addressing is out-of-band
  URL/invite exchange; global discoverability is a misfeature for a private ledger.
- **No CRDT / conflict-resolution / merge** of edits to shared rows. The publisher is the
  source of truth; the receiver's copy is read-only and tracks the publisher's edits on
  re-pull via the normal `UpdateTransactionFromSync` path. The receiver cannot diverge or
  push corrections back.
- **No re-export of others' rows.** "You may only share what you authored" is structural
  (origin-guard + dedup). A peer-sourced row is categorically ineligible for re-sharing; no
  transitive forwarding.
- **No real-time bidirectional CRDT/live sync.** Bidirectional sharing is two independent
  one-way relationships; there is no single "mutual subscribe" verb in v1.
- **No full ed25519 federation / libp2p / actor-inbox-outbox in v1** — named as a deferred
  follow-on (likely ADR 0010); v1 ships shared-per-peer HMAC with the fingerprint-as-peer-id
  format pre-adopted so the upgrade is non-breaking.
- **No mTLS and no full OAuth device-code grant.** Cert lifecycle is the wrong UX for a
  homelab (Tailscale gives transport mutual auth more cheaply), and the invite-code bootstrap
  captures the device-code idea more leanly.
- **No cryptographic recall of already-shared data.** Sharing stops **future** delivery
  (rotate/revoke/delete); data already pushed/pulled cannot be un-sent. A tombstone/retraction
  for upstream soft-delete ([ADR 0007](0007-transaction-soft-delete.md)) is an open question,
  not a v1 commitment.
- **No sharing of balances/account totals or net-worth.** Only selector-matched transactions
  cross; shared accounts (if explicitly shared) are read-only, peer-namespaced, and excluded
  from the subscriber's net-worth by default.

## Consequences

- **"Subscribe" reduces to "add a source" and "share" reduces to "save a search query"** —
  the entire feature is an extension of shipped seams, not a new subsystem. The engine's
  dedup/events/rules/history apply to peer rows untouched.
- **Two engine prerequisites become real, verified work** (not just config): a first-class
  search `source` field (GAP A — without it the loop-guard fails open) and a persister seam
  that folds `ImportTxn.Extensions` + a new `ImportTxn.Labels` into the born row (GAP B —
  without it the peer source cannot stamp provenance). Both are small but structural and must
  ship with the feature.
- **A second open, internet-facing surface** (the pull feed, joining ADR 0008's ingest):
  inert-until-secret, HMAC-gated, body-capped, **rate-limited**, coarse-401. The in-Go matcher
  per pull is a CPU-DoS vector for a leaked token — rate-limiting is mandatory, not optional.
- **One new publisher table** (`peer_shares`) + vault secrets + one settings key
  (`ledger_id`); the subscriber side is schema-free. Lean storage, comprehensive exposure.
- **The netfetch gate gains a sibling**: a per-peer egress allowance, distinct from plugin
  `net:fetch`, that lifts the `100.64.0.0/10` refusal **for granted peer hosts only**.
- **Trust is integrity, not identity, in v1.** A leaked per-peer secret allows impersonation
  until rotation; the ed25519 upgrade (and its already-adopted fingerprint id) is the
  non-breaking path to proven attribution.
- **Symmetry compounds:** one HMAC scheme now signs outbound events, verifies inbound
  webhook deliveries, signs pull-feed requests, and signs push deliveries — four uses, one
  primitive.

## Alternatives considered

The *transport/establishment* options are compared in the table above. The four **full-design
stances** considered for the *primary topology* were:

- **Push-symmetric (webhook-native) as primary.** The most literal extension of
  [ADR 0008](0008-inbound-webhook-source.md): the publisher's dispatcher pushes matched rows
  to the peer's ingest. Highest reuse, lowest latency. **Rejected as primary** because it
  requires the **receiver** reachable — the *least* common homelab shape (most receivers are
  fully firewalled) — and because consent then lives with the *publisher* (a peer can be
  force-fed), a weaker reading of R2's "subscribe." Kept as the **supported secondary** mode.
- **Pull-subscription (kasas-as-a-source-of-another-kasas) as primary — chosen.** Subscribe
  == adding a `Puller`, so the subscriber consents and needs zero inbound reachability (the
  common case), riding the engine cursor + `MultiCredentialed` verbatim. **Chosen** despite
  the new insert-stable-cursor engineering and poll latency, and with its provenance stance
  **overridden** (Decision 3: `source=peer:<id>`, not keep-originator).
- **Full ed25519 signed-object federation.** Strongest on identity (unforgeable `shared_by`)
  and discovery (actor handles, libp2p upgrade). **Rejected for v1** as materially heavier
  than kasas's lean ethos warrants for a 2-instance sync — a full actor/feed/inbox/outbox
  model + ed25519 key lifecycle, with the headline libp2p discovery win deferred anyway.
  **Its best ideas are grafted now**: fingerprint-as-peer-id (so attribution is upgradeable)
  and the signed hop-path (optional loop hardening). Named as the likely follow-on ADR.
- **Pure capability-link (shared-feed) as primary.** Cheapest to build and the cleanest
  establishment primitive. **Rejected as the *primary* model** because it is weakest on
  identity (pure bearer — the publisher cannot know which ledger pulled, `shared_by` is
  self-asserted) and its "subscription" is thin (a link, not a relationship). **Adopted as
  the establishment/bootstrap method and the simplest one-way share** — a supported sub-mode
  of the pull stance, not the subscription model itself.
