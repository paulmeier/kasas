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
                                            └──▶ MCP server (/mcp)
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
| `vault.enabled` | `KASAS_VAULT_ENABLED` | `false` | Use Vault for the access URL |
| `mcp.enabled` | `KASAS_MCP_ENABLED` | `true` | Mount the MCP server at `/mcp` |
| `update.check` | `KASAS_UPDATE_CHECK` | `true` | Daily check for a newer release (logs + dashboard banner) |
| `update.allow_apply` | `KASAS_UPDATE_ALLOW_APPLY` | `true` | Let the dashboard/API trigger an in-place self-update |

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

> **Security:** the dashboard and API are unauthenticated, so with `allow_apply`
> on, anyone who can reach kasas can replace the running binary (with a
> checksum-verified GitHub release) and restart it. Keep kasas on a trusted
> network — e.g. [Tailscale](https://tailscale.com) — or set
> `KASAS_UPDATE_ALLOW_APPLY=false` to keep the informational banner while
> requiring the `kasas self-update` CLI to actually upgrade.

## Dashboard

When `dashboard.enabled` is true (the default), kasas serves a lightweight web UI
at the root path (`/`). A collapsible left sidebar navigates between five pages:

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
- **Settings** — connect to SimpleFIN by pasting a setup token or access URL
  (stored securely and used on the next sync, no restart), force a sync with live
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
| `GET /api/v1/labels` | List labels with per-pair transaction counts (`[{"key","value","transaction_count"}]`) |
| `DELETE /api/v1/labels/{key}` | Remove a label key from every transaction (add `?value=` to scope to one value) |
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

## Search syntax

The Search page and the `/transactions/search` endpoint (and the
`search_transactions` MCP tool) share one query language, evaluated in Go over
every transaction, so it covers any stored field and arbitrary label
combinations. Matching is case-insensitive; an empty query matches everything.

| Form | Meaning |
| --- | --- |
| `coffee` / `"whole foods"` | free text across description, payee, memo, account, id, and labels |
| `description:` `payee:` `memo:` `account:` `id:` | substring on that field (quote for phrases) |
| `amount:>50` `amount:<0` `amount:10..50` | numeric compare (`> >= < <= = !=`) or range (sign-aware) |
| `date:2024` `date:2024-03` `date:>=2024-01-01` `date:2024-01..2024-06` | year / month / day, compare, or range |
| `pending:true` | the pending flag |
| `label:category=food` / `category:food` | label key = value (the second is shorthand) |
| `label:category` | label key present (any value) |
| `label:store~whole` / `label:category!=food` | label value contains / not-equal |
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

## MCP server

When `mcp.enabled` is true, an MCP server is mounted at `/mcp` over the
streamable-HTTP transport. It exposes tools: `list_accounts`, `get_account`,
`list_transactions` (with optional `label_key`/`label_value` drill-down),
`search_transactions` (the query language above), `list_labels`,
`list_organizations`, `sync_status`, `trigger_sync`, and the rules tools
`list_rules`, `create_rule`, `update_rule`, `delete_rule`, and `run_rules`
(pass an `id` to run one rule, or omit it to run all enabled rules).

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
