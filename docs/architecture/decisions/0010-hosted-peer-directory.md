# ADR 0010 — Hosted peer directory & zero-knowledge relay

- **Status:** Proposed (scoping only — not yet built)
- **Date:** 2026-06-26
- **Related:** [ADR 0009](0009-p2p-ledger-sync.md) (selective peer-to-peer ledger
  sharing — **the direct parent this ADR extends**; 0010 reuses its share contract
  verbatim and revises only its "no central discovery server" stance for the opt-in
  hosted case),
  [ADR 0008](0008-inbound-webhook-source.md) (the inbound-webhook source + the
  `"<timestamp>.<body>"` HMAC scheme reused for the relay *hop*, and the
  secret-in-`vault` precedent the keypairs follow),
  [ADR 0002](0002-plugin-network-capability.md) (the netfetch SSRF gate whose
  validate-then-dial helper the directory/relay client reuses — plus **0009's**
  per-peer egress allowance, a netfetch sibling 0009 introduces and 0010 inherits),
  [ADR 0005](0005-plugin-originated-transactions.md) (the auto-purge-on-uninstall
  precedent the compromised-peer teardown mirrors),
  [ADR 0004](0004-transaction-document-artifacts.md) (how "invoices / receipts /
  anything financial" ride document+itemization extension namespaces — **not** a new
  object model),
  [Ingestion architecture](../ingestion.md) (the `peer` `Puller` that gains one new
  transport),
  [Transaction Provenance](../../features/transaction-provenance.md)
  (`transactions.source = "peer:<id>"`),
  [Labels](../../features/labels.md) (`shared_by:<id>`),
  [Search](../../features/search.md) (the `source` field GAP A adds),
  [Event Stream](../../features/event-stream.md),
  [Webhooks](../../features/webhooks.md),
  [Data Model](../data-model.md)

## Context

[ADR 0009](0009-p2p-ledger-sync.md) made two **self-hosted** kasas ledgers share
selectively: a *share* is a saved [search](../../features/search.md) query, a
*subscription* is adding a `peer` [`Puller`](../ingestion.md), and the engine's
persist/dedup/events/rules/history come for free. It deliberately had **no central
server** (R3) — establishment was an out-of-band invite, the recommended substrate
was HTTPS over a **shared Tailscale tailnet**, and "global discoverability is a
misfeature for a private ledger" was stated as a non-goal.

That stance has one hard limit, and it is the gap this ADR exists to fill:

> **0009 can only connect peers who can already reach each other.** Two *strangers*
> on different networks — no shared tailnet, no port-forward, no out-of-band channel
> to paste an invite over, possibly never online at the same moment — have **no way
> to find each other and no way to exchange a single byte.**

The owner's intent is explicit: *"find clients out on the internet"* when two people
are **not** on the same network; *"use the same method GPG uses — private/public
keypairs that let you subscribe from a central server"*; *"the paid service we run in
AWS"*; *"allow users to match other users and share invoices, transactions, anything
financial they would like."* That is three capabilities 0009 lacks — **discovery**
(find a stranger by handle/fingerprint), **rendezvous across NAT** (relay data when
neither side is reachable), and **a published-key identity** (subscribe to a key, not
a URL) — and it reintroduces the exact thing 0009 banned: a central server.

The reframe that decides this ADR, and the only way to add a central server without
betraying 0009's ethos: **make the central server a convenience that cannot read your
data, cannot forge your identity, and is not the only one that can exist.** Concretely
— a **zero-knowledge relay** (it store-and-forwards ciphertext only) + a
**self-hostable directory** (the AWS instance is the default MX, not the network) +
**consent-gated connections** (a stranger must be accepted before one byte flows).
Under those three constraints "no *mandatory* central server" still holds, "global
discoverability is a misfeature" still holds (discovery is consent-gated, not a public
broadcast), and the operator is a router, not an authority. 0009 is **not** superseded;
this ADR adds a transport substrate beneath its share contract and revises one
sentence of its scope.

Three decisions are **locked** going in and are designed *around*, not re-litigated:

- **(D1) Zero-knowledge relay.** Shares are end-to-end encrypted to the subscriber's
  key; the service stores public keys + routes **ciphertext only**. The
  operator/AWS never sees plaintext financial data — **with one stated asterisk**: an
  *unverified first contact* trusts the directory not to swap the recipient's key, so
  the only window where a malicious/compelled operator could read plaintext is a
  pre-pin, pre-safety-number MITM (§7). Once a connection is pinned (and ideally
  safety-number-verified) the property is **unconditional**.
- **(D2) Open, self-hostable protocol.** The directory/relay wire is published; anyone
  runs their own (email-MX / PGP-keyserver / Headscale analogy). The AWS instance is
  the **default, paid, managed** convenience — monetize hosting/UX, not lock-in.
- **(D3) Consent-based connection requests.** Users find each other by handle or
  fingerprint, then send a *connection request* the other side must **accept** before
  any data flows.

## Decision

Add an **opt-in, self-hostable, zero-knowledge rendezvous** as **three thin pieces
layered under [ADR 0009](0009-p2p-ledger-sync.md)'s share contract** — so the wire
payload stays the signed `ImportBatch`, *subscribe == add a source*, *share == save a
query*, and the `peer` `Puller` gains **exactly one new transport and one new step**
(poll a mailbox, then decrypt). Identity is the **GPG model with modern pure-Go
primitives** (ed25519 + age), the directory is a verified keyserver that is **not a
trust root**, and the relay is a **dumb encrypted mailbox** that is the NAT answer.

### 1. Identity — the GPG model, modern Go primitives (not the OpenPGP wire format)

Each ledger that opts in mints **two** keypairs, lazily, on first opt-in (an instance
that never opts in mints nothing and exposes nothing):

- an **ed25519** signing keypair (stdlib `crypto/ed25519`) for **identity &
  attribution** — this is [ADR 0009](0009-p2p-ledger-sync.md)'s already-pre-adopted
  peer fingerprint;
- a companion **X25519/age** recipient keypair (`filippo.io/age`, the **one new dep**)
  for **confidentiality**.

