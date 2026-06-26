# ADR 0010 — Custody-free internet peer connectivity (Syncthing model: direct-first, community relay fallback)

- **Status:** Proposed (scoping only — not yet built)
- **Date:** 2026-06-26
- **"Custody-free" means:** kasas never holds or sees a byte of **financial** data on
  any tier (E2E always). It **does** operate thin, content-blind, overridable
  relay/discovery defaults that carry a **bounded transient-metadata duty** — not zero
  (see s9). Read the title as "no financial-data custody," not the retired "kasas
  operates nothing."
- **Related:** [ADR 0009](0009-p2p-ledger-sync.md) (selective peer-to-peer ledger
  sharing — **the direct parent this ADR extends**; 0010 reuses its share contract
  verbatim, realizes its deferred ed25519-attribution upgrade, and **preserves its
  no-MANDATORY-central-server principle** — direct-first, every default overridable and
  self-hostable — while honestly adding **optional, content-blind** relay/discovery
  defaults),
  [ADR 0008](0008-inbound-webhook-source.md) (the inbound-webhook source + the
  `"<timestamp>.<body>"` HMAC scheme + the open-but-authed `Receiver`/`Delivery`
  ingest seam first contact rides — whether direct or via a relay — and the
  secret-in-`vault` precedent the keypairs and the relay join token follow),
  [ADR 0002](0002-plugin-network-capability.md) (the netfetch SSRF gate whose
  validate-then-dial helper the `peer` client reuses — plus **0009's** per-peer
  egress allowance, a netfetch sibling 0009 introduces and 0010 inherits, now central
  because the dial target is frequently a `100.64.0.0/10` Tailscale name **or a relay
  endpoint**),
  [ADR 0005](0005-plugin-originated-transactions.md) (the auto-purge-on-uninstall
  precedent the compromised-peer teardown mirrors),
  [ADR 0004](0004-transaction-document-artifacts.md) (how invoices / receipts ride
  document+itemization extension namespaces — **not** a new object model),
  [Ingestion architecture](../ingestion.md) (the `peer` `Puller` that gains one
  internet-reachable address, possibly behind a relay),
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

**A first revision of this ADR** (the immediately prior, still-on-record draft) closed
that gap with a **custody-free, direct-only** posture: ed25519 + age identity, an
out-of-band signed bundle for discovery, a recipient-bound consent gate, and the share
delivered **only** when both peers were online and **at least one of them brought its
own front door** (BYO: shared tailnet / Tailscale Funnel / Cloudflare Tunnel / VPS /
dyndns). That draft's spine was an absolutist claim — *"kasas operates NO server that
touches user data; the data-processor obligation is eliminated structurally."* It got
there by going **further** than Syncthing on two axes: it dropped relays entirely
(direct-only) and ran **nothing at all**, not even an optional discovery node.

The owner pushed back, verbatim:

> *"But Syncthing works without Tailscale. Syncthing relies on a network of
> community-contributed relay servers. How do we also achieve this?"*

The objection is correct and concrete. The direct-only draft forces **every receiving
peer** to BYO a reachable front door — real friction, and exactly the friction
Syncthing **does not** impose: a fresh Syncthing install syncs across the open internet,
behind double-NAT, out of the box, because the project **seeds a pool of
community-contributed relay servers** that bridge two peers neither of which is
directly reachable. The owner then **chose**, verbatim:

> *"kasas seeds a few content-blind relays (Syncthing Foundation posture): run a
> handful of content-blind, nothing-at-rest relays so it works out of the box. You take
> on a THIN obligation (a privacy policy, transient IP/metadata) — far lighter than the
> rejected mailbox (no stored data, no retention, no deletion path), but not literally
> zero."*

So this revision **un-rejects** the community relay pool and the seeded discovery node
the prior draft refused, and **softens the "operates nothing" spine** to an honest,
bounded claim. It is otherwise additive: every byte of 0009's share contract and the
prior draft's identity / consent / crypto / storage machinery is reused unchanged.

### The linchpin, stated up front: a LIVE relay is not the store-and-forward MAILBOX

Before any un-rejection, the one distinction the whole revision rests on. The owner
**still refuses** a hosted *store-and-forward mailbox* — a server that accepts ciphertext
from an **offline** recipient's sender and **holds it at rest** until collection. That
mailbox was rejected for one reason, restated verbatim:

> *"Even zero-knowledge, a data processor with a privacy/DPA/retention/deletion
> obligation seems to be a real problem. I don't want to have to handle any of that."*

A **Syncthing-style relay is categorically different**. It is a **live passthrough, not
a mailbox**: it forwards an already-end-to-end-encrypted stream between two peers who
are **both online at the same moment**, and it holds **no user content and no
per-recipient spool at rest** — when either disconnects it retains no traffic. The
single load-bearing difference is **held-at-rest vs forwarded-in-flight**:

| | Store-and-forward **mailbox** (rejected) | Live **relay** (adopted here) |
|---|---|---|
| When it acts | recipient **offline**; sender deposits | both peers **online at once** |
| What it holds | ciphertext **at rest**, until collection | **no content, no spool**; traffic dropped on disconnect |
| Duties incurred | retention policy, deletion path, breach-of-content, DSAR-over-content | a thin transient-metadata duty (s9): privacy policy + abuse contact + no-logs posture |

A live relay relaxes **exactly one** thing: it loosens 0009's *"at least one peer must
be reachable"* to *"both peers online at once"* (it bridges two NATed-but-online
sockets). It does **not** reintroduce offline/async delivery, and it creates **no
user content and no per-recipient spool at rest** — so the still-**locked** "no async /
no store-and-forward mailbox / no content at rest" rule survives intact. This is
precisely why the relay is adoptable while the mailbox stays rejected, and why the whole
"does-not-do" mailbox rejection below is **not** contradicted by the relay tier.

### The honest spine — thin and bounded, not zero

The prior draft's absolutist *"kasas operates nothing / obligation eliminated
structurally"* is now **false** for the seeded relay/discovery tier and is retired. The
honest replacement, which everything below is built around:

> **kasas operates ONLY a content-blind, stateless relay/discovery tier and is NEVER a
> custodian of financial data. The HEAVY mailbox duties — retention, deletion path,
> breach-of-content, DSAR-over-content — are eliminated structurally (no content is
> stored, nothing financial is ever seen, E2E always). The residual is a THIN but
> non-empty obligation for the defaults kasas chooses to seed: a privacy policy
> covering transient IP + connection metadata, an abuse-report contact/process, a
> documented lawful-process posture (a deliberate no-connection-logs design, so the
> defensible answer to compulsion is "we kept nothing," not an accident), and the
> ordinary operator-liability surface of running an internet-reachable relay. There is
> no MANDATORY central server — direct-first works with everything disabled, and every
> default is overridable and self-hostable.**

Routing IP-addressed traffic is "processing" under GDPR — per CJEU *Breyer*
(C-582/14) an IP:port is personal data for a party who can identify the subject — which
is exactly why anyone operating **Syncthing-style relay/discovery servers** (which
process device IDs + source IPs) carries a privacy obligation. So the honest claim is
**not** "zero obligation"; it is "thin and bounded, far lighter than the mailbox it
replaces." **"Custody-free" remains accurate for FINANCIAL DATA** (the user's core
concern): kasas never holds or sees a byte of it, on any tier.

Three decisions are **locked** going in and are designed *around*, not re-litigated:

- **(L1) Direct-FIRST, relay-FALLBACK; still no async, no store-and-forward mailbox, no
  content at rest.** Direct (LAN / shared tailnet / a BYO front door) is **primary** when
  peers can reach each other. A content-blind **live** relay is the **fallback** for the
  both-online-but-neither-reachable (double-NAT) case the prior draft left
  unsupported. The relaxation is precise — *"at least one peer reachable"* becomes
  *"both peers online at once"* — and **nothing else**: no offline mailbox, no spool, no
  ciphertext at rest. A ledger reconciling via durable idempotent pull still tolerates
  "syncs next time we are both up."
