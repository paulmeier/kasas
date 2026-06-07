# REST API

The REST API is the primary programmatic surface. All responses are JSON;
timestamps are RFC 3339 (UTC); money fields are exact decimal strings exactly as
SimpleFIN returns them. Every route is gated by an
[auth tier](authentication.md) — `read`, `write`, or `admin` — and all tiers are
open when no token is configured.

Base path: `/api/v1`. Source:
[`internal/api`](https://github.com/paulmeier/kasas/tree/main/internal/api).

## Conventions

- **Auth** — send `Authorization: Bearer <token-or-key>` (see
  [Authentication](authentication.md)).
- **Pagination** — list endpoints accept `?limit=` (default 100, max 1000) and
  `?offset=`.
- **Time filters** — `?since=` / `?until=` accept a `YYYY-MM-DD` date, an RFC 3339
  timestamp, or unix seconds.
- **Money** — `amount` and `balance` are strings; never parse them as floats if you
  need exactness.

## Operational

| Method & path | Tier | Description |
| --- | --- | --- |
| `GET /healthz` | open | Liveness probe (`200 ok`). |
| `GET /readyz` | open | Readiness probe (pings the database). |
| `GET /metrics` | open | [Prometheus metrics](metrics.md). |
| `GET /api/v1/auth` | open | `{auth_required, authenticated}` — lets a client know whether to prompt. |

## Read tier

| Method & path | Description |
| --- | --- |
| `GET /api/v1/organizations` | List organizations. |
| `GET /api/v1/accounts` | List accounts (`?org_id=` to filter). |
| `GET /api/v1/accounts/{id}` | Get one account. |
| `GET /api/v1/accounts/{id}/transactions` | Transactions for an account. |
| `GET /api/v1/transactions` | List transactions (`?label_key=` + optional `?label_value=` to drill down). |
| `GET /api/v1/transactions/search` | [Search](../features/search.md) (`?q=`); returns `{query, total, transactions}`. |
| `GET /api/v1/transactions/{id}` | Get one transaction. |
| `GET /api/v1/transactions/{id}/history` | The transaction's [version history](../features/transaction-history.md). |
| `GET /api/v1/transactions/{id}/provenance` | The transaction's [provenance](../features/transaction-provenance.md): source, identity, and transformation lineage. |
| `GET /api/v1/labels` | [Labels](../features/labels.md) with per-pair transaction counts. |
| `GET /api/v1/extensions` | [Extension](../features/schema-extensions.md) vocabulary with per-key counts. |
| `GET /api/v1/rules` · `GET /api/v1/rules/{id}` | List / get [rules](../features/rules.md). |
| `GET /api/v1/plugins` · `GET /api/v1/plugins/{id}` | List / get [plugins](../features/plugins.md) (when enabled). |
| `GET /api/v1/events` | Read the [event stream](../features/event-stream.md) from a cursor (`?after=`, `?type=`, `?entity_type=`, `?entity_id=`, `?limit=`, `?newest`). Returns `{events, next}`. |
| `GET /api/v1/events/{sequence}` | Get one event by sequence. |
| `GET /api/v1/events/stream` | Live SSE tail (`?after=` to replay then follow). |
| `GET /api/v1/sync` · `GET /api/v1/sync/history` | Latest [sync](../features/sync.md) status / recent runs (`?limit=`). |
| `GET /api/v1/config` | Effective configuration, secrets redacted (powers Settings). |
| `GET /api/v1/update` | [Self-update](../reference/cli.md#self-update) status (when `update.check` is on). |

## Write tier

| Method & path | Description |
| --- | --- |
| `PUT /api/v1/transactions/{id}/labels` | Replace a transaction's labels (`{"labels":{"category":"food"}}`). |
| `DELETE /api/v1/labels/{key}` | Remove a label key from every transaction (`?value=` to scope). |
| `PUT /api/v1/transactions/{id}/extensions` | Replace a transaction's [extensions](../features/schema-extensions.md). |
| `POST /api/v1/rules` | Create a rule; validates the query, `400` on error. |
| `PUT /api/v1/rules/{id}` · `DELETE /api/v1/rules/{id}` | Replace / delete a rule. |
| `POST /api/v1/rules/{id}/run` | Apply one rule to existing transactions; returns `{matched, updated}`. |
| `POST /api/v1/rules/run` | Apply all enabled rules to existing transactions. |
| `POST /api/v1/sync` | Trigger a [sync](../features/sync.md) (async, returns `202`). |

## Admin tier (dashboard token only)

| Method & path | Description |
| --- | --- |
| `PUT /api/v1/simplefin/credential` | Set the SimpleFIN setup token or access URL. |
| `POST /api/v1/security/token` · `DELETE …` | Generate/set or revoke the [dashboard token](authentication.md). |
| `POST /api/v1/security/api-keys` · `GET …` · `DELETE …/{id}` | Mint / list / revoke [API keys](authentication.md#api-keys). |
| `GET/POST/PUT/DELETE /api/v1/webhooks` (+ `/{id}/test`, `/{id}/rotate-secret`) | Manage [webhooks](../features/webhooks.md). |
| `POST /api/v1/plugins/{id}/enable` · `/disable` · `/reload` | [Plugin](../features/plugins.md) lifecycle. |
| `POST /api/v1/update` | Install the latest release in place (when `update.allow_apply` is on). |

## Examples

```sh
# recent transactions for an account, filtered by label
curl "localhost:8080/api/v1/transactions?since=2024-01-01&limit=50"
curl "localhost:8080/api/v1/transactions?label_key=category&label_value=food"

# search: coffee outflows in 2024
curl "localhost:8080/api/v1/transactions/search?q=coffee%20amount:%3C0%20date:2024"

# poll the event stream forward from a cursor
curl "localhost:8080/api/v1/events?after=42&limit=100"   # -> {"events":[…],"next":57}
```

## DTOs

Handlers convert internal `db` models to stable JSON DTOs in
[`dto.go`](https://github.com/paulmeier/kasas/blob/main/internal/api/dto.go):
`labels` decode to a `{key:value}` object and `extensions` to a structured JSON
object; event `data` and history snapshots are free-form JSON. These same DTOs
back the [MCP tools](mcp.md), so REST and MCP return identical shapes.
