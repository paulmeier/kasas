# ADR 0010 — Custody-free internet peer connectivity (Syncthing-model)

- **Status:** Proposed (scoping only — not yet built)
- **Date:** 2026-06-26
- **Related:** [ADR 0009](0009-p2p-ledger-sync.md) (selective peer-to-peer ledger
  sharing — **the direct parent this ADR extends**; 0010 reuses its share contract
  verbatim, realizes its deferred ed25519-attribution upgrade, and **upholds and
  strengthens** — does not revise — its "no central server" stance),
  [ADR 0008](0008-inbound-webhook-source.md) (the inbound-webhook source + the
  `"<timestamp>.<body>"` HMAC scheme + the open-but-authed `Receiver`/`Delivery`
  ingest seam first contact rides, and the secret-in-`vault` precedent the keypairs
  follow),
  [ADR 0002](0002-plugin-network-capability.md) (the netfetch SSRF gate whose
  validate-then-dial helper the `peer` client reuses — plus **0009's** per-peer
  egress allowance, a netfetch sibling 0009 introduces and 0010 inherits, now central
  because the BYO front door is frequently a `100.64.0.0/10` Tailscale name),
  [ADR 0005](0005-plugin-originated-transactions.md) (the auto-purge-on-uninstall
  precedent the compromised-peer teardown mirrors),
  [ADR 0004](0004-transaction-document-artifacts.md) (how invoices / receipts ride
  document+itemization extension namespaces — **not** a new object model),
  [Ingestion architecture](../ingestion.md) (the `peer` `Puller` that gains one
  internet-reachable address),
  [Transaction Provenance](../../features/transaction-provenance.md)
  (`transactions.source = "peer:<id>"`),
  [Labels](../../features/labels.md) (`shared_by:<id>`, now **proven**),
  [Search](../../features/search.md) (the `source` field GAP A adds),
  [Event Stream](../../features/event-stream.md),
  [Webhooks](../../features/webhooks.md),
  [Data Model](../data-model.md)

## Context

[ADR 0009](0009-p2p-ledger-sync.md) made two **self-hosted** kasas ledgers share
selectively: a *share* is a saved [search](../../features/search.md) query, a
*subscription* is adding a `peer` [`Puller`](../ingestion.md), and the engine's
persist/dedup/events/rules/history come for free. It deliberately had **no central
server** — establishment was an out-of-band invite, the recommended substrate was
HTTPS over a **shared Tailscale tailnet**, and "global discoverability is a misfeature
for a private ledger" was stated as a non-goal. Its one limit: **0009 connects only
peers who can already reach each other** — two homelab boxes with no shared tailnet
and no port-forward have no way to exchange a byte across the open internet.

**An earlier draft of this ADR** tried to close that gap with a *hosted
zero-knowledge directory + store-and-forward encrypted relay* — a paid AWS service
that mapped fingerprints to keys and store-and-forwarded ciphertext for offline
peers. The owner rejected it for **one** reason, stated verbatim:

> *"Even zero-knowledge, a data processor with a privacy/DPA/retention/deletion
> obligation seems to be a real problem. I don't want to have to handle any of
> that."*

Then: *"Can we take the same approach that Syncthing takes? … with the intention we
fully avoid the need for a privacy/DPA/retention/deletion obligation."* The earlier
draft itself conceded the point — it admitted the relay *"is a data processor … needs
a privacy policy, a DPA, metadata-retention limits, and a deletion path."* That is
**exactly** the obligation the owner refuses to carry, and no amount of
zero-knowledge framing deletes it: an operated service that ingests, stores, routes,
or indexes user data is a processor of it.