The split is not a compromise — it is the honest realization of the owner's "same
method GPG uses." age is **encryption-only by design**, which is exactly why ed25519
stays the separate signing layer 0009 already mandates. We adopt the GPG *model*
wholesale — keypair = identity, fingerprint = address, sign-for-attribution,
encrypt-to-recipient, directory = keyserver — and **reject the OpenPGP wire format**
(justified in *Alternatives*): `golang.org/x/crypto/openpgp` is frozen/deprecated, the
live alternatives drag a heavy fork or cgo-Sequoia, and **kasas peers only ever talk
to kasas peers, so there is zero interop value** to pay the packet-zoo weight for.
age + stdlib ed25519 reproduces the model with one well-audited pure-Go dependency.

**Fingerprint = peer id.** `peer:<id>` where `<id>` = unpadded-lowercase-`base32` of
the first 20 bytes of `SHA-256(ed25519 pubkey)` — 32 chars, well under 0009's ≤50-char
`peer:<id>`/`shared_by:<id>` label budget, QR-friendly, and **length-stable across a
future key-type change** because it hashes the key rather than embedding it. This is
the value 0009 pre-adopted, so adopting it here upgrades `shared_by` from
*HMAC-asserted* to *cryptographically proven* **with no wire break**. The human form
is a **Signal-style grouped-decimal "safety number"** (+ a word-list) for out-of-band
comparison.

The X25519 key is **bound to the ed25519 identity by an ed25519 self-signature over
the directory entry**, so a directory can never substitute a foreign encryption key
under a real signing fingerprint without breaking the signature.

**Storage:** both private seeds live in `vault.SecretStore` under two fixed keys
(`peer.identity.ed25519_seed`, `peer.identity.x25519_seed`) via the existing
`SetSecretValue`/`SecretValue` — **no migration**, the
[ADR 0008](0008-inbound-webhook-source.md) secret-in-vault precedent. A private key
**never** lands in a DB column or the connections table.

### 2. Directory — a verified keyserver that is *not* a trust root (R-discover)

A thin REST registry mapping `handle` and `peer:<id>` → a **self-signed bundle**
`{handle, fingerprint, ed25519_pub, x25519_pub, mailbox_url, self_sig, updated_at,
revoked_at?}`. It takes HKP's dead-simple "GET key by fingerprint" read shape and
`keys.openpgp.org`'s **verified/revocable** corrections — **not** SKS's write-anything
design (named-and-rejected for the CVE-2019-13050 flooding death).

- `PUT /dir/v1/keys` — publish/rotate. The server **verifies `self_sig` against
  `ed25519_pub` and that `fingerprint == hash(ed25519_pub)`** before accepting
  (proof-of-control — the keys.openpgp.org correction that kills key-flooding). A
  rotation is additionally signed by the **previous** key (old-signs-new chain).
- `GET /dir/v1/keys/{fingerprint}` and `GET /dir/v1/keys?handle=` — lookup. The
  **client verifies the self-signature locally** before trusting; the directory is a
  cache, never an authority. There is **no list/scrape endpoint** — lookup needs the
  exact handle or the high-entropy fingerprint.
- `DELETE /dir/v1/keys` (signed) — revoke (publish a tombstone). Deletion-supporting,
  unlike SKS.

Writes are authenticated **by the ed25519 self-signature** (the key proves itself —
no operator-issued account/password the operator could forge); reads are open (a
pubkey is public).

**Federation (D2), email-MX/WKD-shaped.** A handle is `name@directory-host`. The AWS
`dir.kasas.app` is the **default** publisher; a self-hoster runs their own directory
**or** self-publishes the same signed bundle at a well-known static path
`https://<their-host>/.well-known/kasas/directory/<fp>.json` (WKD-style, no service at
all). The right-hand-side of the handle is authoritative, exactly like email — so the
AWS instance is one MX among many, which is what makes "no *mandatory* central server"
honest.

### 3. Relay — a dumb, zero-knowledge, store-and-forward encrypted mailbox (D1)

The synthesis of Signal's dumb-relay + Briar's Mailbox, sized to kasas. The relay does
**zero content logic**:

- `POST /mbox/v1/<recipientFingerprint>` stores **one opaque blob**
  `{routing-header(recipient_fp, size, ts, msg_type), ciphertext}`, keyed by recipient
  fingerprint, with a **TTL + per-recipient quota**. `msg_type ∈ {connect, connect_ack,
  connect_decline, share}`.
- `GET /mbox/v1/inbox` (**authenticated by the recipient's ed25519 signature** — the
  delivery-token-not-identity model) drains the queued blobs; the recipient decrypts
  **locally**.
- `DELETE /mbox/v1/inbox/<msgId>` (or auto-delete-on-drain) acks after local persist.
  Idempotent: a re-delivered `share` blob carries the identical 0009
  `peer:<id>:<origExtId>` row id, so the engine's `ON CONFLICT (id)` dedup no-ops it —
  ack-loss is harmless.

**What the relay sees:** recipient fingerprint, blob size, timestamp, message type,
and the TLS-terminated **ciphertext**. **What it cannot see:** any plaintext financial
field (accounts, amounts, memos, labels, invoices), the *sender* (it is inside the
ciphertext — see §5), or the signature. The body is E2E-encrypted **independent of
TLS**, which is what makes this zero-knowledge rather than merely transport-encrypted —
modulo the one §7 first-contact caveat (an operator that is *also* the directory can
read plaintext only by MITMing a key before it is pinned/verified, never after).

**This maps onto 0009 with no new model.** Draining the mailbox **is** the `peer`
source's `Puller.Fetch` (a *mailbox* transport that decrypts before returning the
`ImportBatch`); push, **for the unreachable-peer case only**, is 0009's outbound
dispatcher POSTing ciphertext to the mailbox instead of directly to 0009's inbound
`peer` `Receiver` (the direct POST stays primary for a reachable peer — §4 tiers it). The mailbox slots in as a **new
substrate row in 0009's methods-comparison table**, under the same `ImportBatch`
contract. The 0008/0009 `"<ts>.<body>"` HMAC still authenticates the relay **hop**
(deposit token / poll signature); age+ed25519 add the E2E layer **on top** — two
layers authenticating two different things, never conflated.

