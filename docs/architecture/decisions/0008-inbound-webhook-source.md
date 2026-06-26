# ADR 0008 — Inbound-webhook source (`webhook` archetype)

- **Status:** Accepted (built)
- **Date:** 2026-06-26
- **Related:** [Ingestion architecture](../ingestion.md) (archetypes + the source
  contract this extends),
  [Inbound webhook](../../features/inbound-webhook.md) (the operator-facing feature),
  [Webhooks](../../features/webhooks.md) (the *outbound* dispatcher whose HMAC scheme
  this reuses, symmetrically),
  [ADR 0005](0005-plugin-originated-transactions.md) (the other "originate, don't
  fetch" producer; both land through the shared persist path),
  [Event Stream](../../features/event-stream.md),
  [Transaction Provenance](../../features/transaction-provenance.md)

## Context

[Ingestion](../ingestion.md) models **archetypes — how data arrives — not
providers.** Five archetypes shipped before this ADR: `pull` (SimpleFIN, Teller,
Plaid, on-chain), `file` (CSV), `reference` (market data), plus `manual`. All of
them are kasas-initiated: the engine *fetches* on a schedule, or a human *writes*
directly. The `webhook` archetype — "an external system pushes data to us" — was
named in the taxonomy and reserved in the source SDK, but unbuilt.

The gap matters. A growing share of financial and adjacent systems (payment
processors, point-of-sale, budgeting tools, scripts, automation platforms like
Zapier/IFTTT, and *kasas itself* via its outbound webhooks) emit events **outward**
the moment something happens. For those, polling is the wrong shape: there is
nothing to poll, only something to receive. kasas already has the receiving half of
its own story — the [outbound dispatcher](../../features/webhooks.md) HMAC-signs and
POSTs every event out — but no way to take data *in* the same way.

The hard part is not parsing a body; it is that the ingest endpoint is
**internet-reachable and cannot use the dashboard token** (the sender does not have
it). Authentication, not transport, is the whole design.

## Decision

Add the `webhook` archetype as a first-class source with a new capability seam, and
make **authentication the security boundary** by reusing kasas's existing
outbound-webhook signing scheme in reverse.

### A `Receiver` capability that inverts `Puller`

`internal/source` gains one small capability interface (and a transport-neutral
delivery type), mirroring how every archetype is a composable interface the engine
detects by type assertion:

```go
type Receiver interface { // archetype "webhook"
    Receive(ctx context.Context, delivery Delivery) (*ImportBatch, error)
}

type Delivery struct { // already read + size-limited by the engine
    Header http.Header
    Body   []byte
}
```

A `Receiver` returns the **same `ImportBatch`** a `Puller` returns, so the engine
persists a pushed delivery through the *exact* path it already uses for a pulled one
— idempotent dedup by `(source, external_id)`, atomic events-with-changes,
[rule](../../features/rules.md) auto-labeling, [history](../../features/transaction-history.md),
and [provenance](../../features/transaction-provenance.md) stamping all come for
free. The only new control flow is the trigger: an HTTP POST to
`/api/v1/sources/{type}/ingest` instead of a `gocron` tick. The poller's scheduled
`Sync` becomes a clean no-op for a push source, and a new `Ingest` runs the
verify → parse → persist path, recording the same `sync_log` row and `sync.completed`
event a scheduled run does — but **only after** authentication succeeds, so a
rejected delivery never spams the log.

### HMAC verification, symmetric with the outbound dispatcher

The endpoint is registered **open** (like the OAuth callback) — it bypasses the
dashboard-token middleware because the sender has no token. It is *not*
unauthenticated: the source verifies an **HMAC-SHA256 signature over
`"<timestamp>.<body>"`**, the byte-for-byte scheme `internal/webhooks.Sign` already
uses to sign outbound deliveries (`X-Kasas-Signature: sha256=…` +
`X-Kasas-Timestamp`). Verification is constant-time (`hmac.Equal`) and rejects a
timestamp outside a ±5-minute freshness window (replay resistance; re-delivery is
harmless anyway, since dedup makes it idempotent). Every failure collapses to a
coarse `401` so a handler never leaks which check failed.

Reusing the outbound scheme is the elegant part: **a kasas instance's outbound
webhook can feed another kasas instance's inbound-webhook source unchanged.**

### A generated, revealable, rotatable secret

The shared secret is the auth boundary, so the source **mints** it rather than
asking the operator to invent one. A second small capability,
`WebhookSecret { RevealSecret; RotateSecret }`, lets the admin-gated API reveal the
secret (to copy into the sender) and rotate it (old secret stops verifying at once)
— the same lifecycle the outbound webhook secret already has. The source is
registered and always built, but **inert until a secret is generated**: with no
secret it rejects every delivery, so an instance that never opts in exposes nothing.
A power user can instead paste a specific secret via the existing `Credentialed`
seam (e.g. to match a fixed value a sender already uses).

### Lean storage

The secret lives in the existing `vault.SecretStore` under one key; pushed
transactions reuse the `accounts`/`transactions` tables exactly like every other
source. **No migration, no new table** — consistent with kasas's "lean on storage,
comprehensive on exposure" stance. The source is wired across REST, MCP, and the
dashboard for full parity.

### Payload: the neutral `ImportBatch`

The accepted body is kasas's own `ImportBatch` JSON (accounts, each with its
transactions). This dogfoods the SDK's universal type, needs no second bespoke
schema, and means a sender describes data in the same shape every internal source
produces. The source stamps `source = "webhook"`, namespaces ids with a `webhook:`
prefix, and content-hashes any transaction the sender did not key, so re-delivery is
idempotent.

## Consequences

- **A new internet-facing surface exists**, but it is closed by default (inert until
  a secret is generated), HMAC-gated, body-size-capped (1 MiB), and constant-time
  verified. It adds no new stored secret table and no migration.
- **The engine was generalized once**: `Sync` no-ops for a push source and a shared
  `recordRun`/`persistResult` tail now backs both scheduled fetches and pushed
  deliveries. Existing sources are untouched (every capability is independent).
- **Symmetry with the outbound dispatcher**: one HMAC scheme now both signs outgoing
  events and verifies incoming deliveries.
- **Deferred (v1 scope):** multiple per-sender endpoints/secrets (a future
  `MultiCredentialed` extension), a non-`ImportBatch` convenience payload, and a
  per-source rate limiter (the size cap + HMAC gate are the v1 DoS mitigation).

## Alternatives considered

- **A generic, source-agnostic `/ingest` endpoint** not tied to a source. Rejected:
  everything in kasas is a source, and routing through the source seam means the
  webhook inherits listing, status, provenance, and the persist guarantees for free.
- **Bearer-token auth** (send the secret as a header). Simpler for naive senders,
  but no body binding and no replay protection, and it would *not* be symmetric with
  the outbound dispatcher. Rejected in favor of HMAC; a bearer path can be added
  later if demand appears.
- **A new `inbound_webhooks` table** mirroring the outbound `webhooks` table.
  Rejected as storage weight the feature does not need — one vault key suffices.