- **(L2) Pure OSS, donations only.** No paid service of any kind. The seeded relays and
  discovery nodes are **donation-funded defaults** (Syncthing Foundation posture), **not**
  a paid tier; all custody-based monetization stays dropped.
- **(L3) kasas operates ONLY content-blind, stateless, overridable defaults.** kasas
  ships software + published protocols + reference binaries anyone can run, and **seeds
  a handful of content-blind relay and discovery nodes** so it works out of the box.
  Everything is self-hostable + operator-overridable + disable-able, and **direct-first
  works with them all turned off** — so there is **no MANDATORY central server**. The
  absolutist *"kasas operates nothing"* is retired; the accurate claim is the one above.

The resulting architecture stays small. It is **"0009 + a keypair + a doctrine + a live
relay fallback"**: cryptographic identity (ed25519 + X25519/age), cross-NAT internet
reachability with **BYO front door as an optional optimization**, and a content-blind
relay/discovery tier for the case BYO previously forced onto the user. It is **purely
additive** to 0009's share contract — and it **preserves** 0009's no-mandatory-central
principle while honestly adding optional defaults, a soft convenience like Syncthing's,
not a mandated dependency.

## Decision

Add **custody-free internet peer connectivity** as **a keypair, a direct-first
reachability doctrine with a content-blind relay fallback, a seeded discovery tier, and
a consent gate**, layered under [ADR 0009](0009-p2p-ledger-sync.md)'s share contract.
The wire payload stays 0009's signed `ImportBatch`, *subscribe == add a source*,
*share == save a query*, and the `peer` `Puller` polls the publisher's feed at an
internet-reachable address the publisher already runs **or via a relay**. Identity is
the **GPG model with modern pure-Go primitives** (ed25519 + age); there is **no
keyserver and no mailbox**, and the relay/discovery tiers are **content-blind, stateless,
self-hostable, and overridable**. **The seeded relay/discovery defaults are ON by
default** (Syncthing parity, direct-first, per-instance disable-able + overridable, with
a loud dashboard disclosure) — so the works-out-of-the-box guarantee and the obligation
analysis below rest on a committed posture, not a pending one.

### 1. Identity — the GPG model, modern Go primitives (realizes 0009's proven attribution)

*(Unchanged from the prior draft — the relay touches none of it; identity is established
peer-to-peer before any relay is dialed.)*

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
the first 20 bytes of `SHA-256(ed25519 pubkey)` — 32 chars, well under 0009's <=50-char
`peer:<id>`/`shared_by:<id>` label budget, QR-friendly, and **length-stable across a
future key-type change** because it hashes the key rather than embedding it. This is
the value 0009 pre-adopted, so adopting it here **realizes 0009's deferred upgrade**:
`shared_by` moves from *HMAC-asserted* to *cryptographically proven* **with no wire
break** — the spine of "purely additive to 0009." The human form is a **Signal-style
grouped-decimal "safety number"** (+ a word-list) for out-of-band comparison.

The X25519 key is **bound to the ed25519 identity by an ed25519 self-signature over
the `(ed25519_pub, x25519_pub)` tuple as presented in the out-of-band bundle / connect
request** — *not* over a directory entry, because no keyserver exists. A substituted
encryption key fails verification regardless.

