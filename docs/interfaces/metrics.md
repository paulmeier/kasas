# Metrics & Observability

kasas is built to be unremarkable to operate: Prometheus metrics, structured
logs, and liveness/readiness probes, all out of the box.

## Prometheus metrics

`GET /metrics` exposes the Go/process defaults plus these kasas-specific series
(the endpoint is always open, so a scraper needs no token):

| Metric | Type | Labels | Meaning |
| --- | --- | --- | --- |
| `kasas_sync_total` | counter | `status` | [Sync](../features/sync.md) runs by outcome (success/error). |
| `kasas_sync_duration_seconds` | histogram | — | Sync duration distribution. |
| `kasas_last_successful_sync_timestamp_seconds` | gauge | — | Unix time of the last successful sync. |
| `kasas_accounts` | gauge | — | Accounts seen in the most recent sync. |
| `kasas_transactions_inserted_total` | counter | — | New transactions inserted. |
| `kasas_transactions_updated_total` | counter | — | Existing transactions refreshed by a sync. |
| `kasas_rules_applied_total` | counter | — | New transactions auto-labeled by a [rule](../features/rules.md). |
| `kasas_events_emitted_total` | counter | `type` | [Events](../features/event-stream.md) appended to the stream, by type. |
| `kasas_events_dropped_total` | counter | — | Live event subscribers dropped for lagging. |
| `kasas_transaction_versions_total` | counter | `kind` | [History](../features/transaction-history.md) snapshots, by change kind. |
| `kasas_webhook_deliveries_total` | counter | — | [Webhook](../features/webhooks.md) deliveries (terminal outcome). |
| `kasas_webhook_delivery_attempts_total` | counter | — | Individual delivery attempts (includes retries). |
| `kasas_webhook_deliveries_dropped_total` | counter | — | Deliveries dropped (queue full). |
| `kasas_plugin_invocations_total` | counter | — | [Plugin](../features/plugins.md) hook invocations. |
| `kasas_plugin_errors_total` | counter | — | Plugin invocations that errored or panicked. |
| `kasas_plugin_jobs_dropped_total` | counter | — | Plugin jobs dropped (per-plugin queue full). |

!!! note "Labeled counters start empty"
    Prometheus `*Vec` metrics emit no series until they've been observed at least
    once. So `kasas_sync_total{status="error"}` won't appear on `/metrics` until
    the first error actually happens — that's expected, not a missing metric.

## Logging

Structured logging via the standard library's `slog`. Configure with `[log]`:

- `log.level` — `debug` · `info` · `warn` · `error` (default `info`).
- `log.format` — `json` (default, for log pipelines) or `text` (for humans).

Each HTTP request is logged at **debug** level with method, path, status, bytes,
duration, and the request id. Notable lifecycle events (sync results, dispatcher
and plugin-manager startup, shutdown, the "unsecured" warning) log at info/warn.

## Health & readiness

| Endpoint | Checks | Use |
| --- | --- | --- |
| `GET /healthz` | Process is up (`200 ok`). | Liveness probe. |
| `GET /readyz` | Pings the database. | Readiness / dependency probe. |

Both are unauthenticated. The container image's `HEALTHCHECK` shells out to
[`kasas healthcheck`](../reference/cli.md#healthcheck), which probes `/healthz`
internally — handy because the `scratch`-based image has no `curl`.