**Syncthing is the template.** It does encrypted P2P sync across the internet while
the project holds almost nothing: a Device ID is a stable hash of the device's
long-lived key material (kasas's ed25519 fingerprint, already pre-adopted in 0009);
connections are direct-first (LAN, hole-punch, port-map); global discovery servers
store only `Device-ID -> current address` (ephemeral, signed, content-blind,
self-hostable, **optional**); and the project is **donation-funded** precisely because
it refuses to hold data. The owner's choices go **further** than Syncthing on two
axes — drop relays entirely (direct-only) and run **nothing at all** (not even an
optional discovery node).

So this ADR is a **full rewrite**, not a tweak. The hard constraint, which everything
below is built around and which is the spine of the design:

> **kasas (the project / the maintainer) operates NO server that touches user data,
> and NO user data sits at rest on any kasas-operated service. The data-processor
> obligation is eliminated *structurally* — there is no operated service to
> regulate.**

Three decisions are **locked** going in and are designed *around*, not re-litigated:

- **(L1) Direct-only, no async.** No relay, no store-and-forward mailbox, no community
  relay pool, no drop-it-in-your-own-cloud path. Peers connect **only** when both are
  online and at least one is reachable. The fully-double-NATed, both-offline case is
  **deliberately unsupported** — it is precisely the case that would require a
  store-and-forward server, the thing that creates the obligation. *"No async" is the
  feature that buys zero custody*, not a gap to apologize for: a ledger reconciling via
  durable idempotent pull tolerates "syncs next time we are both up."
- **(L2) Pure OSS, donations only.** No paid service of any kind in this ADR. The
  hosted-AWS service and all custody-based monetization are dropped (Syncthing
  Foundation posture).
- **(L3) kasas operates nothing.** Every transport is direct or user-owned. kasas ships
  software + a published protocol + **optional** reference binaries (e.g. a discovery
  helper) that **others** run or self-host; kasas runs none in production.

The resulting architecture is small. It is **"0009 + a keypair + a doctrine"**: it
adds cryptographic identity (ed25519 + X25519/age) and cross-NAT internet reachability
for peers who **bring their own front door**, with kasas operating nothing. It is
**purely additive** to 0009 — it does not walk back 0009's no-central-server
principle, it honors it harder.

## Decision

Add **custody-free internet peer connectivity** as **a keypair, a reachability
doctrine, and a consent gate**, layered under [ADR 0009](0009-p2p-ledger-sync.md)'s
share contract. The wire payload stays 0009's signed `ImportBatch`, *subscribe == add
a source*, *share == save a query*, and the `peer` `Puller` simply polls the
publisher's feed at an internet-reachable address the publisher already runs. Identity
is the **GPG model with modern pure-Go primitives** (ed25519 + age); there is **no
keyserver, no relay, no mailbox, and no kasas-operated server of any kind**.

### 1. Identity — the GPG model, modern Go primitives (realizes 0009's proven attribution)

Each ledger that opts in mints **two** keypairs, lazily, on first opt-in (an instance
that never opts in mints nothing and exposes nothing):

- an **ed25519** signing keypair (stdlib `crypto/ed25519`) for **identity &
  attribution** — this is [ADR 0009](0009-p2p-ledger-sync.md)'s already-pre-adopted
  peer fingerprint;
- a companion **X25519/age** recipient keypair (`filippo.io/age`, the **one new dep**)
  for **confidentiality**.

We adopt the GPG *model* — keypair = identity, fingerprint = address,
sign-for-attribution, encrypt-to-recipient — and **reject the OpenPGP wire format**
(justified in *Alternatives*): `golang.org/x/crypto/openpgp` is frozen/deprecated, the
live alternatives drag a heavy fork or cgo-Sequoia, and **kasas peers only ever talk
to kasas peers, so there is zero interop value** to pay the packet-zoo weight for. age
+ stdlib ed25519 reproduces the model with one well-audited pure-Go dependency.

**Fingerprint = peer id.** `peer:<id>` where `<id>` = unpadded-lowercase-`base32` of
the first 20 bytes of `SHA-256(ed25519 pubkey)` — 32 chars, well under 0009's ≤50-char
`peer:<id>`/`shared_by:<id>` label budget, QR-friendly, and **length-stable across a
future key-type change** because it hashes the key rather than embedding it. This is
the value 0009 pre-adopted, so adopting it here **realizes 0009's deferred upgrade**:
`shared_by` moves from *HMAC-asserted* to *cryptographically proven* **with no wire
break** — the spine of "purely additive to 0009." The human form is a **Signal-style
grouped-decimal "safety number"** (+ a word-list) for out-of-band comparison.

The X25519 key is **bound to the ed25519 identity by an ed25519 self-signature over
the `(ed25519_pub, x25519_pub)` tuple as presented in the out-of-band bundle / connect
request** — *not* over a directory entry, because no directory exists. A substituted
encryption key fails verification regardless.

> **Syncthing citation precision.** Syncthing's Device ID is `base32(SHA-256 of the
> device's self-signed **certificate**, which wraps its ECDSA P-384 key)` — not a hash
> of the bare public key, and not ed25519. We cite the **model** (id = a stable hash of
> the device's long-lived key material); kasas hashing the raw ed25519 pubkey is a
> deliberate cleaner variant of the same idea.

**Storage:** both private seeds live in `vault.SecretStore` under two fixed keys
(`peer.identity.ed25519_seed`, `peer.identity.x25519_seed`) via the existing
`SetSecretValue`/`SecretValue` — **no migration**, the
[ADR 0008](0008-inbound-webhook-source.md) secret-in-vault precedent. A private key
**never** lands in a DB column or the connections table.

### 2. Reachability — bring your own front door (BYO)

kasas operates **no** reachability infrastructure. A peer that wants to **receive**
shares exposes its already-existing 0009 feed via something it already runs, ranked by
homelab fit:

| BYO front door | Stable name | TLS terminates at | age envelope |
|---|---|---|---|
| **Shared Tailscale tailnet** (recommended) | `peer-ledger.<tailnet>.ts.net` | kasas (WireGuard end-to-end) | optional |
| **Tailscale Funnel** | `*.ts.net` public name | Tailscale edge | **mandatory** |
| **Cloudflare Tunnel** (the headline example) | hostname on the user's domain | Cloudflare edge | **mandatory** |
| **Cheap VPS / reverse proxy** (Caddy/nginx/Traefik) | the user's domain | the proxy (if it terminates) | **mandatory** if terminating |
| **dyndns + forwarded port** | dyndns name over a dynamic IP | kasas | strongly recommended |

The recommended substrate — a shared tailnet — is **often already present** in the
Unraid/homelab audience, so the BYO burden is near-zero for the common case. Options
1–4 and dyndns also yield a **stable name** that *doubles as the address*, so the
out-of-band `{fingerprint + address}` exchange does **not** go stale on an IP change
(contrast a bare-IP invite, which rots on the next DHCP lease). The `address` column
of `peer_connections` (§7) stores exactly this BYO front-door URL.

**The both-online-and-at-least-one-reachable requirement is the feature**, not a bug:
pull needs the **publisher** reachable, push needs the **subscriber** reachable —
either way both must be online and one reachable. The only case forgone is
**both-online-but-neither-has-a-front-door** (symmetric/double-NAT). That is a *narrow*
gap versus Syncthing: Syncthing's **live** relays never solved both-*offline*-async
either — what dropping relays forgoes is a live bridge for the neither-reachable case,
**not** an offline mailbox, which the model never offered.

**Honest limit (stated plainly, not over-sold):** a purely-dynamic-IP homelab peer
with **no** stable name **and no** community phone book is genuinely not well served.
The answer is "get a free stable name (Funnel / dyndns)" or "someone (not kasas) runs
a community `fp -> address` phone book" — **never** "kasas runs one." BYO does shift
some setup onto the user (more than "paste an invite"), but it is the only posture
compatible with the hard constraint, and the recommended tailnet substrate makes it
small for most.

### 3. Discovery — out-of-band, no kasas server

Exchange a **signed bundle** `{fingerprint + stable BYO address + inline ed25519_pub +
x25519_pub + minted_at}` once via paste/QR (generalizing 0009's feed-invite). Keys are
carried **inline** (Autocrypt-style) so the recipient TOFU-pins from the bundle
itself — there is **no server to fetch a key from**:

```
bundle = base64url( ed25519_sign(seed, T) || T )   # sign T directly; ed25519 hashes internally
T      = "kasas-peer-id/1" || fingerprint || ed25519_pub || x25519_pub
                            || address || minted_at
```

The recipient verifies `T`'s self-signature against the inline `ed25519_pub`, confirms
`fingerprint == base32(SHA-256(ed25519_pub)[:20])`, and pins
`{fingerprint, ed25519_pub, x25519_pub}`. A tampered bundle fails verification. The
bundle travels the **human channel** (Signal / email / in person / QR); no kasas
service mints, stores, or relays it — the local dashboard renders it as text/QR from
vault-held keys.

**Optional community/self-hosted phone book — named, not run by kasas.** For
dynamic-IP peers who cannot get a stable name, kasas publishes a `fp -> address`
phone-book **protocol** and may ship an **optional, self-host-only** reference
binary (the `stdiscosrv` analogy) — **with no kasas-blessed default node**: kasas
operates **zero** instances, ships no bootstrap address, and the out-of-the-box path
is direct out-of-band exchange (this guardrail is restated, not buried in
*Alternatives*). A peer announces, signed, every ~30 min
(Syncthing's global-discovery cadence); a client looks up `GET /disco/v1/<fingerprint>`
and **client-verifies** the signature against the fingerprint it already holds before
dialing:

```
announce = ed25519_sign(seed, "kasas-disco/1" || fingerprint || address || ttl || now)
lookup   GET /disco/v1/<fingerprint> -> { address, signed_at, sig }
```

It stores **only** ephemeral, signed, TTL'd `fp -> address` rows — **never** `fp ->
key`, never any financial data. The key is baked into the fingerprint via the hash (the
Syncthing Device-ID property), so a malicious or compelled phone book **cannot
substitute a key**: a wrong key cannot produce the requested fingerprint, and the
worst it can do is withhold or lie about an *address* (denial, not impersonation).
**kasas operates none** — there is no "default kasas node" and no project-allocated
handle namespace; the out-of-the-box path is direct out-of-band exchange.

> **Positive property worth stating:** dropping the keyserver *improves* the trust
> story. The earlier draft's directory mapped `fp -> key` and so could be MITM'd; an
> optional phone book maps `fp -> address` only, so it cannot swap a key. With no
> keyserver to MITM, the key-swap threat largely evaporates.

### 4. Consent — the connection request (D3); no data before accept; no server

The connection, not the share, is the unit of on-ledger state — and first contact
needs **no server**, because it rides the **same BYO reachability** the share will.

1. **Find.** Alice imports Bob's bundle (§3), verifies its self-signature, confirms the
   fingerprint, **TOFU-pins** `{fingerprint, ed25519_pub, x25519_pub}` into
   `peer_connections` (`status pending_out`, `direction out`), and ideally compares his
   safety number out-of-band.
2. **Request.** Alice sign-then-encrypts a **sealed** `connect` envelope whose signed
   tuple is `{type:"connect", from_fp, to_fp (Bob's fingerprint), ed25519_pub,
   x25519_pub, address (her own front door if any, else empty), greeting?, shared_at}`
   and POSTs it **directly** to Bob's BYO address at the generalized
   [ADR 0008](0008-inbound-webhook-source.md) ingest route
   (`POST <Bob.address>/api/v1/sources/peer/ingest`, `msg_type=connect`). Binding
   **`to_fp` inside the signed bytes** makes the request **recipient-bound** (the §6
   share-envelope discipline applied to first contact): Bob rejects any `connect` whose
   `to_fp` is not his own fingerprint, so a captured request cannot be surreptitiously
   re-aimed at a third peer. **The `connect` schema has no field that can carry
   financial data** — "no data before accept" is enforced by the **wire format**, not by
   policy. Alice needs **no front door to send** (an outbound POST works from behind any
   NAT); the age envelope keeps even Bob's TLS-terminating tunnel out of plaintext on
   this first byte.
3. **Accept / reject / block.** Bob's `peer` `Receiver.Receive(Delivery)` runs
   **in-handler** (open route, **not** dashboard-token-gated, **inert until an identity
   is minted** — the [ADR 0008](0008-inbound-webhook-source.md) pattern), decrypts to
   his X25519 seed, verifies Alice's ed25519 signature against the keys *she presented
   inline* (TOFU — no directory copy to trust), and surfaces a **pending request**. Bob
   must **explicitly accept** before anything flows: accept writes a `peer_connections`
   row (`status accepted`), TOFU-pins, and returns a sealed `connect_ack` — delivered
   in the **same synchronous HTTP response** if Alice has no front door, otherwise to
   her address; **reject** drops; **block** tombstones the fingerprint so future
   envelopes drop at receive. No accept ⇒ no row ⇒ **zero bytes ever flow**.
4. **Trust upgrade.** An out-of-band safety-number comparison flips `verified=1` (the
   "verified" badge). **Trust model:** L1 **TOFU-pin** (mandatory) + L2 **out-of-band
   safety number** (recommended financial default). PGP web-of-trust is rejected (wrong
   UX for a single-user homelab; consent + safety-number replace key-signing parties).

**No queued ack (L1).** If the synchronous socket closes before Bob accepts (he is
away from the dashboard), the `connect_ack` is **not** held for later collection —
holding it would be the store-and-forward mailbox L1 forbids. Instead, on a deferred
accept Bob's box either **initiates the reverse** `connect` to Alice's front door (if
she presented one) or Alice **re-sends** the idempotent, fingerprint-pinned request
within a fresh freshness window; the pending pin on each side makes the retry a no-op
write. This folds under L1's "syncs next time we are both up" — no byte ever rests on
a server awaiting collection.

**Replay.** Because the connect is a **synchronous direct** request/response (no async
mailbox), a tight ed25519-signed ±5-min freshness window suffices — 0009's direct-path
discipline. The earlier draft's async-mailbox `(sender_fp, msg_id)` seen-set is **cut**.

### 5. The share — 0009 verbatim, over a direct connection, with the envelope load-bearing

Once a connection is accepted, the share itself is **0009 unchanged**: the subscriber's
`peer` `Puller` polls the publisher's feed at the BYO address
(`GET <address>/api/v1/peer/feed/{share}`, the publisher evaluating `(storedSelector
AND originGuard)` server-side and returning a paginated `ImportBatch` + `next_cursor`),
or the publisher pushes a one-txn `ImportBatch` to the subscriber's `Receiver`. Reused
**verbatim** from 0009 (stated, not re-derived): the share = a saved search-query
selector live by default; subscribe = one `MultiCredentialed` entry; provenance R4
(`source=peer:<fp>`, the `kasas.peer` extension `{origin_source, origin_external_id,
sender_ledger_id, shared_at}`, the `shared_by:<fp>` label — **now proven, not
asserted**); the namespaced id `peer:<fp>:<origExtId>` giving dedup disjointness via
`ON CONFLICT (id)`; balances zeroed before signing; the origin-guard `NOT (source:peer:
OR source:webhook:)` + its Go re-check; **GAP A** (the first-class search `source`
field) and **GAP B** (the persister folds `ImportTxn.Extensions`/`Labels` into the born
row); the per-peer egress allowance; redaction allowlists; compromised-peer teardown
`DELETE WHERE source='peer:<fp>'`; and dedup/events/rules/history all **free**.

**Why the age envelope is now load-bearing — not generic defense-in-depth.** A BYO
**TLS-terminating** front door (Cloudflare Tunnel, Tailscale Funnel, a terminating
reverse proxy) terminates TLS at *its* edge and would otherwise see plaintext financial
data. The age sign-then-encrypt envelope (§6) rides **on top of** the connection and
keeps the `ImportBatch` opaque **even to the user's own tunnel provider** — the one
residual third party. It is therefore **mandatory** for any TLS-terminating BYO front
door, **optional only** on a pure WireGuard/tailnet path where no third party
terminates TLS. (See *Open questions* on surfacing this boundary so a "tailnet-only,
envelope off" deploy can't silently ship plaintext to a terminating proxy.)

### 6. Crypto — ed25519 + age, sign-then-encrypt, recipient-bound

The publisher builds the 0009 signed `ImportBatch` `B` (balances zeroed), then:

1. **Sign** (ed25519, length-framed canonical framing — never raw concatenation):
   `T = "kasas-peer-share/1" || sender_fp || recipient_fp || shared_at || sha256(B)`,
   `sig = Sign(seed, T)` — signing `T` **directly**, since stdlib `ed25519` already
   hashes its input internally (no hand-rolled `Ed25519ph` pre-hash; `T` already folds
   the large body to a fixed `sha256(B)` digest). Binding **`recipient_fp` inside the
   signed bytes** blocks surreptitious-forwarding; `sha256(B)` gives idempotency.
2. **Encrypt** (age, to the recipient's pinned X25519 key; **multi-recipient stanzas**
   = multi-device for free): `ciphertext = age.Encrypt({sig, T, B})`.

On receipt the subscriber decrypts, recomputes `sha256(B)`, checks `recipient_fp == me`
+ freshness, looks up `sender_fp` in `peer_connections`, and **verifies the signature
against the PINNED ed25519 key** — never a re-fetched key, never the body's self-claim.
**Only a pass** makes `source="peer:<sender_fp>"` / `shared_by:<sender_fp>` true (the
realized 0009 upgrade); every failure is a coarse drop (0009's coarse-401 discipline).
**Sign-then-encrypt** (over encrypt-then-sign) puts the signature *inside* the
ciphertext, so the BYO tunnel operator can't see **who** signed.

**One freshness clock, not two.** The earlier draft's two clocks (a tight relay-hop
window vs a loose mailbox-TTL envelope window) **collapse** to a single ±5-min window:
delivery is synchronous/direct, so the envelope uses the same tight window as 0009;
replay beyond it is bounded by `ON CONFLICT (id)` dedup.

age was chosen over `nacl/box` (no native multi-recipient, manual-nonce footgun) and
over literal OpenPGP (*Alternatives*). Supply-chain: pin `age`, X25519 recipients only
(no scrypt path, no `agessh`); ed25519 / SHA-256 / base32 are stdlib.

**The key-swap threat largely evaporates.** With no keyserver there is nothing to
MITM: keys arrive via the signed out-of-band exchange / connect request, and the id
binds the key. The honest residual is the **first-contact** channel itself — a
malicious party in the out-of-band channel, or a phone book lying about an address to
route Alice to an attacker who presents his *own* fingerprint — defended by TOFU-pin +
out-of-band safety-number **before** accept, and by the fact that a wrong key cannot
match a requested fingerprint (fails closed). The honest v1 limit: **detectable after
pin / preventable by safety-number, not cryptographically impossible at first
contact** — stated without invoking any directory. Full Key-Transparency is **cut**,
not deferred: there is no keyserver to make provably-not-a-trust-root.

### 7. Storage — lean-first

- **Keypairs** → `vault.SecretStore`, **no migration** (§1, ADR 0008 precedent). A
  private key never lands in a column.
- **Exactly one new table — `peer_connections`** (migration **00019**, the next free
  number after 0009's proposed `peer_shares` at **00018**; multi-dialect sqlc,
  **ASCII-only SQL comments** to dodge the em-dash bug). It holds **only public pins +
  consent state, never a private key, never plaintext financial data**:

```sql
CREATE TABLE peer_connections (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,  -- BIGSERIAL on pg
    peer_fp         TEXT NOT NULL,                       -- peer:<id> ed25519 fingerprint (the pin + trust anchor)
    handle          TEXT NOT NULL DEFAULT '',            -- mutable LOCAL display alias, never project-allocated
    pinned_ed25519  TEXT NOT NULL,                       -- TOFU pin
    pinned_x25519   TEXT NOT NULL,                       -- TOFU pin
    address         TEXT NOT NULL DEFAULT '',            -- the peer's BYO front door (Funnel/Tunnel/VPS/dyndns/tailnet)
    status          TEXT NOT NULL DEFAULT 'pending_in',  -- pending_in | pending_out | accepted | blocked
    direction       TEXT NOT NULL DEFAULT 'in',          -- in | out (who initiated)
    verified        INTEGER NOT NULL DEFAULT 0,          -- 1 once safety-number compared out-of-band
    greeting        TEXT NOT NULL DEFAULT '',            -- attacker-controlled, length-capped, rendered inert
    created_at      INTEGER NOT NULL,
    accepted_at     INTEGER,
    last_seen_at    INTEGER
) STRICT;
CREATE UNIQUE INDEX idx_peer_conn_fp ON peer_connections (peer_fp);
CREATE INDEX idx_peer_conn_status ON peer_connections (status);
```

This is the earlier draft's DDL minus the machinery the locked decisions delete:
`last_str_epoch`/`last_str_root` (Key-Transparency) are **dropped**, `mailbox_url` is
**renamed** to `address` (the BYO front door), and `directory_host` is **dropped** (no
kasas directory). **Shares** stay on 0009's `peer_shares`; **subscriptions** stay
0009's `MultiCredentialed` entries + per-source cursor (now referencing a connection by
fingerprint). **No new subscriber columns, and no new server** — there is no
directory/relay binary, no KV/object store, nothing operated.

### 8. Surfaces — full REST + MCP + dashboard parity

Optionally-disabled, so **read routes always register** and degrade calmly (the
disabled-subsystem-routes convention: nil-manager → `200 {enabled:false}`, never a
`404` banner; `503` on an unsecured instance → a calm empty+hint).

**REST (admin for mutations, read for lists):** `POST/GET /api/v1/peer/identity`
(mint/rotate; show fingerprint + safety-number + QR), `POST /api/v1/peer/connections`
(send a signed connect request to a peer's `fp` + `address`),
`GET /api/v1/peer/connections` (list masked, status + verified badge),
`POST .../{id}/accept|reject|block|verify`, `DELETE .../{id}` (`?purge=true` cascades
`DELETE WHERE source='peer:<fp>'`, the compromised-peer teardown mirroring 0009's
`peer.purged` and [ADR 0005](0005-plugin-originated-transactions.md)'s
auto-purge-on-uninstall). Plus 0009's open feed `GET /api/v1/peer/feed/{share}` and
the inbound `POST /api/v1/sources/peer/ingest` (which also receives `connect`
envelopes). **Cut** from the earlier draft: `/peer/directory/register`,
`/peer/directory/lookup`, `.../{id}/poll`, and the `/dir/v1/*` + `/mbox/v1/*`
protocol endpoints — there is no directory and no mailbox.

**MCP (1:1 with REST):** `mint_peer_identity`, `request_connection`,
`list_connections`, `accept/reject/block/verify_connection`, `remove_connection`.
**Cut:** `register_directory`, `lookup_peer`, `poll_mailbox`.

**Dashboard:** a **Peers / Connections** page — my fingerprint + QR + safety-number;
look up / add a peer by `fp` + `address`; a **connection-request inbox** (pending
Accept/Reject/Block + the safety-number compare ceremony + verified badge), reusing the
reveal-on-interaction pattern; degrades to a calm empty+hint on an unsecured instance.
**Cut:** the "Find & connect via directory" panel.

**Events** (noun.verb, ride the bus → SSE / outbound-webhooks / Events page free):
`peer.identity.minted`, `peer.identity.rotated`, `peer.connection.requested`,
`peer.connection.accepted`, `peer.connection.rejected`, `peer.connection.blocked`,
`peer.connection.verified`, `peer.connection.purged`. The actual financial sync still
fires 0009's `sync.completed` with `source=peer:<fp>` — **no** duplicate `peer.synced`.
**Cut:** `peer.directory.registered/revoked`, `peer.directory.keychange_detected`,
`peer.mailbox.polled`.

### 9. The "kasas operates nothing" proof — walked end to end

This is the spine. **This peer subsystem adds no kasas-operated server** and holds
**no** user data at rest on any kasas-controlled service, proven by walking every step
and showing each touches only (a) client-local state on the operator's own box, (b) the
user's **own** BYO infrastructure, or (c) an **optional non-kasas** community helper
that holds only ephemeral signed addresses.

**Scope-pin (honest about the rest of the product).** The claim is **peer-subsystem-
scoped**: this ADR introduces **no new kasas-pointing default endpoint**. The product
already ships two default-ON phone-home paths — the update check (`update.check=true`
→ GitHub Releases on `paulmeier/kasas`) and the plugin marketplace
(`plugins.registry.enabled=true` → a `raw.githubusercontent.com/paulmeier/kasas-plugins`
index). Those are **GitHub-hosted static content** (GitHub, not kasas, is the data
processor for any request metadata), are **operator-disableable**
(`update.check=false`, `plugins.registry.enabled=false`), and predate this ADR. The
peer subsystem adds **nothing** to that list and stands up **no** kasas-operated node of
its own.

1. **Identity.** Both keypairs are minted **locally**, in the operator's own process;
   the private seeds are written to the operator's **own** `vault.SecretStore`
   (HashiCorp Vault or a local JSON `FileStore`), under two fixed keys, no migration. No
   key is ever published to any kasas server, because the keyserver is cut. The public
   fingerprint is computed locally. **kasas touches nothing.**
2. **Discovery.** The signed `{fingerprint + address + inline pubkeys}` bundle travels
   the **human** channel (paste / QR). No kasas service mints, stores, or relays it. The
   only optional infrastructure is a community/self-hosted `fp -> address` phone book
   that is **named, not run** by kasas — and even when used it holds only ephemeral
   signed TTL'd addresses, never keys, never financial data, and its data-processor
   relationship is with **whoever runs it**, never kasas.
3. **First contact.** The signed, age-sealed `connect` envelope is an **outbound HTTPS
   POST** sent **directly** to the recipient's BYO address, verified **in-handler** by
   the recipient's **own** kasas binary via the existing `Receiver`/`Delivery` seam.
   Nothing is deposited anywhere awaiting collection — the mailbox is cut — so there is
   no kasas data-at-rest **by construction**. No rendezvous / STUN / DERP helper exists;
   L1 forecloses the both-online-neither-reachable case rather than operating a server to
   bridge it.
4. **Consent state.** `peer_connections` lives on the **operator's own DB** and holds
   only public pins + consent state — never a private key, never plaintext financial
   data — never on any kasas service.
5. **Share.** The subscriber's `peer` `Puller` polls the publisher's 0009 feed **at the
   BYO address** (or the publisher pushes to the subscriber's `Receiver`) — 0009
   verbatim, peer-to-peer. The **only** intermediary that can exist is the user's **own**
   TLS-terminating BYO tunnel provider, whose processor relationship is with the **user**
   — and the age envelope keeps even that party out of plaintext.

**The narrow, defensible claim (avoiding the Breyer overreach).** This does **not**
assert "zero personal data anywhere." Per CJEU *Breyer* (C-582/14), `IP:port` and `fp ->
address` mappings **are** personal data in the hands of a party who can identify the
subject — and those residual duties land on the **user's own BYO tunnel provider** and
**any community phone-book operator** (both of which observe IPs), **never on kasas**,
because kasas observes none of it (it operates no node). The claim is strictly: **"kasas
the project eliminates its *own* data-processor obligation by operating no service that
ingests, stores, routes, or indexes user data."** That is the owner's exact hard
constraint, satisfied structurally — and no server sneaks back in via handle
registration (the stable name *is* the user's BYO hostname; `handle` is a local mutable
alias), via a default discovery node (named, not run), or via a symmetric-NAT
rendezvous helper (name-and-rejected below). *No async* is the feature that buys zero
custody.

## What this ADR deliberately does **not** do

- **No async / store-and-forward.** The both-offline (and both-online-but-neither-
  reachable) case is **unsupported by design** — it is precisely the case that would
  require a server holding bytes awaiting collection, the one thing that creates
  data-at-rest. This is the trade that buys zero custody; a durable-pull ledger tolerates
  "syncs next time we are both up."
- **No kasas-operated server of any kind** — no relay, no mailbox, no keyserver, no
  Key-Transparency log, no coordination/STUN/DERP rendezvous helper, no default discovery
  node. kasas ships software + a published protocol + **optional** reference binaries
  others run.
- **No paid service.** Pure OSS, donations only (Syncthing Foundation posture); no paid
  custody tier of any kind.
- **No project-run handle namespace.** The stable name *is* the user's BYO front-door
  hostname; `peer_connections.handle` is a local mutable display alias, never
  project-allocated (a global namespace would need an operated squat-resistant authority).
- **No support for a purely-dynamic-IP peer with no stable name and no (non-kasas)
  discovery helper** (the honest limit stated in full in §2). The answer is BYO a free
  stable name, or have a third party run a phone book — never a kasas-run anything.
- **No claim of "zero personal data."** Per *Breyer*, `IP:port` and `fp -> address`
  mappings are personal data; the residual duty lands on the user's BYO tunnel provider
  and any phone-book operator, never on kasas. The defensible claim is scoped to kasas's
  own non-operation.
- **No new object model for invoices.** They ride
  [ADR 0004](0004-transaction-document-artifacts.md) extension namespaces inside the
  shared `ImportBatch`, governed by 0009's `include_extensions` allowlist.
- **No forward secrecy.** Identity/recipient keys are long-lived, so a one-time seed leak
  retroactively decrypts any **captured** ciphertext — re-attributed to the **BYO
  transport** (e.g. a tunnel provider logging ciphertext), not a kasas relay, because
  there is no kasas relay. A future per-connection ephemeral-key wrap is the named
  upgrade.
- **No cryptographic recall.** Once a blob is delivered+decrypted it cannot be un-sent;
  rotation/revocation govern the future only. A tombstone/retraction for upstream
  [ADR 0007](0007-transaction-soft-delete.md) soft-delete is still 0009's open question,
  now without an async wrinkle.

## Consequences

- **The data-processor obligation is eliminated structurally.** There is no
  kasas-operated service that ingests, stores, routes, or indexes user data, so there is
  no kasas-controlled processing activity to regulate — the owner's hard constraint, met
  by construction rather than by policy. This is the single headline win over the earlier
  hosted draft, which conceded it *was* a data processor.
- **Attribution becomes proven.** Adopting the ed25519 fingerprint (0009's pre-adopted
  format) upgrades `shared_by` from HMAC-asserted to signature-proven **with no wire
  break**, realizing 0009's deferred upgrade.
- **The `peer` source gains an internet-reachable address and one decrypt step** — and
  nothing else in 0009 changes. Subscribe still == add a `Puller`, share still == save a
  query; dedup/events/rules/history/provenance/origin-guard stay 100% free.
- **One new dep (`filippo.io/age`)** — must clear govulncheck and the supply-chain bar
  (pin the version; X25519 recipients only — no scrypt, no `agessh`). ed25519 / SHA-256 /
  base32 are stdlib.
- **One new table (`peer_connections`, mig 00019)** — public pins + consent state only;
  keypairs stay in vault (no migration). Lean storage, comprehensive exposure.
- **Both peers online + one reachable is required.** The both-offline / neither-reachable
  case is forgone — the cost that buys zero custody (and a narrower gap than it reads:
  even a live relay never solved both-offline-async).
- **The age envelope becomes load-bearing, not optional**, for any TLS-terminating BYO
  front door — it keeps the user's own tunnel provider out of plaintext custody.
- **This *upholds and strengthens* 0009's no-central-server stance** rather than revising
  it. 0009 stays Proposed; 0010 extends it and honors its principle harder. **Dependency:**
  0010 cannot land before (or without) 0009's GAP A (search `source` field), GAP B
  (persister folds Extensions/Labels), origin-guard, namespaced ids, and per-peer egress
  allowance; the mig numbering (00019 after 0009's 00018) assumes 0009 lands first or
  co-lands. (0009 names "likely ADR 0010" as a *federation* follow-on — this revised 0010
  is **not** federation but custody-free direct connectivity; that pointer should be
  reconciled to read as the cryptographic-identity + internet-reachability extension that
  honors 0009's no-central-server principle.)

## Alternatives considered

The headline rejects are **the earlier draft's own mechanisms** — this ADR is primarily
a rewrite that deletes them. Each is rejected on the zero-obligation constraint or on
simplicity.

- **Hosted zero-knowledge directory + encrypted relay (the prior 0010 draft).**
  **Rejected** — even storing only ciphertext + routing metadata, it is a data processor
  the owner explicitly refused ("I don't want to have to handle any of that"); the prior
  draft itself conceded it needed a privacy policy / DPA / retention / deletion path. An
  operated relay is a custody obligation. *This is the main thing this ADR revises.*
- **Store-and-forward async mailbox (the both-offline path).** **Rejected** —
  asynchronous delivery requires a server holding bytes awaiting collection, the one
  thing that creates data-at-rest on a kasas service. Dropping it (direct-only) is the
  feature that buys zero custody.
- **Directory / keyserver (HKP-shaped verified/revocable registry, `handle@host`
  federation).** **Rejected** — an operated lookup service stores `fp -> key` mappings +
  the lookup graph + querent IPs = personal data on a kasas-controlled service. Replaced
  by out-of-band `{fp + address}` exchange and an optional non-kasas `fp -> address`
  phone book.
- **Community relay pool (Syncthing-style volunteer relays).** **Rejected** — going
  *further* than Syncthing: any relay, even content-blind and community-run, is
  infrastructure the project would name and bless as a default. Direct-only drops even
  the live relay.
- **The paid AWS service (`dir.kasas.app`, commercial managed relay).** **Rejected** —
  any paid service touching user data is a data processor with the full obligation; the
  project goes pure OSS, donations only.
- **Full / drop-it-in-your-own-cloud async path.** **Rejected** — even a user-owned
  cloud drop reintroduces a store-and-forward intermediary and an at-rest blob;
  direct-only keeps bytes strictly peer-to-peer, the only intermediary being the user's
  own TLS-terminating tunnel (kept out of plaintext by the age envelope).
- **Key Transparency / CONIKS-lite (append-only signed log, STRs, Merkle inclusion,
  gossip witnesses).** **Cut, not deferred** — KT exists to make an operated *keyserver*
  provably-not-a-trust-root. With no keyserver there is nothing to MITM: keys arrive via
  the signed out-of-band exchange and the id binds the key.
- **A "tiny" STUN/DERP/coordination rendezvous helper for symmetric-NAT.** **Rejected
  (name-and-rejected so it isn't re-proposed as "lightweight")** — any coordination
  helper is an operated service touching connection metadata (IPs, timing), reintroducing
  the obligation. L1 forecloses this case rather than bridging it.
- **A project-run global handle namespace (`name@host`).** **Rejected** — a
  squat-resistant namespace needs an operated authority holding identity-linkable data.
  The stable name is instead the user's BYO front-door hostname; `handle` is a local
  alias.
- **A "default" kasas-hosted community phone book "so it works out of the box."**
  **Rejected (the camel's nose)** — that node would observe querent IPs + the `fp ->
  address` graph = personal data. The phone book stays strictly named-not-run; the
  out-of-the-box path is direct out-of-band exchange.
- **Literal OpenPGP/GnuPG wire format + SKS keyserver + web-of-trust.** **Rejected** —
  `x/crypto/openpgp` is frozen/deprecated; alternatives drag a heavy fork or cgo-Sequoia;
  zero interop value (kasas peers only talk to kasas peers); SKS died to CVE-2019-13050;
  web-of-trust is wrong UX for a single-user homelab. Adopt the GPG **model** (ed25519 +
  age), reject the bytes. `nacl/box` is **rejected** for age (no native multi-recipient,
  manual-nonce footgun); **encrypt-then-sign** is rejected for **sign-then-encrypt**
  (encrypt-then-sign would leak the sender's fingerprint — here, to the BYO tunnel
  operator).
- **libp2p Kademlia DHT / ActivityPub-WebFinger / Nostr-SSB-Matrix.** **Rejected**
  (carried from 0009) — global discoverability is a misfeature for a private ledger;
  these lean on third-party bootstrap/relays/homeservers (a soft central server + a
  metadata leak) and are massively heavy for a 2-instance sync.

## Open questions

- **Surfacing the mandatory-envelope boundary.** When `address` holds a non-tailnet
  hostname behind a TLS-terminating tunnel (Cloudflare/Funnel), should the dashboard show
  a "plaintext-to-your-tunnel" warning unless the age envelope is confirmed active — so a
  "tailnet-only, envelope off" deploy can't silently ship plaintext to a terminating
  proxy?
- **Phone-book spec depth.** Should kasas publish the `fp -> address` phone-book protocol
  (and a reference binary) in *this* ADR, or only name it and defer the spec to a
  follow-on, to avoid implying any kasas-blessed default node?
- **Invoice bytes.** If receipt/invoice **bytes** must traverse (ADR 0004 artifacts the
  subscriber can't dereference), do they ride the direct connection as an age-encrypted
  artifact, or does the extension stay a pure reference — and what about a subscriber who
  can't reach the publisher's external DMS, now that there is no mailbox?
- **Frontdoor-less subscriber.** A frontdoor-less initiator completes first contact via
  the synchronous `connect_ack`, but ongoing **pull** still needs the publisher
  reachable. Is a frontdoor-less initiator who is also a pull subscriber adequately
  served, or must we document that *receiving* shares requires BYO reachability on at
  least the receiving side?
- **Retraction without a mailbox.** Tombstone/retraction for upstream
  [ADR 0007](0007-transaction-soft-delete.md) soft-delete over a direct-only connection —
  does a directly-pulled retraction suffice, or is there a withholding-publisher
  detection gap? A signed monotonic per-connection counter is the named near-term
  hardening, re-attributed from a relay to the publisher/transport.
- **Rotation / multi-device UX.** Old-signs-new cross-signing for the ed25519 identity
  and age multi-recipient for multi-device are named, but device add/remove/revoke and
  re-distributing the rotated bundle out-of-band (with no directory to push to) is
  unspecified.
- **Connect-request rate-limit defaults** at the receiving peer's own front door (the
  only abuse lever that survives) — reuse 0009's leaked-token DoS reasoning, tuned for an
  open ingest route that now also receives `connect` envelopes.
- **The greeting field** is attacker-controlled text shown at the trust-decision moment —
  length-cap + inert rendering are mandatory; confirm no markdown/HTML path survives into
  the dashboard inbox.