### 4. NAT traversal — the mailbox *is* the answer (R-relay)

This is precisely the gap 0009 left. 0009/Tailscale connects only peers who **already
share a tailnet**; two strangers share nothing. With the mailbox, **both NATed parties
dial out** over plain HTTPS: the publisher POSTs ciphertext to the subscriber's
mailbox, the subscriber polls its own mailbox. Neither needs inbound reachability, an
open port, a shared tailnet, hole-punching, or STUN/TURN/ICE — because a financial
share is a **file-shaped blob**, store-and-forward beats a live tunnel, and a homelab
box offline for hours just drains its mailbox when it next polls.

**Tiered exactly like Tailscale's DERP fallback:** 0009's **direct** path (HTTPS over
Tailscale / a reachable feed URL) stays **PRIMARY** when peers *can* reach each other
(lowest latency, no third party touches even ciphertext); the AWS mailbox is the
**FALLBACK** for the stranger/double-NAT case. The directory bundle's `mailbox_url`
tells a publisher where to drop; a peer advertising a directly-reachable base URL skips
the mailbox entirely. Plain HTTPS suffices (no Tor, contra Briar) because the body is
already E2E-encrypted. The SSRF gate is reconciled exactly as 0009 did
it — the `peer` client reuses **0009's extracted validate-then-dial helper** (the
[ADR 0002](0002-plugin-network-capability.md) gate's logic that 0009 makes a shared
injectable helper: DNS-pin-at-dial, redirect re-check, https-only, size/timeout caps)
together with **0009's per-peer egress allowance** (the netfetch sibling 0009
introduces), here granted for the confirmed directory/relay host:port only, never
widening `net_grants`.

### 5. The E2E envelope — sign-then-encrypt with recipient-binding

The publisher builds the 0009 signed `ImportBatch` (plaintext `B`), then:

1. **Sign** (ed25519, over a length-prefixed canonical framing — never raw
   concatenation): `T = "kasas-peer-share/1" || sender_fp || recipient_fp || shared_at ||
   sha256(B)`, `sig = Sign(seed, sha256(T))` — the SHA-256 is an **Ed25519ph-style
   fixed-size signed payload**, domain-separated by the `"kasas-peer-share/1"` version
   prefix, not a hash-of-a-hash quirk (ed25519 hashes internally regardless; the framing
   is length-prefixed and fixed). Binding **`recipient_fp` inside the signed bytes**
   blocks surreptitious-forwarding (a recipient can't re-address a still-valid signed
   inner blob to a third party — it fails verification at the new recipient). `sha256(B)`
   gives idempotency. **Two distinct freshness clocks, never conflated** (the
   store-and-forward correction): the **relay hop** — the deposit token and the
   `GET /mbox/v1/inbox` poll signature — keeps 0009's tight ±5-min HMAC window, because
   those are synchronous request/response; the **end-to-end envelope** does **not** — a
   share signed at deposit and collected hours later (the whole point of an async mailbox,
   §4) is accepted within the **mailbox TTL** (`now − mailbox_TTL ≤ shared_at ≤
   now + skew`), with replay beyond that bounded by the `ON CONFLICT (id)` dedup, not a
   tight clock. A ±5-min *envelope* window would reject every offline-peer delivery and
   defeat §4.
2. **Encrypt** (age, to the recipient's X25519 key; **multi-recipient stanzas** =
   multi-device for free): `ciphertext = age.Encrypt({sig, T, B})`.