> **Syncthing citation precision.** Syncthing's Device ID is `base32(SHA-256 of the
> device's self-signed **certificate**, which wraps its ECDSA P-384 key)` — not a hash
> of the bare public key, and not ed25519. We cite the **model** (id = a stable hash of
> the device's long-lived key material); kasas hashing the raw ed25519 pubkey is a
> deliberate cleaner variant of the same idea. The seeded discovery node (s3) re-uses
> this binding to make itself safe to operate — see there.

**Storage:** both private seeds live in `vault.SecretStore` under two fixed keys
(`peer.identity.ed25519_seed`, `peer.identity.x25519_seed`) via the existing
`SetSecretValue`/`SecretValue` — **no migration**, the
[ADR 0008](0008-inbound-webhook-source.md) secret-in-vault precedent. A private key
**never** lands in a DB column or the connections table. (The relay **join token**, where
a token-gated relay needs one, follows the same precedent: a vault secret under
`peer.relay.join_token`, never a column.)

### 2. Reachability — direct-FIRST, relay-FALLBACK (BYO becomes an optimization)

Syncthing's exact tiering: **try direct, fall back to a relay**. kasas operates a
content-blind relay tier (s2b) and seeds a few defaults, but it is **only ever touched on
fallback** — direct stays primary because it is lowest-latency and **no third party
touches even ciphertext**.

**Connection sequence:**

1. **Try DIRECT.** LAN / a shared Tailscale tailnet / the peer's BYO front door (the
   prior draft's whole BYO machinery, retained verbatim as the **direct tier**). When
   peers can reach each other this is preferred and no relay is involved.
2. **Fall back to a RELAY** only if direct fails **and both peers are online** — pick a
   relay the target has **joined** (its advertised relays intersect the local enabled
   pool, s2b), run the session-key rendezvous, and bridge ciphertext. This serves the
   both-online-but-neither-reachable (symmetric/double-NAT) case the prior draft
   deliberately forwent.

A peer that wants to **receive** shares still *may* expose its already-existing 0009 feed
via a BYO front door, ranked by homelab fit:

| BYO front door (now OPTIONAL) | Stable name | TLS terminates at | age envelope |
|---|---|---|---|
| **Shared Tailscale tailnet** (recommended direct path) | `peer-ledger.<tailnet>.ts.net` | kasas (WireGuard end-to-end) | optional |
| **Tailscale Funnel** | `*.ts.net` public name | Tailscale edge | **mandatory** |
| **Cloudflare Tunnel** | hostname on the user's domain | Cloudflare edge | **mandatory** |
| **Cheap VPS / reverse proxy** (Caddy/nginx/Traefik) | the user's domain | the proxy (if it terminates) | **mandatory** if terminating |
| **dyndns + forwarded port** | dyndns name over a dynamic IP | kasas | strongly recommended |

But **BYO is no longer required to receive** — it is a **latency optimization**. A peer
with no front door registers a session with a relay (s2b) and advertises a `relay://`
address (s3); a counterpart then reaches it by **lookup -> relay-dial**, fully behind
double-NAT, out of the box. The prior draft's *"honest limit"* — that a purely-dynamic-IP
homelab peer with no stable name was *"genuinely not well served"* and the answer was
*"get a free stable name"* — **dissolves**: that peer is now served by the seeded relay
+ discovery pool. The requirement relaxes from *"both online and at least one reachable"*
to **"both online at once."**

**Idempotency holds across tiers.** A share arriving via a relay carries the **identical**
namespaced id `peer:<fp>:<origExtId>` as the same share arriving direct, so the 0009
`ON CONFLICT (id)` dedup makes a direct-first -> relay-fallback re-delivery of the **same**
row a no-op. The relay never double-inserts.

### 2b. The relay tier — live, content-blind, nothing-at-rest content (the `strelaysrv` analog)

kasas publishes a **relay protocol**, ships a **relay binary** anyone can run, and
**seeds a handful of donation-funded default relays**. A relay forwards the
age+ed25519 stream between two **authenticated** kasas peers who both want to connect:
content-blind, **live only**, **no user content at rest**. Mapped to kasas primitives, the
protocol is two-mode (first-byte-keyed), mirroring `strelaysrv`:

**(1) CONTROL channel (TLS).** A peer that wants to be **reachable-via-relay** opens a
long-lived control connection and sends a signed **JoinRelay** request, presenting its
`peer:<fp>`:

```
join = ed25519_sign(seed, "kasas-relay-join/1" || relay_host || peer_fp || now)
```

The relay validates the signature against the presented `ed25519_pub` — confirming
`peer_fp == base32(SHA-256(ed25519_pub)[:20])`, the s1 binding — and, **in token mode**,
a relay **join token**. On success the peer is **registered as reachable-via-this-relay**
(memory only, dropped on control-channel close). The relay learns only the fingerprint
+ the peer's source `IP:port`.

**(2) SESSION rendezvous + bridge.** To reach Bob, Alice sends the relay a
**ConnectRequest** naming Bob's `peer:<fp>`. The relay (a) confirms Bob currently holds a
control channel here (else "relay cannot reach Bob"), (b) mints a **one-time random
session key**, (c) places a `SessionInvitation{session_key, relay_data_addr}` in Bob's
control-channel outbox **and** returns the same to Alice. The relay opens a forwarded
**data session only when BOTH Alice and Bob separately dial the data port and EACH
present the SAME session key**.

This is the **open-proxy guard**: the relay can **never** be aimed at an arbitrary
third-party destination — it only splices two kasas peers who **both** authenticated
(signed JoinRelay + optional token) **and** both dialed in for the **same minted
session** (Syncthing's exact *"only bridges two devices that both connected for the
purpose"*). No JoinRelay + no matching session key => no forwarding.

**What flows over the bridge:** the s6 age sign-then-encrypt, **recipient-bound**
envelope — the `connect`/`connect_ack` handshake when first contact is via a relay
(s4), then the 0009 `ImportBatch` feed/push (s5). The relay forwards **ciphertext only**.
The `recipient_fp` bound **inside the signed bytes** makes a relayed `connect` exactly as
safe as a direct POST: a relay (or anyone capturing the stream) **cannot re-aim a captured
`connect`** at a third peer, and **cannot read plaintext**.

**Live-only / nothing-at-rest content (the invariant that keeps the obligation thin).**
All session state lives in **memory only** — control channels, pending invitations, live
spliced sessions. The relay **persists no user traffic to disk**; in-flight buffers are
bounded and **dropped on disconnect**; once either peer disconnects it holds **no
content for that session**. There is **no spool, no retry-for-an-offline-peer queue, no
TTL'd outbox** — any of those silently re-creates the rejected mailbox and is
**forbidden as an invariant**. State a relay binary *does* keep, stated honestly so
"nothing at rest" is not overclaimed: its **own** TLS cert/key, and **bounded,
non-content aggregate stats counters** (e.g. bytes-forwarded, active sessions) which are
operational metrics, not user traffic. **Locked commitment for the kasas-seeded
defaults:** they **do not retain connection-pair logs** — any operational logging is
strictly minimized and rotated, and the deliberate no-logs design is what makes the
"thin obligation" and the lawful-process posture (s9) honest rather than accidental.

**Content-blind BY CONSTRUCTION — stronger than Syncthing.** Syncthing's relay is
content-blind only because it rides *inside* device-to-device TLS (the relay isn't a TLS
endpoint). kasas's payload is **app-layer E2E encrypted** (age sign-then-encrypt,
recipient-bound), so a kasas relay is content-blind **even if it were a TLS-terminating
endpoint**. It sees **two fingerprints + ciphertext + size + timing + transient
`IP:port`** — **never plaintext financial data, never financial content at rest**. The
s6 envelope is now the **linchpin** that makes the relay safe.

**Pool / seeding.** kasas seeds a small **donation-funded** default relay pool (a
published, operator-fetchable list); community relays **auto-register** into the public
pool; operators may add **self-hosted** relays, **override** the default list, and
**disable** the tier. Default-**ON** but per-instance disable-able, **direct-first** so a
relay is only touched on fallback, and dashboard-surfaced (which relays are active, a
**direct-vs-via-relay** indicator on each connection, one-click disable) so the
self-hosted-ethos audience can opt out cleanly.

**Abuse / security (mandatory — an open relay is dangerous; mirror `strelaysrv`
precisely or seeding one inflates the very obligation this revision keeps thin):**

- **Not an open proxy / amplifier:** forwarding requires both peers to **each
  authenticate** (signed JoinRelay + optional relay token) **and** both present the
  **same minted session key**; **no arbitrary-destination forwarding**.
- **Pre-auth control-channel cost/rate limit:** the JoinRelay/ConnectRequest control
  channel is reachable by any internet host, so **signature-verify and session-mint
  attempts are rate- and cost-bounded per source IP** — otherwise the relay is a cheap
  pre-auth CPU/socket-exhaustion target (the connection-ceiling below bounds sockets,
  not pre-auth verify cost).
- **Rate / bandwidth / resource caps:** a global token-bucket bytes/s cap; a per-session
  token-bucket bytes/s cap; per-peer-`fp` connection/session quotas; an idle/network
  timeout tearing down a no-data session; a control-message timeout; a keepalive ping
  interval; and a **connection ceiling** refusing new JoinRelay near ~80% of the OS
  file-descriptor limit.
- **Relay token model:** a seeded/community relay may run **open-join** (any `fp` may
  register, gated by the signed JoinRelay + rate-limits/quotas) **or** **token-gated**
  (the join token a vault secret under `peer.relay.join_token`) for private/self-hosted
  relays. Either way the session-key rendezvous is the inviolable splice guard.

**Honest concession (open-join is a free two-party tunnel for consenting identities).**
The splice guard **prevents** reflection/amplification and arbitrary-destination
proxying. It does **not** prevent two parties who **both** run kasas identities from
pushing whatever bytes they like through the age channel — an open-join relay **cannot
distinguish** genuine ledger sync from two consenting kasas identities tunneling
unrelated encrypted traffic. That is **mitigated, not prevented**, by the bandwidth /
quota caps and the **token-gated** option, which seeded public relays may prefer for
exactly this reason.

**Lean storage:** relay-pool **selection** on the ledger side is **settings/config** —
keys `peer.relay.pool` (the relay list) and `peer.relay.enabled` (default `true`),
registered as settings `Definition`s (the `market.series` precedent: direct
`UpsertSetting`, immediate). **No new ledger table.** The relay binary is a separate
server with **ephemeral in-memory** session state, **not** ledger schema.

### 3. Discovery — seeded, content-blind `fp -> address` phone book (the `stdiscosrv` analog)

The out-of-band **signed bundle stays PRIMARY** and complete on its own (it carries the
**keys**). The phone book is an **optional convenience tier** that resolves **only the
current address** for a fingerprint the looker-up already holds — and kasas now **seeds a
few default nodes** so it works out of the box, while keeping them **self-hostable +
overridable + disable-able**.

Exchange a **signed bundle** `{fingerprint + stable address + inline ed25519_pub +
x25519_pub + minted_at}` once via paste/QR (generalizing 0009's feed-invite). Keys are
carried **inline** (Autocrypt-style) so the recipient TOFU-pins from the bundle itself —
there is **no server to fetch a key from**, ever:

```
bundle = base64url( ed25519_sign(seed, T) || T )   # sign T directly; ed25519 hashes internally
T      = "kasas-peer-id/1" || fingerprint || ed25519_pub || x25519_pub
                            || address || minted_at
```

The recipient verifies `T`'s self-signature against the inline `ed25519_pub`, confirms
`fingerprint == base32(SHA-256(ed25519_pub)[:20])`, and pins
`{fingerprint, ed25519_pub, x25519_pub}`. A tampered bundle fails verification.

**Phone-book protocol (ASCII-only):**

```
announce  POST /disco/v1/announce
          body = ed25519_sign(seed, "kasas-disco/1" || fingerprint || address || ttl || now)
                 || { fingerprint, address, ttl, now }
lookup    GET  /disco/v1/<fingerprint> -> { address, signed_at, ttl, sig }   # 404 if absent/expired
```

Announce re-runs on a server-returned `Reannounce-After` cadence (~30 min, Syncthing's
global-discovery cadence); rows are ephemeral and **TTL-expire** if not refreshed. The
`address` is **opaque to the server**: a direct BYO URL **or** a relay address
`relay://relay.host:port/?peer=<fp>`. A no-front-door peer registers a session with a
relay (s2b) and announces that `relay://` address, so **lookup -> relay-dial** reaches a
fully-NATed-but-online peer out of the box — exactly the case s2's prior "honest limit"
left unserved.

**The load-bearing security property (kept verbatim — it is what makes seeding a default
node safe).** The node stores **only** ephemeral, signed, TTL'd `fp -> address` rows —
**never** `fp -> key`, never any financial data. The key is baked into the fingerprint via
`fingerprint == base32(SHA-256(ed25519_pub)[:20])`, so a malicious or compelled discovery
server **cannot substitute a key**: a wrong key cannot produce the requested fingerprint.
The worst it can do is **withhold or lie about an *address* = denial, not impersonation.**
A looker-up **must** verify the announce signature against the **pinned** `ed25519_pub`
before dialing, and the s4 TOFU/safety-number consent gate runs on connect — so a
lied-about address that points at an attacker fails (the attacker presents his **own**
fingerprint, which won't match the pin).

> **Syncthing precision (cite the model, not the bytes).** `stdiscosrv` derives the
> device ID from the client's presented **mutual-TLS certificate**. kasas instead carries
> the fingerprint in a **self-signed announce body** the **looker-up** verifies against the
> fingerprint it already holds — a deliberate cleaner variant of the same anti-spoof
> property (mirroring how s1 hashes the bare ed25519 pubkey rather than a cert).

**Enumeration guard + honest leak parity.** Lookup is keyed by the **exact** 32-char
high-entropy fingerprint; there is **no list/enumerate endpoint** and no prefix/range
scan, plus per-IP lookup rate-limits + `429`. So a *stranger* cannot scrape the social
graph. But the **node operator** still sees, with the **same candor owed the relay**:
`announcer-IP -> fingerprint` (who is reachable, from where) and
`querent-IP -> queried-fingerprint` (who is looking for whom) — a **partial
who-connects-to-whom graph** for peers that use it, the **same class of metadata leak as
the relay's fingerprint-pair exposure**, and the **same reason a privacy policy is owed**
(s9). Do not let the relay carry the full candor while discovery reads as benign; the
discovery node is graph-exposing too.

**Seeded + overridable (lean storage).** kasas **seeds** a few default discovery nodes +
ships a bootstrap list (works out of the box) + ships the discovery **binary** anyone can
self-host; the node list is operator-**overridable** and the whole tier is
**disable-able**. Ledger-side config: settings keys `discovery.enabled` (default `true`)
and `discovery.servers` (CSV, default = the seeded set), registered as settings
`Definition`s (the `market.series` precedent: direct `UpsertSetting`, immediate). The
node's TTL'd address map is **ephemeral in-memory** state (present-until-TTL, not "nothing
at rest" — stated honestly), **not** ledger schema; **no new table**. New event
`peer.discovery.announced` rides the bus; **no** `peer.discovery.keychange_detected`
(there are no keys at the phone book to change).

> **Positive property worth stating:** mapping `fp -> address` only (never `fp -> key`)
> *improves* the trust story — there is no key to MITM. Seeding a default node is therefore
> safe **by construction** for identity: the node is never trusted for identity, only
> consulted for a hint (the residual is the metadata leak above, not impersonation).

### 4. Consent — the connection request; no data before accept

The connection, not the share, is the unit of on-ledger state — and first contact needs
**no keyserver**, because it rides the **same reachability** the share will (a BYO front
door **or a relay**).

1. **Find.** Alice imports Bob's bundle (s3) — possibly after a discovery lookup that
   gave her his current `address` — verifies its self-signature, confirms the
   fingerprint, **TOFU-pins** `{fingerprint, ed25519_pub, x25519_pub}` into
   `peer_connections` (`status pending_out`, `direction out`), and ideally compares his
   safety number out-of-band.
2. **Request.** Alice sign-then-encrypts a **sealed** `connect` envelope whose signed
   tuple is `{type:"connect", from_fp, to_fp (Bob's fingerprint), ed25519_pub,
   x25519_pub, address (her own front door or relay address if any, else empty),
   greeting?, shared_at}` and delivers it to Bob: she **POSTs it directly to Bob's BYO
   address**, **or relays it to Bob via a community relay** (s2b) when neither peer is
   directly reachable — in both cases at the generalized
   [ADR 0008](0008-inbound-webhook-source.md) ingest route (`msg_type=connect`). Binding
   **`to_fp` inside the signed bytes** makes the request **recipient-bound**: Bob rejects
   any `connect` whose `to_fp` is not his own fingerprint, so a captured request — **even
   one that transited a relay** — cannot be surreptitiously re-aimed at a third peer. **The
   `connect` schema has no field that can carry financial data** — "no data before accept"
   is enforced by the **wire format**, not by policy. The age envelope keeps even Bob's
   TLS-terminating tunnel **and any relay** out of plaintext on this first byte.
3. **Accept / reject / block.** Bob's `peer` `Receiver.Receive(Delivery)` runs
   **in-handler** (open route, **not** dashboard-token-gated, **inert until an identity
   is minted** — the [ADR 0008](0008-inbound-webhook-source.md) pattern), decrypts to
   his X25519 seed, verifies Alice's ed25519 signature against the keys *she presented
   inline* (TOFU — no directory copy to trust), and surfaces a **pending request**. Bob
   must **explicitly accept** before anything flows: accept writes a `peer_connections`
   row (`status accepted`), TOFU-pins, and returns a sealed `connect_ack` — over the same
   synchronous response (direct or relayed) if Alice has no front door, otherwise to her
   address; **reject** drops; **block** tombstones the fingerprint so future envelopes drop
   at receive. No accept => no row => **zero bytes ever flow**.
4. **Trust upgrade.** An out-of-band safety-number comparison flips `verified=1` (the
   "verified" badge). **Trust model:** L1 **TOFU-pin** (mandatory) + L2 **out-of-band
   safety number** (recommended financial default). PGP web-of-trust is rejected (wrong
   UX for a single-user homelab).

**No queued ack (L1).** If the synchronous socket closes before Bob accepts (direct or
relayed), the `connect_ack` is **not** held for later collection — holding it would be the
store-and-forward mailbox L1 forbids, and a relay explicitly drops it on disconnect.
Instead, on a deferred accept Bob's box either **initiates the reverse** `connect` to
Alice **when Alice has a reachable target** (her own front door, or a live relay session
she has registered) or Alice **re-sends** the idempotent, fingerprint-pinned request
within a fresh freshness window; the pending pin on each side makes the retry a no-op
write. If Alice is a pure-relay peer whose session has dropped, she has **no reachable
address**, so the reverse-connect path does not apply and the retry must originate from
Alice once she is online again — which folds under L1's "syncs next time we are both up."

**Replay.** Because the connect is a **synchronous** request/response — whether direct or
spliced live through a relay — a tight ed25519-signed +/-5-min freshness window suffices.
No async mailbox means no seen-set is needed.

### 5. The share — 0009 verbatim, with the envelope load-bearing

Once a connection is accepted, the share itself is **0009 unchanged** — the relay is
**pure transport**, the share contract is verbatim. The subscriber's `peer` `Puller`
polls the publisher's feed at the BYO address (`GET <address>/api/v1/peer/feed/{share}`,
the publisher evaluating `(storedSelector AND originGuard)` server-side and returning a
paginated `ImportBatch` + `next_cursor`) **or via a relay session**, or the publisher
pushes a one-txn `ImportBatch` to the subscriber's `Receiver`. Reused **verbatim** from
0009 (stated, not re-derived): the share = a saved search-query selector live by default;
subscribe = one `MultiCredentialed` entry; provenance R4 (`source=peer:<fp>`, the
`kasas.peer` extension `{origin_source, origin_external_id, sender_ledger_id,
shared_at}`, the `shared_by:<fp>` label — **now proven, not asserted**); the namespaced id
`peer:<fp>:<origExtId>` giving dedup disjointness via `ON CONFLICT (id)` (now also the
cross-tier idempotency guarantee, s2); balances zeroed before signing; the origin-guard
`NOT (source:peer: OR source:webhook:)` + its Go re-check; **GAP A** (the first-class
search `source` field) and **GAP B** (the persister folds `ImportTxn.Extensions`/`Labels`
into the born row); the per-peer egress allowance; redaction allowlists; compromised-peer
teardown `DELETE WHERE source='peer:<fp>'`; and dedup/events/rules/history all **free**.

> **One-line nod:** the per-peer egress allowance, which today grants exactly `host:port`
> for a direct front door, may now grant a **relay address** as the dial target — the same
> mechanism, the operator-typed value can be a relay endpoint.

**Why the age envelope is now the linchpin — not generic defense-in-depth.** A BYO
**TLS-terminating** front door (Cloudflare Tunnel, Tailscale Funnel, a terminating reverse
proxy) **and now a relay** sit in the path and would otherwise see plaintext financial
data. The age sign-then-encrypt envelope (s6) rides **on top of** the connection and keeps
the `ImportBatch` opaque **even to the user's own tunnel provider and even to the relay** —
which is exactly what makes the relay content-blind **by construction**. It is therefore
**mandatory** for any TLS-terminating BYO front door **and for any relayed connection**,
**optional only** on a pure WireGuard/tailnet direct path where no third party terminates
TLS. (See *Open questions* on surfacing this boundary.)

### 6. Crypto — ed25519 + age, sign-then-encrypt, recipient-bound

*(Kept verbatim and elevated: this envelope is the linchpin that makes the relay
content-blind.)* The publisher builds the 0009 signed `ImportBatch` `B` (balances zeroed),
then:

1. **Sign** (ed25519, length-framed canonical framing — never raw concatenation):
   `T = "kasas-peer-share/1" || sender_fp || recipient_fp || shared_at || sha256(B)`,
   `sig = Sign(seed, T)` — signing `T` **directly**, since stdlib `ed25519` already
   hashes its input internally. Binding **`recipient_fp` inside the signed bytes** blocks
   surreptitious-forwarding — including forwarding **through a relay**; `sha256(B)` gives
   idempotency.
2. **Encrypt** (age, to the recipient's pinned X25519 key):
   `ciphertext = age.Encrypt({sig, T, B})`. age natively supports **multiple recipient
   stanzas**, the wire path to multi-device.[^multidevice]

On receipt the subscriber decrypts, recomputes `sha256(B)`, checks `recipient_fp == me`
+ freshness, looks up `sender_fp` in `peer_connections`, and **verifies the signature
against the PINNED ed25519 key** — never a re-fetched key, never the body's self-claim.
**Only a pass** makes `source="peer:<sender_fp>"` / `shared_by:<sender_fp>` true (the
realized 0009 upgrade); every failure is a coarse drop. **Sign-then-encrypt** puts the
signature *inside* the ciphertext, so neither the BYO tunnel operator **nor a relay** can
see **who** signed.

[^multidevice]: Multi-device is **not free** at the storage layer — s7's
    `peer_connections` pins a **single** `pinned_x25519` per peer. Encrypting to N devices
    requires storing N pinned X25519 keys per connection (a future column/side-table), and
    is named, not built, here.

**One freshness clock.** Delivery is synchronous (direct or live-relayed), so a single
+/-5-min window suffices; replay beyond it is bounded by `ON CONFLICT (id)` dedup.

age was chosen over `nacl/box` (no native multi-recipient, manual-nonce footgun) and
over literal OpenPGP (*Alternatives*). Supply-chain: pin `age`; **X25519 recipients
only** — enforced **in kasas code** by constructing only the age `X25519Recipient` /
`X25519Identity` types and **never** the `scrypt` or `agessh` helpers (the age high-level
API links those packages, so X25519-only is implementation discipline, not an
API-enforced default); ed25519 / SHA-256 / base32 are stdlib.

**The key-swap threat largely evaporates.** With no keyserver there is nothing to MITM:
keys arrive via the signed out-of-band exchange / connect request, and the id binds the
key. The honest residual is the **first-contact** channel — a malicious party in the
out-of-band channel, or a phone book lying about an address to route Alice to an attacker
who presents his *own* fingerprint — defended by TOFU-pin + out-of-band safety-number
**before** accept, and by the fact that a wrong key cannot match a requested fingerprint
(fails closed). Full Key-Transparency is **cut**, not deferred: there is no keyserver to
make provably-not-a-trust-root.

### 7. Storage — lean-first

- **Keypairs** -> `vault.SecretStore`, **no migration** (s1, ADR 0008 precedent). A
  private key never lands in a column. The relay **join token** (where used) is likewise a
  vault secret (`peer.relay.join_token`), never a column.
- **Exactly one new ledger table — `peer_connections`** (migration **00019**, the next
  free number after 0009's proposed `peer_shares` at **00018**; the latest migration **on
  disk is 00017**, so **both 00018 and 00019 are proposed-only** and 0010 reserves 00019 —
  it does not yet exist). Multi-dialect sqlc, **ASCII-only SQL comments** to dodge the
  em-dash bug. It holds **only public pins + consent state, never a private key, never
  plaintext financial data**:

```sql
CREATE TABLE peer_connections (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,  -- BIGSERIAL on pg
    peer_fp         TEXT NOT NULL,                       -- peer:<id> ed25519 fingerprint (the pin + trust anchor)
    handle          TEXT NOT NULL DEFAULT '',            -- mutable LOCAL display alias, never project-allocated
    pinned_ed25519  TEXT NOT NULL,                       -- TOFU pin
    pinned_x25519   TEXT NOT NULL,                       -- TOFU pin (single key; multi-device needs many, see s6)
    address         TEXT NOT NULL DEFAULT '',            -- the peer's dial target: BYO front door OR a relay:// address
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

**Shares** stay on 0009's `peer_shares`; **subscriptions** stay 0009's
`MultiCredentialed` entries + per-source cursor (now referencing a connection by
fingerprint). **No new subscriber columns.** Crucially, the **relay and discovery are
separate server binaries with ephemeral in-memory state — NOT ledger schema.** Relay and
discovery **selection** on the ledger side is **settings/config** (`peer.relay.pool`,
`peer.relay.enabled`, `discovery.servers`, `discovery.enabled`) via the existing settings
`Definition` registry — **no new table** beyond `peer_connections`, and **no migration**
for the relay/discovery tiers at all.

### 8. Surfaces — full REST + MCP + dashboard parity

Optionally-disabled, so **read routes always register** and degrade calmly (the
disabled-subsystem-routes convention: nil-manager -> `200 {enabled:false}`, never a `404`
banner; `503` on an unsecured instance -> a calm empty+hint).

**REST (admin for mutations, read for lists).** Identity + connections (unchanged):
`POST/GET /api/v1/peer/identity` (mint/rotate; show fingerprint + safety-number + QR),
`POST /api/v1/peer/connections` (send a signed connect request to a peer's `fp` +
`address`/relay), `GET /api/v1/peer/connections` (list masked, status + verified badge +
**direct-vs-via-relay** indicator), `POST .../{id}/accept|reject|block|verify`,
`DELETE .../{id}` (`?purge=true` cascades `DELETE WHERE source='peer:<fp>'`). Plus 0009's
open feed `GET /api/v1/peer/feed/{share}` and the inbound
`POST /api/v1/sources/peer/ingest` (which also receives `connect` envelopes, direct or
relayed). **New (relay/discovery config):** `GET/PUT /api/v1/peer/relays` (list
configured/seeded relays + `peer.relay.enabled`; add/override/disable; relay health) and
`GET/PUT /api/v1/peer/discovery` (list configured nodes + `discovery.enabled`;
override/disable).

**MCP (1:1 with REST):** `mint_peer_identity`, `request_connection`, `list_connections`,
`accept/reject/block/verify_connection`, `remove_connection`, **plus** `list_relays` /
`set_relays` and `list_discovery` / `set_discovery`.

**Dashboard:** a **Peers** page — my fingerprint + QR + safety-number; look up / add a
peer by `fp` + `address` (a discovery lookup may pre-fill the address); a
**connection-request inbox** (pending Accept/Reject/Block + the safety-number compare
ceremony + verified badge), reusing the reveal-on-interaction pattern; a **"connected via
relay" vs "direct"** indicator and a **"resolved via discovery (relay/direct)"** indicator
on each connection row; **plus** a **Relays** panel and a **Discovery** panel showing the
active **seeded defaults** with a **one-click disable** + an override field. Degrades to a
calm empty+hint on an unsecured instance.

**Events** (noun.verb, ride the bus -> SSE / outbound-webhooks / Events page free):
`peer.identity.minted`, `peer.identity.rotated`, `peer.connection.requested`,
`peer.connection.accepted`, `peer.connection.rejected`, `peer.connection.blocked`,
`peer.connection.verified`, `peer.connection.purged`, **and `peer.discovery.announced`**.
The actual financial sync still fires 0009's `sync.completed` with `source=peer:<fp>`
(optionally carrying a relay-fallback marker) — **no** duplicate `peer.synced`.

### 9. What kasas operates, honestly — never a financial-data custodian

This replaces the prior draft's *"kasas operates nothing" proof*. The honest claim is the
**narrow** one: kasas operates **only** a content-blind, stateless relay/discovery tier and
is **never** a custodian of financial data. Walk every step:

1. **Identity.** Both keypairs are minted **locally**, in the operator's own process; the
   private seeds are written to the operator's **own** `vault.SecretStore`, under two fixed
   keys, no migration. No key is ever published anywhere — the keyserver is cut. **kasas
   touches nothing.**
2. **Discovery.** kasas now **seeds a few default `fp -> address` nodes** (overridable /
   self-hostable / disable-able) holding **only** ephemeral signed TTL'd `fp -> address`
   rows — **never** `fp -> key`, never financial data. The processor relationship for a
   seeded default is with **kasas** (thin — see below); for a self-hosted node, with its
   operator. Direct out-of-band bundle exchange still works with discovery fully disabled.
3. **First contact / reachability.** The signed, age-sealed `connect` envelope is delivered
   direct-first (an outbound HTTPS POST to the recipient's BYO address) and, on double-NAT,
   **may route via a content-blind relay** (kasas-seeded or community). The relay holds
   **no content at rest** — the mailbox is still cut — and sees only ciphertext + size +
   timing + transient IPs.
4. **Consent state.** `peer_connections` lives on the **operator's own DB** and holds only
   public pins + consent state — never a private key, never plaintext financial data.
5. **Share.** The subscriber's `peer` `Puller` polls the publisher's 0009 feed at the BYO
   address **or via a relay** — 0009 verbatim. The age envelope keeps **every** intermediary
   — the user's own TLS-terminating tunnel **and any relay** — out of plaintext.

**The honest obligation, per *Breyer* — thin, but more than "a privacy policy."** Per CJEU
*Breyer* (C-582/14), `IP:port` and `fp -> address` mappings **are** personal data in the
hands of a party who can identify the subject, and **routing IP-addressed traffic is
processing** — which is why Syncthing-style discovery/relay servers carry a privacy
obligation. So for the relay/discovery **defaults kasas chooses to seed**, the residual
duty **lands on kasas** (for self-hosted/community nodes it lands on **their** operator)
and is **not** reducible to one artifact. It is the floor of running an
internet-reachable, content-blind relay:

- **a transient-metadata privacy policy** (the two session fingerprints, each side's
  source `IP:port`, session timing/duration, non-content aggregate counters, and
  discovery's TTL'd `fp -> address` rows + querent IPs — all transient/in-flight or
  TTL-expired, routing-only, no profiling, no sale; covering **nothing** financial and
  **no content** at rest);
- **an abuse-report contact/process** — the relay's IP appears as the apparent source of
  whatever two peers exchange (to an outside scanner, an ISP, or a complaint) even though
  the operator cannot decrypt it, so a reachable abuse path is owed;
- **a documented lawful-process posture** — a court does not care that the operator cannot
  decrypt; it can still compel logging or blocking. The defensible answer is the **locked
  no-connection-logs design** (s2b), so "we kept nothing" is a deliberate posture, not an
  accident;
- **the ordinary operator-liability surface** of an internet-reachable relay (DoS-reflector
  risk if the splice guard ever has a gap; the open-join "free tunnel" concession of s2b),
  bounded by the s2b abuse controls.

Each of these is **categorically lighter than the mailbox's content duties** — no
retention, no deletion path, no breach-of-content, no DSAR-over-content, because **no
content and no per-recipient spool is ever stored** — but they are **named, not collapsed
into "a privacy policy."** The **financial-data** custody obligation, by contrast, is
**eliminated entirely**: kasas never sees or stores a byte of it, on any tier.

**Content-blind, stated precisely.** The relay is content-blind for **financial data** (age
E2E) but **does** see two fingerprints + ciphertext **size** + timing + transient
`IP:port`; the discovery node sees the TTL'd `fp -> address` rows + announcer/querent IPs —
i.e. **both** tiers see a partial who-connects-to-whom graph (s3). That exposure is
**precisely why the obligations above are owed** — do not let "content-blind" slide into
"sees nothing." The metadata a relay sees is strictly **less** than the BYO tunnel
provider already sees, and **direct-first avoids it entirely**.

**The defensible claim, scoped honestly:** *"kasas operates only a content-blind, stateless
relay/discovery tier and is never a custodian of financial data; its obligation is thin and
bounded — a privacy policy + abuse contact + a no-logs lawful-process posture for transient
IP + connection metadata on the defaults it seeds — categorically lighter than the rejected
store-and-forward mailbox."* And no server sneaks in beyond that: there is no keyserver (the
key is bound into the id), no handle namespace (the stable name is the user's BYO hostname or
a relay address; `handle` is a local alias), and no mailbox (live relays drop all content on
disconnect).

**Scope-pin (honest about the rest of the product).** The product already ships two
default-ON phone-home paths — the update check (`update.check=true` -> GitHub Releases) and
the plugin marketplace (`plugins.registry.enabled=true` -> a `raw.githubusercontent.com`
index). Those are **GitHub-hosted static content** (GitHub, not kasas, is the processor for
that request metadata), are operator-disableable, and predate this ADR. The peer subsystem
now adds its **own** content-blind relay/discovery defaults to that list — disclosed here,
not hidden — each disable-able (`peer.relay.enabled=false`, `discovery.enabled=false`).

## What this ADR deliberately does **not** do

- **No async / store-and-forward MAILBOX, no content at rest.** A relay is a **live
  passthrough** between two simultaneously-online peers, holding **no user content** at
  disconnect — it is **not** a mailbox. The both-**offline** case stays unsupported: it is
  precisely the case that would require a server holding bytes awaiting collection, the one
  thing that creates content-at-rest. **Invariant:** relay buffers are in-flight only,
  bounded, and dropped on disconnect; **no spool, no retry-for-an-offline-peer queue, no
  TTL'd outbox** (any of those silently re-creates the rejected mailbox). *Revision note:*
  the prior draft's "no relay at all / direct-only" stance is **revised here** — a
  content-blind **live** relay fallback is now adopted; the **mailbox** remains rejected.
- **No financial-data custody.** The age sign-then-encrypt, recipient-bound envelope makes
  the relay content-blind **by construction** — it sees two fingerprints + ciphertext + size
  + timing + transient `IP:port`, **never plaintext**. "Custody-free" is accurate strictly
  for **financial data**, which kasas never holds or sees on any tier.
- **No claim of zero obligation / no "kasas operates nothing."** kasas operates a
  content-blind, stateless, overridable relay/discovery tier and **owes a thin but
  non-empty duty** for the defaults it seeds (per *Breyer*): a transient-metadata privacy
  policy **plus** an abuse-report contact, a documented no-connection-logs lawful-process
  posture, and the ordinary operator-liability surface of an internet-reachable relay (all
  detailed in s9). The same duty lands on whoever self-hosts a node. It is far lighter than
  the mailbox's content duties (no retention, deletion path, breach-of-content, or
  DSAR-over-content — nothing financial and no content is ever stored), but it is **not
  zero**. kasas is simply **never** a custodian of financial data. (This honest bullet —
  replacing the prior absolutist "no kasas-operated server of any kind" — is the model for
  the whole spine.)
- **No paid service.** Pure OSS, donations only (Syncthing Foundation posture); the seeded
  relays/discovery are **donation-funded defaults**, not a paid tier.
- **No project-run handle namespace.** The stable name *is* the user's BYO front-door
  hostname (or a relay address); `peer_connections.handle` is a local mutable display alias,
  never project-allocated. Discovery maps `fp -> address` only, never an allocated handle.
- **No keyserver / Key-Transparency log / `fp -> key` directory.** Keys arrive via the
  signed out-of-band bundle / connect request and the id binds the key. Seeded **discovery**
  maps `fp -> address` only, so a malicious/compelled node can only **deny** an address,
  never **swap** a key (denial, not impersonation). KT exists to police a keyserver kasas
  does not run.
- **No new object model for invoices.** They ride
  [ADR 0004](0004-transaction-document-artifacts.md) extension namespaces inside the shared
  `ImportBatch`, governed by 0009's `include_extensions` allowlist.
- **No forward secrecy.** Identity/recipient keys are long-lived, so a one-time seed leak
  retroactively decrypts any **captured** ciphertext — re-attributed to the **BYO transport
  or the relay operator** (who only ever holds ciphertext), not to kasas. A future
  per-connection ephemeral-key wrap is the named upgrade, unchanged by this revision.
- **No cryptographic recall.** Once a blob is delivered+decrypted it cannot be un-sent;
  rotation/revocation govern the future only. Unchanged by the relay tier — a relay holds
  no content to recall.

## Consequences

- **kasas is never a financial-data custodian; the heavy mailbox duties are eliminated
  structurally; the residual is a thin-but-non-empty operator duty for the seeded
  relay/discovery defaults.** This is the honest replacement for the prior draft's
  "obligation eliminated structurally" — the **heavy** part (retention / deletion /
  breach-of-content / DSAR-over-content) is gone because no content is stored and nothing
  financial is ever seen; the **thin** part is accepted, bounded, and far lighter than the
  rejected mailbox. **Concrete deliverables:** (1) a short transient-metadata **privacy
  policy** for the kasas-seeded relays + discovery defaults (the two session fingerprints,
  each side's source `IP:port`, session timing/duration, non-content aggregate counters, and
  discovery's TTL'd `fp -> address` rows + announcer/querent IPs — all transient/in-flight or
  TTL-expired, routing-only, no profiling, no sale; covering **nothing** financial, **no
  content** at rest); (2) an **abuse-report contact/process**; (3) a documented
  **no-connection-logs lawful-process posture**; **plus** an "operators run their own policy"
  note for self-hosted nodes.
- **Out-of-the-box reachability behind double-NAT.** A fresh kasas with no front door syncs
  across the open internet via the seeded relay + discovery pool — closing the friction the
  owner flagged ("Syncthing works without Tailscale").
- **The seeded relay/discovery pool ships ON by default** (Syncthing parity): direct-first
  so a default is touched only on fallback, per-instance disable-able + overridable, and
  loudly disclosed via the dashboard. This is the **chosen** posture, not an open question —
  the s9 obligation analysis rests on it being on for the median user.
- **BYO front door becomes OPTIONAL** — a latency optimization, not a requirement to
  receive. Direct-first keeps the common case (shared tailnet / LAN) third-party-free.
- **No MANDATORY central server still holds.** Direct + out-of-band bundle exchange work
  with relay/discovery **fully disabled**; every default is overridable and self-hostable.
  So this **preserves** 0009's no-mandatory-central principle — but the absolutist "kasas
  operates nothing" is **softened** to "operates only content-blind, stateless, overridable
  defaults." 0010 extends 0009 and realizes its proven-attribution upgrade, **honestly**.
- **Attribution becomes proven.** Adopting the ed25519 fingerprint upgrades `shared_by` from
  HMAC-asserted to signature-proven **with no wire break**, realizing 0009's deferred
  upgrade.
- **The `peer` source gains an internet-reachable address (possibly a relay) and one
  decrypt step** — and nothing else in 0009 changes. Subscribe still == add a `Puller`,
  share still == save a query; dedup/events/rules/history/provenance/origin-guard stay 100%
  free.
- **Two new server binaries (relay + discovery), both stateless / ephemeral-in-memory** —
  separate from the ledger, **not** ledger schema; they hold only their own TLS cert/key +
  non-content stats (relay) or a TTL'd in-memory address map (discovery). They must carry
  `strelaysrv`-shaped abuse controls (peer-pair auth, session-key splice guard, pre-auth
  control-channel rate/cost limits, rate/bandwidth/idle/connection caps) or a seeded relay
  becomes an open proxy that inflates the obligation.
- **A residual social-graph / timing leak at BOTH the relay and the discovery node.** Even
  content-blind, a relay sees which two fingerprints connect, when, and payload **size** (a
  coarse activity proxy); the discovery node sees `announcer-IP -> fingerprint` and
  `querent-IP -> queried-fingerprint` (a partial who-looks-for-whom graph). Both are
  sensitive for a financial ledger, both are strictly **less** than the BYO tunnel provider
  already sees, and **direct-first avoids the relay leak entirely**; padding/timing defenses
  are named future hardening, **not** a claim of full privacy-neutrality.
- **One new dep (`filippo.io/age`)** — must clear govulncheck (pin the version; X25519
  recipients only — no scrypt, no `agessh` — enforced in code by constructing only the
  X25519 recipient/identity types). ed25519 / SHA-256 / base32 are stdlib.
- **One new ledger table (`peer_connections`, mig 00019)** — public pins + consent state
  only; relay/discovery **selection** is settings/config, **no migration**. Lean storage,
  comprehensive exposure.
- **The age envelope is load-bearing, not optional**, for any TLS-terminating BYO front door
  **and for any relayed connection** — it is what makes the relay content-blind.
- **Dependency.** 0010 cannot land before (or without) 0009's GAP A (search `source` field —
  verified still absent in `internal/search/search.go`, whose `predFromTerm` cases stop at
  `description/.../synced` with **no `case "source"`**), GAP B (persister folds
  Extensions/Labels — verified `internal/poller/persister.go:131` still inserts the born row
  with hardcoded `"{}","{}"`), origin-guard, namespaced ids, and per-peer egress allowance;
  the relay revision does **not** change these prerequisites (the relay is pure transport).
  The mig numbering (00019 after 0009's 00018) assumes 0009 lands first or co-lands; latest
  on disk is **00017**, so **neither 00018 nor 00019 yet exists**. 0009 names "likely ADR
  0010" as a *federation* follow-on; **this ADR is direct-first connectivity with a relay
  fallback, not the full federation 0009 named** — the 0009 pointer should be re-labelled
  accordingly when 0009 next changes.

## Alternatives considered

- **Community relay pool (Syncthing-style volunteer relays). ADOPTED (was rejected).** The
  prior draft rejected this as "going *further* than Syncthing." It is now the relay tier
  (s2b). The flip is justified by the **live-relay-vs-mailbox** distinction: a relay is a
  live passthrough between two both-online peers holding **no content at rest** —
  categorically different from the rejected store-and-forward mailbox (ciphertext at rest +
  retention + deletion). Adding it is **consistent** with the locked "no async / no content
  at rest" rule.
- **A relay-fallback tier for the both-online-but-neither-reachable (double-NAT) case.
  ADOPTED (was "deliberately unsupported").** Now supported by the live relay; BYO front
  door drops from required-to-receive to optional.
- **A kasas-SEEDED default discovery node. ADOPTED (was rejected as "the camel's nose").**
  kasas now seeds a few `stdiscosrv`-analog nodes (signed, ephemeral, TTL'd `fp -> address`
  only) so it works out of the box, kept self-hostable + overridable + disable-able. Safe to
  operate **for identity** because it maps `fp -> address` only (never `fp -> key`): a hostile
  node can deny, never impersonate. The residual is the metadata/graph leak named in s3/s9.
- **A "tiny" STUN/DERP/coordination rendezvous helper. REFRAMED — the relay IS that
  bridge.** The prior draft name-and-rejected any coordination helper as "an operated service
  touching connection metadata, reintroducing the obligation." That is now accepted, but
  **bounded**: the relay touches transient IP/timing/size only, is content-blind and
  nothing-at-rest-for-content, and so reintroduces a **thin** obligation (privacy policy +
  abuse contact + no-logs posture) — **not** the heavy mailbox obligation. The reject becomes
  the adoption.
- **Store-and-forward async MAILBOX (the both-offline path). STILL REJECTED.** Asynchronous
  delivery requires a server holding ciphertext **at rest** awaiting collection — the one
  thing that creates content-at-rest on a kasas service and re-imports the heavy
  retention/deletion/DSAR-over-content/breach-of-content duties the owner refused. A live
  relay does **not** bring this back; any relay feature that holds bytes after both peers are
  not simultaneously online silently re-creates this mailbox and is forbidden.
- **Hosted zero-knowledge directory + encrypted relay (the original AWS draft). STILL
  REJECTED.** Even storing only ciphertext + routing metadata, it is a data processor the
  owner explicitly refused; it conceded it needed a privacy policy / DPA / retention /
  deletion path. A **store-and-forward** relay is custody; a **live** relay is not.
- **A paid service of any kind. STILL REJECTED.** Pure OSS, donations only; the seeded
  relays/discovery are donation-funded defaults, not a paid tier.
- **A project-run global handle namespace (`name@host`). STILL REJECTED.** A squat-resistant
  namespace needs an operated authority holding identity-linkable data. The stable name is
  the user's BYO front-door hostname (or a relay address); `handle` is a local alias.
- **Key Transparency / CONIKS-lite, and literal OpenPGP/GnuPG + SKS keyserver +
  web-of-trust. STILL CUT.** KT exists to police an operated keyserver kasas does not run;
  `x/crypto/openpgp` is frozen/deprecated, alternatives drag a heavy fork or cgo-Sequoia,
  zero interop value (kasas peers only talk to kasas peers), SKS died to CVE-2019-13050,
  web-of-trust is wrong UX for a single-user homelab. Adopt the GPG **model** (ed25519 +
  age), reject the bytes. `nacl/box` is rejected for age (no native multi-recipient,
  manual-nonce footgun); **encrypt-then-sign** is rejected for **sign-then-encrypt**
  (encrypt-then-sign would leak the sender's fingerprint — here, to the BYO tunnel operator
  **or the relay**).
- **Do nothing / keep the direct-only prior draft. REJECTED as too much friction.** That
  draft is the baseline this revision evolves: it forced **every** receiving peer to BYO a
  reachable front door, the exact friction the owner flagged ("Syncthing works without
  Tailscale"). Keeping it would mean a fully-double-NATed peer is unservable out of the box —
  a real gap a thin, content-blind relay tier closes for a thin, bounded obligation.
- **libp2p Kademlia DHT / ActivityPub-WebFinger / Nostr-SSB-Matrix. STILL REJECTED**
  (carried from 0009) — global discoverability is a misfeature for a private ledger; these
  lean on third-party bootstrap/relays/homeservers (a soft central server + a metadata leak)
  and are massively heavy for a 2-instance sync. The seeded relay/discovery tier is a
  bounded, content-blind, exact-fingerprint-keyed alternative, **not** a global DHT.

## Open questions

- **The privacy policy + operator-duty set as deliverables.** What is the minimum honest
  transient-metadata **privacy policy** for the kasas-seeded relays + discovery defaults
  (two session fingerprints, each side's source `IP:port`, session timing/duration,
  non-content aggregate counters; for discovery the TTL'd `fp -> address` rows +
  announcer/querent IPs), and what minimal **abuse-report contact** + **no-connection-logs
  lawful-process statement** accompany it? How is the "self-hosted operators run their own
  policy" boundary surfaced so an operator enabling a self-hosted relay knows the duty is
  theirs? (The *posture* — privacy policy + abuse contact + no-logs + default-on — is
  **decided**; only the exact wording is open.)
- **Relay metadata / social-graph leak.** Even content-blind, a relay sees which two
  fingerprints connect, when, and payload size; the discovery node sees the announce/lookup
  graph. Both are strictly less than the BYO tunnel provider already sees and direct-first
  avoids the relay leak entirely — are padding / timing-obfuscation defenses worth
  specifying now, or named as future hardening?
- **Relay token / join model.** Should seeded public relays run **open-join** (any `fp` may
  register, gated by signed JoinRelay + rate-limits) while private/self-hosted relays run
  **token-gated** (`peer.relay.join_token` in vault), and how does a peer discover which
  seeded relays accept its JoinRelay vs require a token?
- **Relay selection + fallback policy.** How does the ledger pick a relay on fallback
  (intersection of the target's advertised relays and the local enabled pool; preference
  order; health-check; failover across multiple relays), and is relay-fallback automatic or
  operator-confirmed per connection?
- **Discovery announce cadence + abuse.** Concrete defaults for the ~30-min `Reannounce-
  After` cadence, per-IP lookup rate-limits + `429`, and confirmation that the high-entropy
  32-char fingerprint is the only lookup key with no prefix/range/enumerate path surviving.
- **Surfacing the mandatory-envelope boundary.** When the dial target is a TLS-terminating
  tunnel **or a relay**, should the dashboard show a "plaintext-to-third-party" warning
  unless the age envelope is confirmed active — so a "tailnet-only, envelope off" deploy
  can't silently ship plaintext to a terminating proxy or a relay?
- **Carried-forward 0009 questions, unchanged by the relay tier.** Invoice/artifact bytes
  over the connection; a frontdoor-less subscriber's ongoing pull; retraction without a
  mailbox; rotation / multi-device UX with no directory to push to (and the multiple-pinned-
  X25519-keys storage it would need, s6); connect-request rate-limit defaults at the
  receiving front door (now also reachable via a relay); and the inert, length-capped
  greeting field.
- **Mig-numbering reconciliation.** `peer_connections` stays mig **00019** (next free after
  0009's proposed **00018**), but latest on disk is **00017** — confirm 0009 lands first or
  co-lands, and that relay/discovery selection stays settings/config with **no** new
  migration.
