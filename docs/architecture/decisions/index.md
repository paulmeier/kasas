# Architecture Decision Records

An **Architecture Decision Record (ADR)** captures a single significant
decision — the context that forced it, the choice made, and the consequences
accepted — so the reasoning survives the commit that implements it.

These records are intentionally **append-only**. A decision that no longer
holds is not edited away; a later ADR supersedes it and the old one stays as
history, its status updated to point forward.

## Status vocabulary

| Status | Meaning |
| --- | --- |
| **Proposed** | Written up for discussion; not yet agreed or built. |
| **Accepted** | Agreed and in force (built, or committed to build). |
| **Superseded** | Replaced by a later ADR (linked from the header). |
| **Rejected** | Considered and deliberately not taken (kept for the reasoning). |

## Index

| ADR | Title | Status |
| --- | --- | --- |
| [0001](0001-plugin-dependency-bundling.md) | Allow bundled dependencies in plugins | Accepted |
| [0002](0002-plugin-network-capability.md) | Host-mediated plugin network access (`net:fetch`) | Accepted |
| [0003](0003-marketplace-trust-tiers.md) | Marketplace trust tiers | Accepted |
| [0004](0004-transaction-document-artifacts.md) | Document & artifact association for transactions | Proposed |
| [0005](0005-plugin-originated-transactions.md) | Plugin-originated transactions (`source:provide`) | Proposed |
| [0006](0006-external-market-reference-data.md) | External market & reference data as a first-class source | Proposed |
| [0007](0007-transaction-soft-delete.md) | Soft-delete: reversible transaction hiding | Proposed |
| [0008](0008-inbound-webhook-source.md) | Inbound-webhook source (`webhook` archetype) | Accepted |
| [0009](0009-p2p-ledger-sync.md) | Selective peer-to-peer ledger sharing | Proposed |
| [0010](0010-custody-free-peer-connectivity.md) | Custody-free internet peer connectivity | Proposed |

ADRs 0001–0003 form one arc: they widen what a plugin may *do* without
abandoning the sandbox that makes plugins safe to install. ADR 0004 is the
use case that motivated the arc — associating a receipt with a transaction —
and shows how the new seams (plus the existing ones) compose to serve it.
ADR 0005 takes the last step the arc deliberately deferred — letting a plugin
*originate* a transaction — and resolves it through the existing ingestion seam
(a plugin produces a batch; the engine writes it) rather than a raw write.
ADR 0006 opens a different front: world data (benchmarks, quotes, FX) as a
first-class source with its own archetype and a `market_*` cache namespace —
the backend half of a decision shared with the sillview dashboard, whose own
ADR-0004 records the consumption half.
ADR 0007 turns inward to the ledger's own integrity: it replaces destructive
hard deletion with a reversible soft-delete (`deleted_at`), so transactions —
manual *or* synced — can be hidden from views and analysis without erasing the
record, with hard deletion narrowing to genuine teardown (source uninstall,
account deletion).
ADR 0008 adds the first **push** source: the `webhook` archetype, where an external
system POSTs a signed batch to an ingest endpoint instead of kasas polling for it. It
inverts the `Puller` direction through a new `Receiver` capability while reusing the
engine's existing persist path verbatim, and reuses kasas's own outbound-webhook HMAC
scheme for verification — so the security boundary is a shared secret, not the
dashboard token.
ADR 0009 turns ADR 0008's one-way ingest into a *relationship*: selective,
subscription-style peer-to-peer sharing between two self-hosted ledgers, with no
mandatory central server. It reframes a "share" as a saved search query and a
"subscription" as adding a `peer` source, so the engine's persist/dedup/events/rules
all apply for free; on the receiver each row keeps its original originator (in an
extension) *and* gains a `shared_by:<ledger>` tag, while a structural origin-guard
forbids re-exporting rows you did not author.
ADR 0010 fills the one gap 0009 left — connecting two *strangers* on different
networks who share no tailnet and no out-of-band channel — **without kasas operating
any server**. An earlier draft proposed a hosted, paid zero-knowledge directory +
encrypted relay, but that was a data processor with privacy/DPA/retention/deletion
duties the project refuses to carry; this revision takes the **Syncthing** approach
instead. It is "0009 + a keypair + a doctrine": ed25519 identity (realizing 0009's
deferred *proven*-attribution upgrade) + `age` encryption, peers reachable only via
their own **bring-your-own front door** (a shared tailnet, a tunnel, a VPS),
discovery by out-of-band fingerprint exchange (no keyserver to MITM), and
consent-gated connection requests — **direct-only, no async, pure OSS**. The
data-processor obligation is eliminated *structurally*: there is no kasas-operated
service to regulate. It **upholds and strengthens** — does not revise — 0009's
no-central-server stance.