**Sign-then-encrypt** puts the signature *inside* the ciphertext, so the relay can't
see **who** signed — sharper D1 than encrypt-then-sign, which would leak the sender on
the wire. On receipt the subscriber decrypts, recomputes `sha256(B)`, checks
`recipient_fp == me` and freshness, looks up `sender_fp` in `peer_connections`, and
**verifies the signature against the PINNED ed25519 key** (never a freshly-fetched
directory key, never the body's self-claim). **Only a pass** makes
`source = "peer:<sender_fp>"` / `shared_by:<sender_fp>` true — which is the upgrade
0009 pre-adopted: attribution moves from *which secret signed* (asserted) to *which key
signed* (proven), closing 0009's explicit v1 honesty caveat. Every failure is a coarse
drop (no which-check-failed leak), matching 0009's coarse-401 discipline. age was
chosen over `nacl/box` (no native multi-recipient, manual-nonce footgun) and over
literal OpenPGP (§Alternatives).

### 6. Consent — the connection request (D3); no data before accept

Modeled on a contact request; the **connection**, not the share, is the unit of
on-ledger state.

1. **Find.** Alice looks up Bob by `handle` or `peer:<id>`, verifies his self-signed
   bundle, **TOFU-pins** his `{fingerprint, ed25519_pub, x25519_pub}`, and ideally
   compares his safety number out-of-band.
2. **Request.** Alice sends a **sealed envelope** (sign-then-encrypt to Bob)
   `{type:"connect", from_handle, from_fp, ed25519_pub, x25519_pub, mailbox_url,
   greeting?, shared_at}` to Bob's mailbox. The request **carries Alice's keys inline**
   (Autocrypt-style, generalizing 0009's feed-invite), so Bob can act **without
   trusting his directory's copy** of Alice — and the directory-optional invite path
   still works as the no-directory fallback. **The `connect` schema has no field that
   can hold financial data** — "no data before accept" is enforced by the wire format,
   not by policy.
3. **Accept / reject / block.** Bob's poller drains, decrypts, verifies Alice's
   signature against the keys *she presented*, and surfaces a **pending request** in
   the dashboard. Bob must **explicitly accept** before anything flows: accept writes a
   `peer_connections` row (status `accepted`) and returns a sealed `connect_ack`;
   **reject** drops it; **block** writes a tombstone so future envelopes from that
   fingerprint are dropped at drain. No accept ⇒ no row ⇒ no feed token ⇒ **zero bytes
   ever flow** ⇒ no public-stranger exposure. **Replay guard for non-row envelopes:** the
   `share` blob's idempotency rides the `peer:<id>:<origExtId>` row-id dedup, but `connect`
   / `connect_ack` / `connect_decline` are *consent messages*, not transaction rows — a
   captured `connect` blob could be re-deposited as a fresh pending request. So the signed
   tuple carries a `msg_id` (nonce); the drainer tracks a small seen-set keyed by
   `(sender_fp, msg_id)` for the TTL window and no-ops re-delivery — PoW + TTL + quota only
   bound flooding *cost*, this gives per-message replay rejection.
4. **Then, and only then, data.** An accepted connection **is** 0009's per-peer
   relationship: each side independently creates a 0009 share targeting the other
   (0009's "two one-way relationships," unchanged), and subsequent matched
   `ImportBatch` pages ride the mailbox as `share` envelopes.

### 7. Key trust — the one new threat, and how it dies

The hosted directory introduces exactly **one** new attacker: a **malicious or
compelled directory** that hands out the *wrong* pubkey to silently MITM a connection
— the central-server risk 0009 avoided by having no central server. Defeated in
layers, **none of which trust the operator**:

- **L1 — TOFU-pin (v1, mandatory).** Pin the peer's fingerprint+keys at the first
  accepted connection (in `peer_connections`); warn loudly on any later change. The
  directory **cannot silently swap a pinned key** without the client noticing, and
  delivery always verifies against the **pin**, never a re-fetched directory key — so
  even a *fully compromised* directory cannot break an already-pinned connection.
- **L2 — out-of-band safety number (v1, recommended financial default).** The
  Signal-style grouped-decimal/word comparison over a side channel (call / in person)
  confirms the key with **zero trust in any server** — the strongest rung, surfaced as
  a "verified" badge across REST/MCP/dashboard. The ed25519 self-signature binding the
  X25519 key means even a substituted *encryption* key fails verification.
- **L3 — append-only signed key log (v1 ships the log; full proofs deferred).** The
  directory publishes signed tree roots day one (so the follow-on is non-breaking) and
  clients/peers can fetch+gossip them; **full CONIKS/Key-Transparency Merkle inclusion
  + equivocation-proof gossip is deferred to a follow-on ADR**, the way 0009 pre-adopted
  the ed25519 format and deferred the federation build.

PGP **web-of-trust is rejected** (wrong UX for a single-user homelab; consent D3 +
safety-number replace key-signing parties). This layering is the linchpin: it lets the
ADR honestly claim the hosted directory is a **discovery convenience, not a trusted
authority** — which is what reconciles a central server with 0009's "don't trust a
central server." The honest limit, stated plainly: **v1 is "misbehavior detectable
after pin / preventable by safety-number," not "cryptographically impossible"**; the
key-transparency log is the upgrade to provable.

### 8. How this layers on [ADR 0009](0009-p2p-ledger-sync.md) — maximal reuse

**Reused unchanged:** the share = saved-search-query / subscribe = add-a-`Puller`
model; the neutral `ImportBatch` payload; provenance R4
(`source = "peer:<senderLedgerId>"`, originator preserved in the `kasas.peer`
extensions namespace `{origin_source, origin_external_id, sender_ledger_id, shared_at}`,
human `shared_by:<id>` label, namespaced id `peer:<id>:<origExtId>` for dedup
disjointness via `ON CONFLICT (id)`); the **origin-guard** (every selector AND-ed with
`NOT (source:peer: OR source:webhook:)` so a ledger exports only rows it authored —
kills A→B→A echo, forbids transitive re-share); **GAP A** (the first-class search
`source` field) and **GAP B** (the persister folds `ImportTxn.Extensions`/`Labels` into
the born row); the per-peer egress allowance reconciling the netfetch gate; redaction
allowlists; and dedup/events/rules/history, all **free**.

**What changes, additively (non-breaking):** (1) `peer:<id>` is now the ed25519
**fingerprint**, so `shared_by` becomes **proven** (the format 0009 pre-adopted); (2)
the `peer` `Puller` gains a **mailbox transport** (drain ciphertext → verify+decrypt →
return `ImportBatch`) — one new transport, one new step; (3) the push dispatcher can
POST ciphertext to a mailbox; (4) the directory lookup is a **new establishment path**
feeding 0009's existing pairing after consent. 0009's direct-path HMAC trust layer
stays for reachable peers. **0009 is not superseded** — this ADR adds a forward link
and revises only its "no central discovery server" sentence for the opt-in hosted case.

### 9. "Invoices / anything financial" — ride ADR 0004, no new object model

Per the owner's *"share invoices, transactions, anything financial."* Transactions /
accounts / labels are 0009's job. **Invoices / receipts ride
[ADR 0004](0004-transaction-document-artifacts.md)'s document + itemization extension
namespaces** — they are fields in the shared `ImportBatch`'s `extensions`, governed by
0009's per-share `include_extensions` allowlist, **not** a new object model. If the
receipt *bytes* must traverse (the subscriber can't reach the publisher's
paperless/Drive store), they ride as an optional **age-encrypted artifact blob**
deposited alongside the batch in the same mailbox (ciphertext to the relay), the
extension merely referencing its deposit id. (Whether the ledger should ever touch
artifact bytes vs stay a pure reference is an open question.)

### 10. Storage — lean-first

- **Keypairs** → `vault.SecretStore`, **no migration** (§1, ADR 0008 precedent). The
  per-connection mailbox/feed token → vault keyed by the connection row, never a
  column.
- **Exactly one new table — `peer_connections`** (migration **00019**, the next free
  number after 0009's proposed `peer_shares` at 00018; multi-dialect sqlc, **ASCII-only
  SQL comments** to dodge the em-dash bug). A connection earns a table — it is
  genuinely relational, listed, and per-row-revocable consent state a vault scalar
  can't express — the same precedent that gave `rules`, `webhooks`, and `peer_shares`
  theirs. It holds **only public pins + consent state, never a private key, never
  plaintext financial data**:

```sql
CREATE TABLE peer_connections (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,  -- BIGSERIAL on pg
    peer_fp         TEXT NOT NULL,                       -- peer:<id> ed25519 fingerprint (the pin + trust anchor)
    handle          TEXT NOT NULL DEFAULT '',            -- mutable alias, display only
    pinned_ed25519  TEXT NOT NULL,                       -- TOFU pin
    pinned_x25519   TEXT NOT NULL,                       -- TOFU pin
    directory_host  TEXT NOT NULL DEFAULT '',
    mailbox_url     TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'pending_in',  -- pending_in | pending_out | accepted | blocked | revoked
    direction       TEXT NOT NULL DEFAULT 'in',          -- in | out (who initiated)
    verified        INTEGER NOT NULL DEFAULT 0,          -- 1 once safety-number compared out-of-band
    last_str_epoch  INTEGER,                             -- last verified signed-tree-root epoch (L3 hardening)
    last_str_root   TEXT NOT NULL DEFAULT '',
    greeting        TEXT NOT NULL DEFAULT '',            -- attacker-controlled, length-capped, rendered inert
    created_at      INTEGER NOT NULL,
    accepted_at     INTEGER,
    last_seen_at    INTEGER
) STRICT;
CREATE UNIQUE INDEX idx_peer_conn_fp ON peer_connections (peer_fp);
CREATE INDEX idx_peer_conn_status ON peer_connections (status);
```

- **Shares** stay on 0009's `peer_shares`; **subscriptions** stay 0009's
  `MultiCredentialed` entries + per-source cursor (now referencing a connection by
  fingerprint instead of carrying a bare base-URL). **No new subscriber columns.**
- **The directory/relay service is the only new *server*** — a stateless Go binary +
  a KV/object store with TTL (DynamoDB/S3): mailbox ciphertext, signed bundles, the
  append-only log + STR archive. **That state is the operator's, not the ledger's
  schema** — zero impact on the lean ledger DB.

### 11. Surfaces — full REST + MCP + dashboard parity

The directory/relay subsystem is optionally-disabled, so **read routes always
register** and degrade calmly (the disabled-subsystem-routes convention: nil-manager →
`200 {enabled:false}`, never a `404` banner).

**REST (kasas-side client; admin for mutations, read for lists):**
`POST/GET /api/v1/peer/identity` (mint/rotate; show fingerprint + safety-number + QR),
`POST/DELETE /api/v1/peer/directory/register` (publish/revoke signed entry),
`GET /api/v1/peer/directory/lookup?handle=|?fp=` (resolve + return the verified
bundle), `POST /api/v1/peer/connections` (send a request),
`GET /api/v1/peer/connections` (list masked, with status + verified badge),
`POST .../{id}/accept|reject|block|verify`, `DELETE .../{id}` (`?purge=true` cascades
`DELETE WHERE source='peer:<fp>'`, the compromised-peer teardown mirroring 0009's
`peer.purged` and [ADR 0005](0005-plugin-originated-transactions.md)'s
auto-purge-on-uninstall), `POST .../{id}/poll` (manual mailbox drain). The
`/dir/v1/*` and `/mbox/v1/*` endpoints are the **relay service's own** published
protocol — the kasas instance is a *client* of them.

**MCP (1:1 with REST):** `mint_peer_identity`, `register_directory`, `lookup_peer`,
`request_connection`, `list_connections`, `accept/reject/block/verify_connection`,
`remove_connection`, `poll_mailbox`.

**Dashboard:** extend 0009's **Peers** page with a "Find & connect" panel (lookup by
handle/fingerprint, my fingerprint + QR + safety-number, send-request) and a
"Connection requests" inbox (pending requests with Accept/Reject/Block, the
safety-number compare ceremony, verified badge), reusing the reveal-on-interaction
pattern; degrades to a calm empty+hint on an unsecured instance.

**Events** (noun.verb, ride the bus → SSE / outbound-webhooks / Events page free):
`peer.identity.minted/rotated`, `peer.directory.registered/revoked`,
`peer.connection.requested/accepted/rejected/blocked/verified/revoked`,
`peer.mailbox.polled`, `peer.directory.keychange_detected`. Actual financial sync
still fires 0009's `sync.completed` with `source=peer:<fp>` — **no** duplicate
`peer.synced`, matching 0009.

### 12. Business & ops — the paid AWS service, tied to the existing dual license

The protocol is published and the relay/directory ship as a runnable **MIT** reference
binary — anyone self-hosts the whole rendezvous (D2), so "no *mandatory* central
server" holds. The AWS `dir.kasas.app` is the **default, paid, managed** convenience
(reliable always-on relay, the no-DNS-domain majority who can't self-publish WKD-style,
handle registration) — monetizing **hosting + UX, not lock-in**, which fits kasas's
existing **CLA + commercial dual-licensing** (the MIT core ships the client + open
relay spec; the hosted managed relay is the commercial half). Because *identity is the
key, not the host*, a paying user can migrate to a self-hosted directory keeping their
fingerprint and every pin (only the handle's right-hand-side changes) — the
trusted-because-replaceable property that is **both** the ethos win and the business
win.

**Abuse / moderation, honestly bounded by D1.** A zero-knowledge relay **cannot
moderate content** (it never sees plaintext) — a deliberate liability-minimizing
feature, so all levers are **metadata-shaped**: per-recipient mailbox **quota + TTL**,
per-fingerprint/per-IP **rate limits** + a small proof-of-work stamp on `connect`
envelopes (so a stranger can't cheaply flood a victim before any accept), **consent**
(D3) as the spam wall (an un-accepted request is one TTL'd, declinable entry with zero
data exposure), and a metadata-level **deny-list/defederation** takedown that never
decrypts anything. Directory handle-squatting is bounded by proof-of-control on `PUT` +
the append-only log; a contested-handle takedown is operator **policy**, not a protocol
feature.

**Regulatory, stated plainly:** even zero-knowledge, the hosted relay **is a data
processor** — it stores ciphertext blobs + routing metadata + IPs. The managed service
therefore needs a privacy policy, a DPA, metadata-retention limits (TTL), and a
deletion path. For a **pinned/verified** connection it holds **no plaintext and no
keys** (the lowest-liability posture and the strongest selling point) — but the claim
is *conditional on verification*: an operator that is also the directory could read
plaintext only inside the §7 first-contact-before-pin window, never after, and the
safety-number ceremony (or the deferred KT log) closes even that. And in all cases
**"zero-knowledge" is not "zero data"** — the relay still holds metadata, and the
directory still holds the lookup graph (who-looks-up-whom + the fingerprint→`mailbox_url`
linkage, the same operator in the default deploy). The ADR says so plainly rather than
selling an unconditional "the server sees nothing."

## Methods compared

0010 adds a **mailbox substrate row** to [ADR 0009](0009-p2p-ledger-sync.md)'s
methods-to-establish-a-sync table; the mechanisms it newly chooses among — **discover**
a stranger, **relay** across NAT, and **trust the key** against a malicious directory —
are compared here, each rejected option named with a one-line reason.

| Approach | What it does | Pros | Cons | Verdict |
|---|---|---|---|---|
| **Directory keyserver** (HKP-shape, verified/revocable, federated `handle@host`) | DISCOVER: maps a handle or ed25519 fingerprint to a self-signed `{ed25519_pub, x25519_pub, mailbox_url}` bundle; `PUT` verifies proof-of-control, `GET` is client-verified, signed `DELETE` revokes; handle = `name@directory-host` with a WKD-style static self-publish escape hatch. | Dead-simple REST; proof-of-control kills SKS-style key-flooding; deletion/revocation supported; federation makes the AWS node replaceable (D2); no list/scrape endpoint = no enumeration of who-banks-here. | A central lookup point a malicious operator could swap keys at (mitigated by TOFU+safety-number, fully closed only by the deferred KT log); handle squatting needs proof-of-control + operator policy; a cold directory offers little over 0009's out-of-band invite until populated. | **ADOPT** as the v1 discovery layer (verified, self-hostable, federated `handle@host`); reject the SKS write-anything architecture by name (CVE-2019-13050). |
| **Out-of-band invite / Autocrypt key-in-bootstrap** (0009's path, generalized) | DISCOVER without a directory: the connection request itself carries the sender's keys inline, so a pasted/QR'd invite establishes a peer with no directory round-trip. | Zero infrastructure; works offline; the directory-optional fallback; already proven by 0009's feed-invite; pairs naturally with consent. | Requires an out-of-band channel (the exact thing two strangers LACK, so it can't be the only path); opportunistic-without-verification is too weak alone for financial data. | **KEEP** as the no-directory fallback + the key-carrying shape of the connect request; insufficient alone for the stranger case, which is why the directory exists. |
| **Encrypted store-and-forward mailbox** (Signal dumb-relay + Briar-Mailbox) | RELAY-ACROSS-NAT: both NATed sides dial out — publisher POSTs ciphertext to the recipient's per-fingerprint mailbox, recipient ed25519-signed-polls to drain, acks to delete; TTL + quota. | THE NAT answer with no hole-punching/STUN/TURN/shared-tailnet; offline-tolerant; relay sees only ciphertext+routing (D1); maps 1:1 onto the `peer` `Puller.Fetch`; tiny stateless server (D2/commercial); the leanest synthesis. | Poll latency vs 0009's direct push; metadata leak (recipient fp + size + timing = a social graph); mailbox spam is a new DoS surface (quota/TTL/rate-limit/PoW mitigate); a data-processor obligation even zero-knowledge. | **ADOPT** as the v1 fallback substrate UNDER 0009's direct path (which stays primary for reachable peers, DERP-style tiering). |
| **Tailscale / DERP direct path** (0009's substrate, *carried over — see [ADR 0009](0009-p2p-ledger-sync.md)*) | RELAY-ACROSS-NAT for peers who already share a tailnet: WireGuard hole-punch + a blind packet relay; the proof that ciphertext-only relaying is mature pure-Go. | Lowest latency, no third party touches even ciphertext; strong free identity; self-hostable control plane (Headscale = the D2 precedent); the gold-standard blind-relay proof. | Requires a SHARED tailnet — exactly what two strangers lack, so it does NOT connect strangers; the `100.64.0.0/10` SSRF-gate friction; narrower audience. | **KEEP** as 0009's PRIMARY direct path for reachable peers; borrow DERP's blind-relay proof + tiering; it cannot solve the stranger case (that's the mailbox's job). |
| **TOFU-pin + out-of-band safety-number** (v1 key trust) | TRUST-THE-KEY: pin the peer's keys at first accepted connection and always verify delivery against the pin; compare a Signal-style safety number over a side channel to upgrade to verified. | Even a fully-compromised directory can't break a pinned connection (delivery never re-fetches a key); safety-number gives zero-trust-in-any-server verification; lean, pure-Go, no infrastructure. | First-contact MITM window before the pin; users may skip the safety-number; detects/prevents but doesn't make swaps cryptographically impossible. | **ADOPT** as the v1 trust model (mandatory TOFU + recommended safety-number); honest about the first-contact window. |
| **Key Transparency** (CONIKS-lite: append-only signed log → Merkle inclusion + equivocation gossip) | TRUST-THE-KEY, hardened: the directory serves inclusion proofs against hash-chained signed tree roots that clients verify and gossip, so a key swap is provable misbehavior. | Upgrades the directory from trusted to verifiable — the airtight malicious-directory defense; the linchpin that fully reconciles a central server with 0009's no-central-trust. | Heaviest machinery (Merkle log, STR signing, proof verification, a witness/gossip network); fights the lean ethos if forced into v1; gossip only DETECTS after the fact, doesn't prevent a one-time targeted swap. | **DEFER** to a follow-on ADR; v1 ships the append-only signed log + day-one STRs so the upgrade is non-breaking (0009's adopt-format-now cadence). |
| **Literal OpenPGP/GnuPG + SKS keyserver** (*also rejected in [ADR 0009](0009-p2p-ledger-sync.md)*) | DISCOVER+TRUST the classic way: OpenPGP packet identity/encryption + an SKS gossip keyserver web-of-trust. | The conceptual model the owner cites (keypair=identity, fingerprint=address, sign+encrypt, keyserver=directory). | `x/crypto/openpgp` frozen/deprecated; alternatives drag a heavy fork or cgo-Sequoia; zero interop value (kasas peers only talk to kasas peers); SKS write-anything died to CVE-2019-13050; web-of-trust is the wrong UX for a single-user homelab. | **REJECT** the wire format + SKS architecture + web-of-trust by name; ADOPT only the MODEL, realized with ed25519 + age. |

## What this ADR deliberately does **not** do

- **No *mandatory* central server.** The directory/relay is opt-in and self-hostable
  (D2); 0009's direct path stays the default for reachable peers. This **revises**, it
  does not delete, 0009's "no central discovery server" — and only for the opt-in
  hosted case.
- **No public-stranger data exposure.** Discovery is **consent-gated** (D3), not a
  broadcast network — "global discoverability is a misfeature" (0009) still holds. No
  list/scrape endpoint; lookup needs the exact handle or high-entropy fingerprint.
- **No plaintext at the relay, ever.** The relay sees only ciphertext + routing (D1).
  It is **not** a smart server and does no content logic (the SKS lesson).
- **No literal OpenPGP/GnuPG, no web-of-trust, no MLS/Noise ratcheting.** Keep the GPG
  *model* (ed25519 + age), reject the wire format; consent replaces web-of-trust;
  long-lived keys with coarse-FS rotation, not a group ratchet, for async
  store-and-forward of self-contained objects.
- **No full Key-Transparency in v1.** Ships TOFU + safety-number + an append-only
  signed log (with day-one STRs so the upgrade is non-breaking); full CONIKS Merkle
  inclusion/equivocation-proof gossip + VRF index-hiding is a named follow-on.
- **No sealed-*sender* anonymity beyond what sign-then-encrypt gives in v1**, no
  mixnet, no traffic-analysis guarantee. The relay still learns recipient + size +
  timing, and the directory still learns the lookup graph + the fingerprint→`mailbox_url`
  linkage; "zero-knowledge" means zero-*plaintext*, not zero-metadata.
- **No delivery-completeness guarantee against a withholding relay.** A zero-knowledge
  relay can neither read nor forge a share — but it **can silently drop or selectively
  withhold** one, and best-effort polling with no signed monotonic per-connection
  sequence can't *detect* a withheld update (distinct from the traffic-analysis caveat
  above). A **signed monotonic counter in the envelope tuple** is the named near-term
  hardening that makes a gap detectable; v1 leans on the operator's
  reputation/replaceability (D2) and the durable-pull catch-up.
- **No forward secrecy.** Identity/recipient keys are long-lived (no ratchet — rejected
  below for async store-and-forward of self-contained objects), so any party that logs
  ciphertext can decrypt **all** past traffic retroactively if a seed ever leaks once.
  Rotation governs the **future** only; it does not protect already-captured ciphertext.
  A future per-connection ephemeral-key wrap is the named upgrade.
- **No new object model for invoices.** They ride [ADR 0004](0004-transaction-document-artifacts.md)
  extension namespaces.
- **No cryptographic recall.** Inherited from 0009 — once a blob is delivered+decrypted
  it cannot be un-sent; rotation/revocation govern the future only. A
  tombstone/retraction for upstream [ADR 0007](0007-transaction-soft-delete.md)
  soft-delete over the mailbox is still the open question 0009 flagged, now with an
  encrypted-transport wrinkle.
- **No SSSS-style operator-blind key backup in v1** (named as an open recovery
  convenience). Lose the vault seed and the fingerprint + pins are gone.

## Consequences

- **The `peer` source gains one transport + one decrypt step** — and nothing else in
  0009 changes. Subscribe still == add a `Puller`, share still == save a query;
  dedup/events/rules/history/provenance/origin-guard stay 100% free.
- **Attribution becomes proven.** Adopting the ed25519 fingerprint (0009's pre-adopted
  format) upgrades `shared_by` from HMAC-asserted to signature-proven **with no wire
  break**, closing 0009's v1 honesty caveat.
- **One new dep (`filippo.io/age`)** — it must clear govulncheck and the supply-chain
  bar (pin the version; X25519 recipients only — no scrypt path, no agessh). ed25519 /
  SHA-256 / base32 are stdlib.
- **One new table (`peer_connections`, mig 00019)** — public pins + consent state only;
  keypairs stay in vault (no migration). Lean storage, comprehensive exposure.
- **A new hosted server now exists** — even zero-knowledge, a **data processor** with a
  privacy/DPA/retention/deletion obligation. This is a real philosophical tension 0009
  avoided, reconciled (not erased) by D1+D2+D3.
- **Two crypto layers** now coexist: the 0008/0009 relay-hop HMAC and the age+ed25519
  E2E layer above it — authenticating different things, never conflated.
- **A first-contact MITM window remains** until the out-of-band safety-number is
  compared; the deferred key-transparency log is the real fix. v1's honesty: detectable
  after pin, preventable by safety-number, not cryptographically impossible — and the
  unconditional zero-knowledge claim is scoped to *verified* connections, not asserted
  flat.
- **Two residuals are admitted, not hidden:** a withholding relay can drop (never read
  or forge) a share undetectably without a signed sequence counter, and long-lived keys
  mean **no forward secrecy** (a one-time seed leak retroactively decrypts any captured
  ciphertext). Both have named hardening upgrades (a monotonic envelope counter; an
  ephemeral-key wrap); neither is a v1 commitment.

## Alternatives considered

The *discover / relay / trust-the-key* mechanisms are compared in the **Methods
compared** table above (which 0010 grafts as a mailbox row onto
[ADR 0009](0009-p2p-ledger-sync.md)'s methods table). Below are the three **full-design
stances** weighed for the overall shape, plus the notable name-and-reject calls.

**The three full-design stances** (the primary was chosen; the other two are adopted as
the named hardening upgrade and the structural direction):

- **Minimal encrypted mailbox + verified directory (chosen).** The leanest correct
  realization of D1+D2+D3 and the cleanest layering on 0009: a sign-then-encrypt dumb
  mailbox (relay sees only ciphertext + routing), a published protocol + tiny stateless
  reference server (AWS as default MX), and a sealed accept-before-bytes connection
  request. One new dep (`age`), ed25519 from stdlib, keys in vault (no migration),
  exactly one new table, invoices ride [ADR 0004](0004-transaction-document-artifacts.md),
  crypto honest (TOFU + safety-number now, KT deferred). **Chosen** as the v1 spine.
- **Key-Transparency-backed directory (Stance B).** Strongest on the malicious-directory
  threat — Merkle inclusion proofs + hash-chained signed tree roots + gossip make the
  directory provably-not-a-trust-root. But it front-loads the heaviest machinery into v1,
  fighting the lean ethos and 0009's "adopt the format now, build the heavy upgrade
  later" cadence. It is strictly the chosen spine **plus** a deferrable trust layer pulled
  forward. **Adopted as the named directory-hardening upgrade**, deferred to a follow-on
  ADR; v1 ships the append-only log + day-one STRs so it lands non-breaking.
- **Federated `ledger@directory-host` (Stance C).** The best articulation of D2 — handle
  federation makes identity the key, not the host, so the hosted service is
  trusted-because-replaceable (the ethos *and* the business win, tying to the existing CLA
  dual-licensing). Same lean crypto + storage as the chosen spine. But full
  server-to-server federation is a long-tail tax (versioning, defederation, a spam arms
  race) that v1 wouldn't exercise. **Adopted as the structural direction** — `handle@host`
  + WKD self-publish ship in v1, cross-directory resolution deferred until a second node
  exists.

The notable name-and-reject calls:

- **Literal OpenPGP / GnuPG wire format.** Adopt the *model*, reject the *bytes*.
  `golang.org/x/crypto/openpgp` is frozen/deprecated (the Go team named ed25519 +
  modern AEAD the replacement); the alternatives drag a heavy fork or cgo-Sequoia; and
  kasas peers only ever talk to kasas peers, so OpenPGP's subkey/UID/expiry packet
  machinery buys **zero** interop for real weight. ed25519 + age reproduce the GPG model
  with one audited pure-Go dep. **Rejected (wire format); adopted (model).**
- **SKS keyserver architecture.** Write-anything, never-delete, accept-any-third-party
  -signature — the design that died to CVE-2019-13050 certificate-flooding. **Rejected
  by name**; the directory instead requires **proof-of-control on publish**, supports
  **revocation**, and stores **no third-party annotations** (the keys.openpgp.org
  correction).
- **Keybase.** Centralized, social-proof sprawl, acquired and stagnated — the D2
  anti-pattern. **Rejected by name**; federation (handle@host + WKD self-publish) is the
  answer.
- **Matrix homeserver / federation.** Enormous (room state, Megolm group ratchet,
  federation trust-surface, a documented E2EE-CVE history), and a share is point-to-point,
  not a multi-party room. **Rejected**; borrow only the SSSS encrypted-key-backup pattern
  as a deferred recovery convenience and the "homeserver sees only ciphertext"
  confirmation.
- **libp2p Kademlia DHT / ActivityPub-WebFinger.** Global discoverability is a misfeature
  for a private ledger (already rejected in 0009); ActivityPub is public-social-shaped
  with no NAT story. **Rejected** (borrow only Follow/subscribe wording, already in 0009).
- **`nacl/box` instead of age.** Lower-level: no native multi-recipient (multi-device
  would be N hand-rolled boxes), manual-nonce footgun, no streaming. **Offered as a named
  fallback only.**
- **Encrypt-then-sign.** Leaks the sender's fingerprint in cleartext at the relay.
  **Rejected** for **sign-then-encrypt** (signature inside the ciphertext = sender hidden
  from the relay), with `recipient_fp` bound inside the signed tuple to block
  surreptitious-forwarding.
- **Full CONIKS Key-Transparency in v1.** The airtight malicious-directory defense, but
  heavy (Merkle inclusion + VRF index-hiding + a witness/gossip network). **Deferred** to
  a follow-on ADR; v1 ships TOFU + safety-number + an append-only signed log with day-one
  STRs so the upgrade is non-breaking — the same "adopt the format now, build the heavy
  upgrade later" cadence 0009 used for the ed25519 fingerprint.

## Open questions

- **Sealed-sender vs sealed-recipient for v1.** Sign-then-encrypt already hides the
  signer from the relay, but the subscriber knows the publisher via the accepted
  connection anyway — is a separate routing-anonymity layer worth v1 complexity, or
  deferred?
- **Key-transparency depth.** v1 ships the log + day-one STRs; when does full Merkle
  inclusion/equivocation-proof gossip + VRF index-hiding land, and who runs witnesses so
  a lone homelab client gets enough gossip diversity to catch equivocation?
- **Multi-device UX.** Per-device keypair + age multi-recipient + old-signs-new
  cross-signing is named, but device add/remove/revoke and keeping the directory bundle
  consistent without a per-peer re-verify is unspecified.
- **SSSS-style operator-blind encrypted key backup** as an optional recovery convenience
  — in or out of v1, and does it muddy the zero-knowledge story?
- **Quota / TTL / rate-limit / proof-of-work defaults** — reuse 0009's leaked-token DoS
  reasoning, but the concrete numbers need tuning against the AWS cost model.
- **Regulatory posture** — DPA, AWS-region pinning for EU users, lawful-intercept answered
  with metadata only, whether handles/fingerprints count as PII, and how the deletion path
  interacts with TTL-expired-but-uncollected blobs.
- **Invoice-byte relay** — when a subscriber can't reach the publisher's external DMS,
  should the share inline/re-host the encrypted blob, or only the itemization?
- **The greeting field** is attacker-controlled text shown at the trust-decision moment —
  length-cap + inert rendering are mandatory; confirm no markdown/HTML path.
- **Tombstone/retraction for upstream [ADR 0007](0007-transaction-soft-delete.md)
  soft-delete** over the encrypted mailbox — still 0009's open question, now idempotent +
  E2E-encrypted.
