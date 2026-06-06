<p align="center">
  <img src="internal/dashboard/web/logo.png" alt="kasas" width="160">
</p>

# kasas

[![CI](https://github.com/paulmeier/kasas/actions/workflows/ci.yml/badge.svg)](https://github.com/paulmeier/kasas/actions/workflows/ci.yml)
[![Release](https://github.com/paulmeier/kasas/actions/workflows/release-please.yml/badge.svg)](https://github.com/paulmeier/kasas/actions/workflows/release-please.yml)
[![GHCR](https://img.shields.io/badge/ghcr.io-paulmeier%2Fkasas-blue?logo=docker&logoColor=white)](https://github.com/paulmeier/kasas/pkgs/container/kasas)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A self-hosted Go service that syncs your [SimpleFIN](https://www.simplefin.org/)
financial data into a local SQLite (or Postgres) database and serves it over a
REST API and a built-in [MCP](https://modelcontextprotocol.io/) server.

- **Single static binary**, no CGO — pure-Go SQLite (`modernc.org/sqlite`) and
  pure-Go Postgres (`jackc/pgx`).
- **SQLite or Postgres** — zero-dependency on embedded SQLite by default, or
  point it at a Postgres server with one config change. Same binary either way.
- **Web dashboard** at `/` — an account overview + a filterable, sortable,
  paginated transactions table with inline, editable transaction **labels**
  (key:value pairs, with typeahead suggestions), plus a **search** page with a
  robust query language over any field and label combination, built with
  [go-app](https://go-app.dev)
  (Go → WebAssembly, embedded in the binary; no Node/JS build).
- **Extensible without a schema change** — strict `key:value` **labels** for
  categorization, plus **[schema extensions](#schema-extensions)**: arbitrary,
  namespaced JSON metadata any app can attach to a transaction, so integrations
  innovate independently.
- **One small container** (`scratch` base — ~12 MB pulled, ~24 MB on disk for
  linux/amd64; the embedded WASM dashboard adds ~5 MB) with a bind-mounted
  SQLite file.
- **No external dependencies.** Optionally store the SimpleFIN access URL in
  HashiCorp Vault; otherwise it lives in a local `0600` file.
- **Prometheus metrics** at `/metrics`, structured `slog` logging, graceful
  shutdown.

## How it works

A background scheduler (`gocron`) polls a SimpleFIN bridge on an interval and
upserts organizations and accounts. New transactions are inserted by their
SimpleFIN id; a transaction that already exists has its bridge-owned fields
refreshed (so a pending charge that later posts, or a corrected amount, flows in)
while any **labels you added are always preserved** — a sync never clobbers your
labels. Every run is recorded in `sync_log`. You can also force a sync from the
dashboard's Settings page or `POST /api/v1/sync`.

```
SimpleFIN bridge ──poll──▶ kasas ──▶ SQLite ──▶ REST API  (/api/v1/...)
                                            ├──▶ MCP server (/mcp)
                                            └──▶ Webhooks ──push──▶ your apps
```

## Quick start (Docker)

Prebuilt multi-arch (amd64/arm64) images are published to GHCR on every release:

```sh
docker pull ghcr.io/paulmeier/kasas:latest
```

1. Get a **setup token** from your SimpleFIN bridge (a base64 string).
2. Start the service (the Compose file builds locally; swap in the GHCR `image:`
   to use the published one):

   ```sh
   export KASAS_SIMPLEFIN_SETUP_TOKEN="<your base64 setup token>"
   docker compose up -d --build
   ```

3. The token is claimed on first sync and the resulting access URL is persisted
   to `./data/secrets.json`, so the token is only used once. Check it worked:

   ```sh
   curl localhost:8080/api/v1/sync      # latest sync status
   curl localhost:8080/api/v1/accounts  # synced accounts
   ```

> **Volume permissions:** the container runs as UID `65532`. The mounted data
> directory must be writable by that user:
> `mkdir -p data && sudo chown -R 65532:65532 data`.
> On Unraid, point the volume at `/mnt/user/appdata/kasas` and adjust ownership
> to match.

## Quick start (local)

Requires Go 1.25+ (only to build; the running service needs nothing else).

```sh
cp config.example.toml config.toml      # edit as needed
make build
./bin/kasas -config config.toml serve
```

Or run a single sync and exit:

```sh
KASAS_SIMPLEFIN_SETUP_TOKEN="..." ./bin/kasas -config config.toml sync
```

## Configuration

Configuration comes from a TOML file (`-config path`) and/or environment
variables. Env vars are prefixed `KASAS_`, with sections joined by underscores
(`[server].addr` → `KASAS_SERVER_ADDR`) and win over the file. See
[`config.example.toml`](config.example.toml) for every option.

| Key | Env | Default | Description |
| --- | --- | --- | --- |
| `server.addr` | `KASAS_SERVER_ADDR` | `:8080` | HTTP listen address |
| `database.driver` | `KASAS_DATABASE_DRIVER` | `sqlite` | Backend: `sqlite` or `postgres` |
| `database.path` | `KASAS_DATABASE_PATH` | `/data/kasas.db` | SQLite file path (driver=sqlite) |
| `database.dsn` | `KASAS_DATABASE_DSN` | — | Postgres connection string (driver=postgres) |
| `simplefin.setup_token` | `KASAS_SIMPLEFIN_SETUP_TOKEN` | — | One-time base64 setup token |
| `simplefin.access_url` | `KASAS_SIMPLEFIN_ACCESS_URL` | — | Pre-claimed access URL |
| `sync.interval` | `KASAS_SYNC_INTERVAL` | `6h` | Poll interval (Go duration) |
| `sync.lookback_days` | `KASAS_SYNC_LOOKBACK_DAYS` | `90` | History window; `0` = all |
| `vault.enabled` | `KASAS_VAULT_ENABLED` | `false` | Use Vault for the access URL + dashboard token |
| `mcp.enabled` | `KASAS_MCP_ENABLED` | `true` | Mount the MCP server at `/mcp` |
| `dashboard.token` | `KASAS_DASHBOARD_TOKEN` | — | Access token for the API, dashboard, and MCP. Empty = unauthenticated (see [Authentication](#authentication)) |
| `update.check` | `KASAS_UPDATE_CHECK` | `true` | Daily check for a newer release (logs + dashboard banner) |
| `update.allow_apply` | `KASAS_UPDATE_ALLOW_APPLY` | `true` | Let the dashboard/API trigger an in-place self-update |
| `events.enabled` | `KASAS_EVENTS_ENABLED` | `true` | Record the [event stream](#event-stream) and [transaction history](#transaction-history) |
| `events.retention_days` | `KASAS_EVENTS_RETENTION_DAYS` | `0` | Prune events older than N days; `0` = keep forever |
| `events.history_retention_days` | `KASAS_EVENTS_HISTORY_RETENTION_DAYS` | `0` | Prune [transaction history](#transaction-history) snapshots older than N days; `0` = keep forever |

## Authentication

By default the REST API, the web dashboard, and the MCP-over-HTTP server are
**unauthenticated** — convenient on a trusted network, but anyone who can reach
the port can read your financial data and change settings. Set a **dashboard
token** to require callers to authenticate. It gates `/api/v1/*` and `/mcp`; the
`/healthz`, `/readyz`, and `/metrics` endpoints stay open (for container probes
and Prometheus).

Provide a token in any of three ways, in precedence order:

1. **Config / env** — `dashboard.token` or `KASAS_DASHBOARD_TOKEN`. Authoritative:
   when set it always applies, and you rotate it by changing the value and
   restarting.
2. **Generated in the dashboard** — open **Settings → Dashboard security** and
   click **Generate token** (or paste your own, ≥16 chars). It is saved to the
   secret store next to the SimpleFIN credential (the local `0600` `secrets.json`,
   or Vault when enabled), and used only when no config/env token is set. You can
   revoke it there too.
3. **None** — kasas logs a warning at startup and the dashboard shows an
   "unsecured" banner until you set one.

Clients authenticate with a bearer token:

```sh
curl -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" \
  http://localhost:8080/api/v1/accounts
```

The dashboard prompts for the token on first visit and remembers it in the
browser. MCP-over-HTTP clients send the same `Authorization: Bearer` header; the
stdio MCP transport (`kasas mcp`) is local and needs no token.

> It is a single shared secret (no per-user accounts). Generating/revoking from
> the dashboard is only possible when the token is **not** config-managed. A token
> complements, but does not replace, keeping kasas on a trusted network.

### API keys

The dashboard token is a single shared **admin** secret. For **external
integrations** — a budgeting app, a tax tool, a fraud detector, a notifier —
provision a separate **API key** per consumer instead, so access is scoped and
revocable independently. Keys are scoped:

- **`read`** — `GET` endpoints only (accounts, transactions, search, events, …).
- **`read_write`** — also mutations (edit labels, manage rules, trigger a sync).

Provisioning stays **admin-only** (the dashboard token): an API key can never mint
another key, manage webhooks, rotate the token, or set the SimpleFIN credential.
Mint, list, and revoke keys under **Settings → API keys**, or over REST/MCP:

```sh
# Mint a read-only key. The full secret is returned ONCE — store it now.
curl -X POST -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"Budgeting app","scope":"read"}' \
  http://localhost:8080/api/v1/security/api-keys
# -> {"id":1,"name":"Budgeting app","prefix":"kasas_AbCd1234","scope":"read","key":"kasas_…"}

# Use it exactly like the dashboard token:
curl -H "Authorization: Bearer kasas_…" http://localhost:8080/api/v1/transactions
```

Only a SHA-256 **hash** of each key is stored, so a database leak never exposes
usable credentials — the full secret is shown exactly once, at creation. The MCP
tools `list_api_keys`, `create_api_key`, and `revoke_api_key` do the same over MCP.

## Storage backends

kasas runs on **SQLite** (default) or **Postgres**, selected at runtime — the
same binary supports both. Schema migrations and type-safe queries are generated
for each dialect, so switching backends needs no code changes.

**SQLite** (default) needs nothing: an embedded, file-based database opened in
WAL mode at `database.path`. Ideal for a single-container deployment.

**Postgres** — point kasas at a server and it creates its schema on first start:

```sh
KASAS_DATABASE_DRIVER=postgres \
KASAS_DATABASE_DSN="postgres://user:pass@host:5432/kasas?sslmode=disable" \
kasas serve
```

With Docker Compose, an optional Postgres service is included:

```sh
# edit docker-compose.yml: set KASAS_DATABASE_DRIVER=postgres + KASAS_DATABASE_DSN
docker compose --profile postgres up -d
```

> Switching backends does not migrate existing data between them; each backend
> keeps its own database.

## Updating

**Docker** — pull the new image and recreate the container:

```sh
docker pull ghcr.io/paulmeier/kasas:latest
docker compose up -d
```

**Binary** — every release publishes static binaries (linux/darwin × amd64/arm64)
with SHA-256 checksums to [GitHub Releases](https://github.com/paulmeier/kasas/releases).
The binary can update itself in place:

```sh
kasas self-update          # download, verify, and replace the running binary
kasas self-update -check   # report whether a newer release exists; install nothing
```

`self-update` fetches the latest release, downloads the asset matching your
OS/arch, verifies it against the published `.sha256` (refusing to proceed on a
mismatch or a missing checksum), and atomically replaces the binary. You need
write access to its directory; restart the service afterwards to run the new
version.

While `serve` runs, kasas also checks **once a day** for a newer release and logs
a notice — it never self-modifies. Disable the check with `KASAS_UPDATE_CHECK=false`
(recommended for Docker, where you update by pulling a new image). Builds without
a release version (e.g. `dev`) skip the check entirely.

**From the dashboard** — when a newer release is available, the dashboard shows a
banner at the top with an **"Update & restart"** button. Clicking it calls
`POST /api/v1/update`, which performs the same download → verify → replace as the
CLI and then **re-execs the new binary in place** (no external supervisor needed);
the page reloads onto the new version once it's back. The button is backed by the
same `update.allow_apply` switch:

> **Security:** with `allow_apply` on, anyone who can reach the (authenticated, if
> a [dashboard token](#authentication) is set) API can replace the running binary
> with a checksum-verified GitHub release and restart it. Set a dashboard token,
> keep kasas on a trusted network — e.g. [Tailscale](https://tailscale.com) — or
> set `KASAS_UPDATE_ALLOW_APPLY=false` to keep the informational banner while
> requiring the `kasas self-update` CLI to actually upgrade.

## Dashboard

When `dashboard.enabled` is true (the default), kasas serves a lightweight web UI
at the root path (`/`). A collapsible left sidebar navigates between six pages:

- **Dashboard** — a balance card per account, and a transactions table (date,
  account, payee/description, color-coded amount, pending badge) with an account
  filter, sortable columns, a selectable page size (10/20/50/100), and pagination.
- **Search** — a query box over a robust search language (any field plus any
  combination of labels, with `AND`/`OR`/`NOT`, ranges, and grouping), a scrollable
  syntax help modal, and a results table with the same sorting, pagination, and
  inline label editing. Results persist across navigation (the last query is
  re-run on return). See the [search syntax](#search-syntax) below.
- **Labels** — every label (a `key: value` pair) with the number of transactions
  carrying it, and a delete that strips it from all of them. (Labels are created
  on the Dashboard.)
- **Rules** — create rules that automatically label transactions. Each rule pairs
  a condition (a query in the [search syntax](#search-syntax)) with the labels to
  apply; it runs against every newly-synced transaction, and a **Run** button
  applies a rule (or all enabled rules) to existing transactions. Edit,
  enable/disable, and delete inline. See [Rules](#rules) below.
- **Events** — a live feed of the [event stream](#event-stream): the most recent
  events, polled forward as new ones arrive, with the type, entity, time, and an
  expandable JSON payload per row.
- **Settings** — connect to SimpleFIN by pasting a setup token or access URL
  (stored securely and used on the next sync, no restart), generate or revoke the
  [dashboard token](#authentication) that secures kasas, force a sync with live
  status, and review the effective configuration (read-only, secrets redacted).

The sidebar collapses to an icon rail; the choice is remembered across pages.
Browsing is read-only except for **labels**: each transaction has an editable
Labels cell where you can add or remove `key: value` labels (type e.g.
`category: food`, or `tag: groceries` for a simple label), with typeahead
suggestions drawn from your existing labels (the Labels column itself is not
sortable). It's a
[go-app](https://go-app.dev) PWA — the UI is written in Go, compiled to
WebAssembly, embedded in the binary (served gzipped, ~3 MB), and reads from the
same-origin REST API. Turn it off with `KASAS_DASHBOARD_ENABLED=false` (the WASM
is still embedded; the route is just not served).

## REST API

All responses are JSON. Timestamps are RFC 3339 (UTC); money fields are exact
decimal strings as returned by SimpleFIN.

| Method & path | Description |
| --- | --- |
| `GET /healthz` | Liveness probe (`200 ok`) |
| `GET /readyz` | Readiness probe (pings the database) |
| `GET /metrics` | Prometheus metrics |
| `GET /api/v1/organizations` | List organizations |
| `GET /api/v1/accounts` | List accounts (`?org_id=` to filter) |
| `GET /api/v1/accounts/{id}` | Get one account |
| `GET /api/v1/accounts/{id}/transactions` | Transactions for an account |
| `GET /api/v1/transactions` | List transactions (`?label_key=` and optional `?label_value=` to drill down) |
| `GET /api/v1/transactions/search` | Search transactions with the query language (`?q=`); returns `{query, total, transactions}` |
| `GET /api/v1/transactions/{id}` | Get one transaction |
| `PUT /api/v1/transactions/{id}/labels` | Replace a transaction's labels (`{"labels":{"category":"food"}}`) |
| `PUT /api/v1/transactions/{id}/extensions` | Replace a transaction's [schema extensions](#schema-extensions) (`{"extensions":{"tax.category":"meal"}}`) |
| `GET /api/v1/transactions/{id}/history` | The transaction's [version history](#transaction-history): full snapshots + per-version diffs |
| `GET /api/v1/labels` | List labels with per-pair transaction counts (`[{"key","value","transaction_count"}]`) |
| `DELETE /api/v1/labels/{key}` | Remove a label key from every transaction (add `?value=` to scope to one value) |
| `GET /api/v1/extensions` | List the extension vocabulary with per-key transaction counts (`[{"namespace","key","transaction_count"}]`) |
| `GET /api/v1/rules` | List labeling rules |
| `POST /api/v1/rules` | Create a rule (`{"name","query","labels":{…},"enabled"}`); validates the query, 400 on error |
| `GET /api/v1/rules/{id}` | Get one rule |
| `PUT /api/v1/rules/{id}` | Replace a rule |
| `DELETE /api/v1/rules/{id}` | Delete a rule |
| `POST /api/v1/rules/{id}/run` | Apply one rule to existing transactions (even if disabled); returns `{matched, updated}` |
| `POST /api/v1/rules/run` | Apply all enabled rules to existing transactions |
| `GET /api/v1/sync` | Latest sync status |
| `GET /api/v1/sync/history` | Recent sync runs (`?limit=`) |
| `POST /api/v1/sync` | Trigger a sync (runs async, returns `202`) |
| `GET /api/v1/events` | Read the [event stream](#event-stream) from a cursor (`?after=`, `?type=`, `?entity_type=`, `?entity_id=`, `?limit=`); `?newest` for the latest. Returns `{events, next}` |
| `GET /api/v1/events/{sequence}` | Get one event by its sequence number |
| `GET /api/v1/events/stream` | Live event tail over Server-Sent Events (add `?after=` to replay from a cursor, then follow) |
| `GET /api/v1/config` | Effective configuration, secrets redacted (powers the Settings page) |
| `PUT /api/v1/simplefin/credential` | Set the SimpleFIN setup token or access URL (`{"token":"..."}`) |
| `GET /api/v1/update` | Update status (when `update.check` is on) |
| `POST /api/v1/update` | Install the latest release in place (when `update.allow_apply` is on) |

List endpoints accept `?limit=` (default 100, max 1000), `?offset=`, and
`?since=`/`?until=` (a `YYYY-MM-DD` date, RFC 3339, or unix seconds).

Labels are strict `key: value` pairs (both non-empty strings) stored as a JSON
object per transaction. Keys are canonicalized to lowercase; value matching is
exact (case-sensitive). Drill down with `?label_key=` (any value) plus an
optional `?label_value=` for an exact match — the filter is pushed down to JSON
SQL in both the SQLite and Postgres backends, so callers can build their own
views without scanning every row.

```sh
curl "localhost:8080/api/v1/transactions?since=2024-01-01&limit=50"
curl "localhost:8080/api/v1/transactions?label_key=category&label_value=food"
curl "localhost:8080/api/v1/transactions/search?q=coffee%20amount:%3C0%20date:2024"
```

## Schema extensions

Rigid schemas kill platform adoption, so kasas lets any app attach its own
**namespaced metadata** to a transaction without a schema change. Extensions are
a JSON object of dotted keys to **arbitrary JSON values** (string, number,
boolean, null, object, array):

```json
{
  "id": "…",
  "amount": "-45.23",
  "extensions": {
    "tax.category": "meal",
    "forecast.recurring": true,
    "custom.myapp.score": 88
  }
}
```

They are **parallel to labels, not a replacement**: labels are strict
`key: value` *strings* for user categorization; extensions are app-owned data, so
keys are namespaced and **case-preserved** and values keep their JSON type. A
`PUT` replaces the whole set (send the full object; `{}` clears it); the values
are stored verbatim. Re-syncs never touch them. Write via the REST API or the
`set_transaction_extensions` MCP tool; the dashboard displays them read-only.
Filter on them with the search language's `ext:` field (below).

```sh
curl -X PUT localhost:8080/api/v1/transactions/<id>/extensions \
  -H 'content-type: application/json' \
  -d '{"extensions":{"tax.category":"meal","forecast.recurring":true,"custom.myapp.score":88}}'
curl "localhost:8080/api/v1/extensions"
curl "localhost:8080/api/v1/transactions/search?q=ext:tax.category=meal"
```

## Search syntax

The Search page and the `/transactions/search` endpoint (and the
`search_transactions` MCP tool) share one query language, evaluated in Go over
every transaction, so it covers any stored field and arbitrary label
combinations. Matching is case-insensitive; an empty query matches everything.

| Form | Meaning |
| --- | --- |
| `coffee` / `"whole foods"` | free text across description, payee, memo, account, id, labels, and extensions |
| `description:` `payee:` `memo:` `account:` `id:` | substring on that field (quote for phrases) |
| `amount:>50` `amount:<0` `amount:10..50` | numeric compare (`> >= < <= = !=`) or range (sign-aware) |
| `date:2024` `date:2024-03` `date:>=2024-01-01` `date:2024-01..2024-06` | year / month / day, compare, or range |
| `pending:true` | the pending flag |
| `label:category=food` / `category:food` | label key = value (the second is shorthand) |
| `label:category` | label key present (any value) |
| `label:store~whole` / `label:category!=food` | label value contains / not-equal |
| `ext:tax.category=meal` | extension key = value (values matched as text; e.g. `ext:forecast.recurring=true`) |
| `ext:custom.myapp.score` | extension key present (any value) |
| `ext:tax.category~me` / `ext:tax.category!=meal` | extension value contains / not-equal |
| `a OR b`, `a b` (implicit AND), `-a` / `NOT a`, `(a OR b) c` | boolean combine, negate, group |

```sh
# coffee outflows in 2024 that aren't reimbursed
curl "localhost:8080/api/v1/transactions/search?q=coffee%20amount:%3C0%20date:2024%20-label:reimbursed"
```

## Rules

Rules automatically label transactions. A rule pairs a **condition** — a query in
the [search syntax](#search-syntax) above — with one or more **labels** to apply
to every transaction it matches. Enabled rules run against each newly-synced
transaction automatically; you can also **run** a rule (or all enabled rules) over
your existing transactions at any time. A matching rule is authoritative for its
own label keys (it overwrites a different existing value) and leaves other labels
alone; re-running is idempotent. Manage rules on the dashboard's Rules page, over
the REST API, or via MCP — they are stored in a `rules` table (the one piece of
state that is not derived from synced data).

```sh
# flag large coffee charges for review, then apply the rule to existing transactions
id=$(curl -s localhost:8080/api/v1/rules \
  -d '{"name":"Coffee review","query":"description:coffee amount:>50","labels":{"status":"review"}}' \
  | jq .id)
curl -X POST "localhost:8080/api/v1/rules/$id/run"   # -> {"matched":3,"updated":3}
```

## Event stream

kasas records a **canonical event stream**: an append-only, ordered, replayable
log of every meaningful change. It is the substrate for sync engines,
notifications, automations, external consumers, and CQRS / event-sourcing — read
it to learn *what changed and when*, rather than re-diffing current state. Toggle
it with `events.enabled` (default on); it is exposed over REST, SSE, MCP
(`list_events`), and the dashboard's **Events** page.

Each event is a self-contained envelope — `data` carries a snapshot of the
entity, so a consumer needs no follow-up query (and a `*.deleted` event still
carries the entity's last-known state):

```json
{
  "sequence":    42,
  "event_id":    "550e8400-e29b-41d4-a716-446655440000",
  "type":        "transaction.created",
  "entity_type": "transaction",
  "entity_id":   "abc-123",
  "occurred_at": "2026-06-06T12:34:56Z",
  "data":        { "id": "abc-123", "account_id": "...", "amount": "-12.34", "...": "..." }
}
```

Event types: `transaction.created` / `transaction.updated` (and the reserved
`transaction.deleted`), `account.created` / `account.updated`, `label.applied` /
`label.removed`, `extension.set` / `extension.removed`, `rule.created` /
`rule.updated` / `rule.deleted` / `rule.executed`, and `sync.completed`. (A bulk
label-vocabulary delete emits one coarse `label.removed` with `entity_type:
"label"`; single-transaction label and extension edits emit granular per-key
events with `entity_type: "transaction"`.)

**Consumer contract:** order by `sequence` and dedupe on `event_id`. `sequence`
is strictly increasing but **may have gaps** (a rolled-back change consumes a
value), so treat it as a cursor, not a contiguous count. Events and the changes
that produce them are written in the same database transaction, so the stream
never contains an event whose change was rolled back.

```sh
# Poll forward from a cursor (a sync engine's main loop):
curl "localhost:8080/api/v1/events?after=42&limit=100"   # -> {"events":[…],"next":57}

# Everything for one transaction (its timeline):
curl "localhost:8080/api/v1/events?entity_type=transaction&entity_id=abc-123"

# Follow live over SSE (header auth works for non-browser clients):
curl -N -H "Authorization: Bearer $TOKEN" localhost:8080/api/v1/events/stream
```

Real-time delivery uses Server-Sent Events at `/api/v1/events/stream`: pass
`?after=<sequence>` to replay the backlog and then follow live, or omit it to
stream only new events. Each SSE frame carries the event's `sequence` as its `id`
(so a reconnecting client resumes via `?after`), the `type` as the event name,
and the full envelope as the JSON `data` payload. A subscriber that falls too far
behind is dropped and should reconnect with its last sequence as `?after` to
replay the gap. Note that a browser `EventSource` cannot send an `Authorization`
header, so the dashboard's Events page polls `/api/v1/events` rather than using
SSE; non-browser consumers (curl, a Go service) use SSE with a Bearer token.

The log grows append-only. By default it is kept forever so the stream is fully
replayable from sequence 0; set `events.retention_days` to a positive number to
prune events older than that many days on a schedule (a consumer offline longer
than the window then loses the pruned history).

## Transaction history

Every meaningful change to a transaction also appends an immutable, **full
snapshot** to its history, so you can answer *"why does this transaction look
different today than last month?"* The timeline reads `v1 imported` (the row as it
was first synced, including any labels a rule applied at birth), then `v2 synced`
(the bank corrected the amount or merchant, or a pending charge posted), `v3
labeled` (you or a rule changed its labels), `extended` (an app changed its schema
extensions), and so on. Each version carries the complete snapshot plus a computed
**diff** against the previous one (changed fields and label/extension
add/remove/change).

This complements the [event stream](#event-stream): events are a fine-grained,
prunable change log (a `label.applied` carries only the changed key); history is
the durable, whole-transaction record. Recording rides on `events.enabled`, but its
retention is independent — history is meant to be kept far longer than the noisy
event log, so `events.history_retention_days` defaults to `0` (keep forever).

```sh
# One transaction's full history (oldest first), each with a diff vs the prior version:
curl "localhost:8080/api/v1/transactions/abc-123/history"
# -> {"transaction_id":"abc-123","versions":[{"version":1,"change_kind":"imported",…,"diff":{…}}, …]}
```

Transactions that predate this feature get their first version lazily: the first
time one changes after upgrading, a `v1` baseline is captured from its current
state, then the change is recorded as `v2`. Until then its history is empty.

In the dashboard, hover a transaction row and click the clock to open its history
timeline. Over MCP, call `get_transaction_history`.

## Webhooks

Webhooks turn the [event stream](#event-stream) into an **outbound push**: kasas
`POST`s each subscribed event to an HTTP endpoint you register, HMAC-signed, so
external apps react to changes without polling. This makes kasas an **integration
hub** — budgeting, accounting, tax, fraud detection, and notification apps build on
the events without kasas implementing any of them.

Register endpoints on the dashboard **Webhooks** page, via `POST /api/v1/webhooks`,
or the `create_webhook` MCP tool. Subscribe to specific event types (the same
taxonomy as the event stream) or to all of them with `*`:

```sh
curl -X POST -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://my-app.example.com/hooks/kasas","event_types":["transaction.created","transaction.updated"]}' \
  http://localhost:8080/api/v1/webhooks
# -> {"id":1,"url":…,"event_types":[…],"enabled":true,"secret":"whsec_…"}
```

Each delivery is a JSON `POST` of the event (the same envelope as the REST/SSE
event) with these headers:

```
X-Kasas-Event:     transaction.created
X-Kasas-Event-Id:  <uuid>             # idempotency / dedupe key
X-Kasas-Timestamp: <unix seconds>
X-Kasas-Signature: sha256=<hex HMAC>
```

**Verify the signature** by recomputing `HMAC-SHA256(secret, "<timestamp>.<body>")`
over the raw request body and comparing in constant time (reject stale timestamps to
thwart replay):

```python
expected = "sha256=" + hmac.new(secret.encode(), f"{ts}.{raw_body}".encode(),
                                 hashlib.sha256).hexdigest()
assert hmac.compare_digest(expected, request.headers["X-Kasas-Signature"])
```

**Reliability.** Delivery is best-effort: kasas retries with exponential backoff
(`webhooks.max_attempts`, `webhooks.timeout`), and the dashboard shows each
endpoint's last-delivery health. It is *not* a durable queue — if an endpoint is
down or kasas restarts, missed events are reconciled the way any consumer catches
up: replay from your last cursor via `GET /api/v1/events?after=<sequence>`. That is
exactly why the durable event stream exists.

Manage webhooks over REST (`GET`/`POST`/`PUT`/`DELETE /api/v1/webhooks`, plus
`/{id}/test` to send a test delivery and `/{id}/rotate-secret`) or the MCP tools
(`list_webhooks`, `create_webhook`, `update_webhook`, `delete_webhook`,
`test_webhook`). Webhooks ride on the event stream, so they require `events.enabled`.

## Plugins

Plugins are the **in-process** counterpart to webhooks: instead of pushing events to
an external service, a plugin runs *inside* kasas, in a sandboxed language VM, and
reacts to the same committed events. They let developers extend kasas — tax,
budgeting, forecasting, notifications — without bloating the core ledger. v1 ships
the **Lua** runtime ([gopher-lua], pure Go, no cgo); JavaScript/TypeScript and Go
(via WASM) are planned behind the same adapter seam.

Plugins run **asynchronously, after commit** (like webhooks, not like the synchronous
rules engine): a slow or crashing plugin can never block or corrupt a sync. Each
plugin runs on its own goroutine with a per-hook timeout, and a panic becomes a
recorded error, not a crash.

**Install** a plugin by dropping a directory into the plugins folder
(`plugins.dir`, default `/data/plugins`):

```
/data/plugins/
  budgeting/
    plugin.toml      # manifest (required)
    main.lua         # entrypoint (manifest `entrypoint`, default main.lua)
```

The `plugin.toml` declares the lifecycle **hooks** the plugin implements and the
**capabilities** it needs:

```toml
name        = "budgeting"          # must match the directory name
version     = "0.1.0"
description = "Auto-categorize spending"
runtime     = "lua"
entrypoint  = "main.lua"

# Hooks fired by the matching events: OnTransactionCreate (transaction.created),
# OnTransactionUpdate (transaction.updated), OnSyncComplete (sync.completed).
hooks = ["OnTransactionCreate", "OnTransactionUpdate"]

# Capabilities the host grants (and enforces): transactions:read, labels:write,
# extensions:write. A call to a host function the plugin wasn't granted errors.
capabilities = ["transactions:read", "labels:write"]

[config]                            # arbitrary config, exposed to the plugin as kasas.config
keyword = "coffee"
```

The plugin implements its declared hooks as global functions and acts through the
capability-checked `kasas` **host API** (`kasas.get_transaction`, `kasas.search`,
`kasas.apply_labels`, `kasas.remove_labels`, `kasas.set_extension`,
`kasas.remove_extension`, `kasas.log`, and `kasas.config`):

```lua
function OnTransactionCreate(txn)
  if string.find(string.lower(txn.description), kasas.config.keyword, 1, true) then
    kasas.apply_labels(txn.id, { category = "food" })   -- routes through the normal
    kasas.log("info", "tagged", { id = txn.id })        -- emitter: emits label.applied
  end
end

function OnTransactionUpdate(txn) OnTransactionCreate(txn) end
```

Because a plugin's writes go through the same emitter as a REST or rules edit, they
produce the normal `label.applied` / `extension.set` events and transaction history
— and flow to webhooks and other consumers.

**Enable** is per-plugin and opt-in (a plugin is third-party code). Discovered
plugins start **disabled**; enabling one loads and runs its code, so that action is
**admin-only** (the dashboard token, never an API key):

```sh
curl -s -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" http://localhost:8080/api/v1/plugins
# -> {"plugins":[{"id":1,"name":"budgeting","state":"disabled","hooks":[…],…}]}

curl -X POST -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" \
  http://localhost:8080/api/v1/plugins/1/enable
# -> {"id":1,"name":"budgeting","enabled":true,"loaded":true,"state":"loaded",…}
```

Manage plugins on the dashboard **Plugins** page (status/health, enable/disable
toggle, reload), over REST (`GET /api/v1/plugins`, `GET /api/v1/plugins/{id}`, and
admin-tier `POST /api/v1/plugins/{id}/{enable,disable,reload}`), or the MCP tools
(`list_plugins`, `get_plugin`, `enable_plugin`, `disable_plugin`, `reload_plugin`).
Plugins ride the event bus, so they require `events.enabled` and `plugins.enabled`.

**Sandbox & limits (v1).** The Lua VM opens only safe libraries — no filesystem,
process, network, or dynamic code loading — and each hook is bounded by
`plugins.hook_timeout`. It is **not** a hard memory sandbox (a buggy plugin can still
allocate without bound), so the v1 trust model is *operator-installed, opt-in* plugins;
a stronger WASM sandbox with hard resource caps (and a plugin marketplace) is the
planned next step.

[gopher-lua]: https://github.com/yuin/gopher-lua

## MCP server

When `mcp.enabled` is true, an MCP server is mounted at `/mcp` over the
streamable-HTTP transport. It exposes tools: `list_accounts`, `get_account`,
`list_transactions` (with optional `label_key`/`label_value` drill-down),
`search_transactions` (the query language above, including `ext:`), `list_labels`,
`set_transaction_extensions` (replace a transaction's [schema extensions](#schema-extensions))
and `list_extensions` (the extension vocabulary),
`get_transaction_history` (one transaction's [version history](#transaction-history)
with per-version diffs), `list_organizations`, `sync_status`, `trigger_sync`,
`list_events` (read the [event stream](#event-stream): cursor with `after`, filter
by `type`/`entity_type`/`entity_id`), and the rules tools `list_rules`,
`create_rule`, `update_rule`, `delete_rule`, and `run_rules` (pass an `id` to run
one rule, or omit it to run all enabled rules).

For desktop MCP clients that launch a subprocess, run it over stdio instead:

```sh
kasas -config config.toml mcp
```

## Metrics

`/metrics` exposes, among the Go/process defaults:

- `kasas_sync_total{status}` — sync runs by outcome
- `kasas_sync_duration_seconds` — sync duration histogram
- `kasas_transactions_inserted_total` — new transactions inserted
- `kasas_rules_applied_total` — new transactions auto-labeled by a rule
- `kasas_events_emitted_total{type}` — events appended to the stream, by type
- `kasas_events_dropped_total` — live event subscribers dropped for lagging
- `kasas_transaction_versions_total{kind}` — [history](#transaction-history) snapshots recorded, by change kind
- `kasas_last_successful_sync_timestamp_seconds` — last success (unix time)
- `kasas_accounts` — accounts seen in the most recent sync

## Database schema

Managed by embedded [goose](https://github.com/pressly/goose) migrations under
[`migrations/`](migrations) (one dialect-specific set per backend in
`migrations/sqlite` and `migrations/postgres`), applied automatically on startup
(or via `kasas migrate`):

```
organizations  id, domain, name, sfin_url
accounts       id, org_id, name, currency, balance, balance_date, synced_at
transactions   id, account_id, amount, pending, date, description, payee, memo, synced_at, labels
rules          id, name, query, labels, enabled, created_at, updated_at
events         id, event_id, event_type, entity_type, entity_id, occurred_at, data
transaction_versions  id, transaction_id, change_kind, occurred_at, data
sync_log       id, started_at, completed_at, status, error
```

`labels` is a JSON object of `key: value` pairs (see [REST API](#rest-api)).
Type-safe access code is generated from [`queries/`](queries) — shared queries
plus per-dialect `queries/sqlite` and `queries/postgres` for the JSON label
filtering — by [sqlc](https://sqlc.dev) for both dialects: SQLite into
[`internal/db/`](internal/db) and Postgres into
[`internal/db/pg/`](internal/db/pg). A small `db.Store` abstraction lets the rest
of the app stay backend-agnostic.

## Development

```sh
make help       # list targets
make generate   # regenerate sqlc code after editing queries/ or migrations/
make test       # run tests
make test-race  # run tests with the race detector
make cover      # coverage report (HTML)
make build      # static binary -> bin/kasas
make docker     # build the container image
```

### Testing

Tests use [testify](https://github.com/stretchr/testify). Each suite gets an
isolated, fully migrated SQLite database via `internal/testutil`, so there are
no mocks for the data layer — queries run against real SQLite. Coverage spans
the config loader, the secret store, the generated queries (ordering, filters,
idempotency, foreign keys), the SimpleFIN client and poller (against `httptest`
servers), the REST API (`httptest` round-trips), and the MCP tools (driven
through a real in-process client session).

The Postgres backend has an integration test that runs against a real database
when `KASAS_TEST_POSTGRES_DSN` is set, and is skipped otherwise (so the default
suite needs no database):

```sh
docker run -d --name kasas-pg -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=kasas \
  -p 5432:5432 postgres:16-alpine
KASAS_TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/kasas?sslmode=disable" \
  go test ./internal/db/
```

### Project layout

```
cmd/kasas/          main entrypoint and subcommands
cmd/kasas-wasm/     dashboard WebAssembly client entrypoint (GOOS=js GOARCH=wasm)
internal/api/       chi routes, REST handlers, MCP server
internal/dashboard/ go-app dashboard UI + handler (served at /)
internal/config/    viper configuration
internal/db/        SQLite sqlc output + Store interface + Postgres adapter
internal/db/pg/      Postgres sqlc output
internal/poller/    SimpleFIN client + gocron scheduler
internal/vault/     secret store (Vault KV v2, with local-file fallback)
internal/testutil/  shared test database + fixtures
migrations/sqlite/   embedded goose migrations (SQLite)
migrations/postgres/ embedded goose migrations (Postgres)
queries/            shared sqlc query definitions (both dialects)
```

## Contributing

See **[CONTRIBUTING.md](CONTRIBUTING.md)** for local development: configuring and
running the service against SQLite or Postgres, the test suite (including the
gated Postgres integration test), regenerating sqlc code, linting, and the
Conventional-Commits / release-please flow. CI (gofmt, lint, race tests against
SQLite *and* Postgres, and a build stage) must pass on every PR.

## License

[MIT](LICENSE)
