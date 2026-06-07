# Sync Pipeline

The sync pipeline is the **ingestion engine** for [pull sources](../architecture/ingestion.md).
A background scheduler drives the configured source's `Puller` on an interval — the
source returns a neutral `ImportBatch` — and the engine upserts organizations and
accounts and reconciles transactions, inserting new ones, refreshing existing ones,
and **never touching the metadata you added**. Every run is recorded, and every
change emits an event.

[SimpleFIN](https://www.simplefin.org/) is the source today, so this page uses it as
the worked example; the steps that talk to a bank (resolving a credential, fetching)
live in the source, and everything after the `ImportBatch` is the engine — identical
for any future source.

Source: [`internal/poller`](https://github.com/paulmeier/kasas/tree/main/internal/poller)
(engine) · [`internal/sources/simplefin`](https://github.com/paulmeier/kasas/tree/main/internal/sources/simplefin)
(the source).

!!! tip "One engine, any source"
    Scheduling, dedup, the transactional persist, events, rules, and history are
    the engine's job and work the same for every source. See
    [Ingestion & Sources](../architecture/ingestion.md) for the source contract.

## At a glance

- **Scheduled** by `gocron` every `sync.interval` (default `6h`), plus an
  optional run at startup and an on-demand trigger.
- **Serialized** — a mutex ensures only one sync runs at a time, so concurrent
  triggers never overlap.
- **Idempotent** — transactions are inserted by their source's external id with
  `ON CONFLICT DO NOTHING`; re-syncing the same data is a no-op.
- **Non-destructive** — a re-sync refreshes source-owned fields (amount, pending,
  description…) but leaves your [labels](labels.md) and
  [extensions](schema-extensions.md) alone.
- **Atomic** — all upserts, inserts, refreshes, label changes, events, and
  history snapshots for a run commit in a single database transaction.

## A sync run, step by step

```mermaid
sequenceDiagram
    autonumber
    participant Sched as gocron
    participant Eng as Engine (poller)
    participant Src as Source (SimpleFIN)
    participant Sec as Secret store
    participant SF as SimpleFIN bridge
    participant DB as db.Store (tx)
    participant Bus as Event bus

    Sched->>Eng: Sync(ctx)
    Note over Eng: mutex.Lock — only one sync at a time
    Eng->>DB: CreateSyncLog(status="running")

    Eng->>Src: Fetch(since, cursor)
    Note over Src,SF: credential + fetch live in the source
    Src->>Sec: resolveAccessURL()
    alt stored access URL exists
        Sec-->>Src: access URL
    else only a setup token
        Src->>SF: POST claim(setup token)
        SF-->>Src: access URL
        Src->>Sec: persist access URL (one-time)
    end
    Src->>SF: GET /accounts?start-date=since&pending=1
    SF-->>Src: AccountSet (orgs, accounts, transactions)
    Src-->>Eng: ImportBatch (neutral)

    rect rgb(238, 247, 238)
    Note over Eng,DB: emitter.Record → one DB transaction
    loop each account
        Eng->>DB: UpsertOrganization / UpsertAccount
        Eng->>DB: emit account.created / account.updated
        loop each transaction
            Eng->>DB: InsertTransaction (ON CONFLICT DO NOTHING)
            alt new row
                Eng->>DB: emit transaction.created
                Eng->>DB: apply matching rules → labels + label.applied
                Eng->>DB: history v1 (imported)
            else already exists
                Eng->>DB: UpdateTransactionFromSync (labels untouched)
                Eng->>DB: if source fields changed → transaction.updated + history (synced)
            end
        end
    end
    end

    DB-->>Eng: commit
    Eng->>Bus: publish buffered events
    Eng->>DB: CompleteSyncLog(status, counts) + metrics
    Eng->>Bus: emit sync.completed
```

## Connecting: resolving the access URL

Credential handling belongs to the source — each source manages its own. The
**SimpleFIN** source authenticates with a long-lived **access URL** (credentials
embedded in the URL's userinfo); its `resolveAccessURL` finds it in priority order:

1. **Stored** — a previously persisted access URL in the
   [secret store](../getting-started/configuration.md#secrets) (Vault or the local
   `0600` file). Used as-is.
2. **Configured access URL** — `simplefin.access_url`; persisted to the store on
   first use.
3. **Setup token** — `simplefin.setup_token`, a one-time base64 token. kasas
   base64-decodes it to a claim URL, `POST`s to claim a fresh access URL, and
   **persists that** — so the token is consumed exactly once.

You can also set the credential at runtime from the
[dashboard Settings page](../interfaces/dashboard.md) or
`PUT /api/v1/simplefin/credential`, no restart required — the next sync picks it
up.

## Fetching

The engine calls the source's `Fetch(ctx, since, cursor)`, passing a `since`
derived from `sync.lookback_days` (default `90`; `0` fetches all available
history). For the SimpleFIN source that becomes `GET <access-url>/accounts` with
two query parameters:

- `start-date=<unix>` — from `since`. This bounds how far back each pull reaches.
- `pending=1` — include pending transactions, so a charge is visible before it
  posts.

SimpleFIN responds with an `AccountSet` (organizations, accounts with balances, and
each account's transactions), which the source normalizes into the engine's neutral
`ImportBatch`. `cursor` is unused here — SimpleFIN re-fetches a window rather than
resuming a stream — but the engine persists any cursor a source does return.

## Reconciling: insert vs. refresh

This is the heart of the pipeline, and the guarantee that makes kasas safe to
build on.

```mermaid
flowchart TD
    T[Transaction from the batch] --> INS[InsertTransaction<br/>ON CONFLICT DO NOTHING]
    INS --> Q{rows affected?}
    Q -->|1 — new| NEW[New transaction]
    Q -->|0 — exists| OLD[Existing transaction]

    NEW --> NE[emit transaction.created]
    NE --> NR[apply enabled rules<br/>→ birth labels]
    NR --> NV[history v1: imported]

    OLD --> GET[read previous row]
    GET --> UPD["UpdateTransactionFromSync<br/>(amount, pending, date,<br/>description, payee, memo, synced_at)"]
    UPD --> CH{source fields<br/>changed?}
    CH -->|yes| UE[emit transaction.updated<br/>+ history: synced]
    CH -->|no| SKIP[no event]

    style NEW stroke:#3a7d44,stroke-width:2px
    style OLD stroke:#b9770e,stroke-width:2px
```

For an **existing** transaction, `UpdateTransactionFromSync` writes only the
columns the source owns. The `labels` and `extensions` columns are **deliberately
excluded from the UPDATE**, so a pending charge that later posts — or an amount the
source corrects — flows in cleanly while your categorization is preserved
untouched. This is the "the data you add is sacred" principle, enforced at the SQL
level.

## Birth-time labeling

When a transaction is inserted, the poller runs every enabled
[rule](rules.md) against it. Rules are compiled once per sync (their
[search](search.md) queries parsed up front); a matching rule's labels are merged
onto the new transaction, written, and each applied key emits a `label.applied`
event. So a transaction can arrive already categorized, and that categorization is
captured in its `v1 imported` [history snapshot](transaction-history.md).

Rules apply only to **new** transactions during a sync — re-syncing an existing
row never re-runs rules — which keeps syncs idempotent. To apply rules to history,
[run them on demand](rules.md#running-rules).

## The sync log & metrics

Each run writes a `sync_log` row (`started_at`, then `completed_at`, `status` of
`success`/`error`, and any `error` message). The latest status and recent history
are available at `GET /api/v1/sync` and `/api/v1/sync/history`, on the dashboard,
and via the `sync_status` MCP tool. A run also updates Prometheus
[metrics](../interfaces/metrics.md): `kasas_sync_total{status}`, the duration
histogram, `kasas_transactions_inserted_total`, `kasas_rules_applied_total`,
`kasas_last_successful_sync_timestamp_seconds`, and the accounts gauge.

## Triggering a sync

| How | Detail |
| --- | --- |
| Scheduled | Every `sync.interval`; the default `run_on_start=true` also runs one at boot. |
| REST | `POST /api/v1/sync` (runs async, returns `202`). |
| MCP | The `trigger_sync` tool. |
| Dashboard | **Settings → force a sync**, with live status. |
| CLI | [`kasas sync`](../reference/cli.md#sync) runs exactly one sync and exits. |

## Configuration

See [Configuration → `[sync]`](../getting-started/configuration.md#sync) for
`enabled`, `interval`, `lookback_days`, and `run_on_start`.
