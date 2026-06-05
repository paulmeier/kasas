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
- **Read-only web dashboard** at `/` — an account overview + a filterable,
  paginated transactions table, built with [go-app](https://go-app.dev) (Go →
  WebAssembly, embedded in the binary; no Node/JS build).
- **One small container** (`scratch` base — ~12 MB pulled, ~24 MB on disk for
  linux/amd64; the embedded WASM dashboard adds ~5 MB) with a bind-mounted
  SQLite file.
- **No external dependencies.** Optionally store the SimpleFIN access URL in
  HashiCorp Vault; otherwise it lives in a local `0600` file.
- **Prometheus metrics** at `/metrics`, structured `slog` logging, graceful
  shutdown.

## How it works

A background scheduler (`gocron`) polls a SimpleFIN bridge on an interval,
upserts organizations and accounts, and inserts transactions idempotently
(`transactions.id` is the SimpleFIN transaction ID, inserted with
`ON CONFLICT DO NOTHING`). Every run is recorded in `sync_log`.

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

## Dashboard

When `dashboard.enabled` is true (the default), kasas serves a lightweight,
**read-only** web UI at the root path (`/`): a balance card per account, and a
transactions table (date, account, payee/description, color-coded amount,
pending badge) with an account filter and "load more" paging. It's a
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
| `GET /api/v1/transactions` | List transactions |
| `GET /api/v1/transactions/{id}` | Get one transaction |
| `GET /api/v1/sync` | Latest sync status |
| `GET /api/v1/sync/history` | Recent sync runs (`?limit=`) |
| `POST /api/v1/sync` | Trigger a sync (runs async, returns `202`) |

List endpoints accept `?limit=` (default 100, max 1000), `?offset=`, and
`?since=`/`?until=` (a `YYYY-MM-DD` date, RFC 3339, or unix seconds).

```sh
curl "localhost:8080/api/v1/transactions?since=2024-01-01&limit=50"
```

## MCP server

When `mcp.enabled` is true, an MCP server is mounted at `/mcp` over the
streamable-HTTP transport. It exposes tools: `list_accounts`, `get_account`,
`list_transactions`, `list_organizations`, `sync_status`, and `trigger_sync`.

For desktop MCP clients that launch a subprocess, run it over stdio instead:

```sh
kasas -config config.toml mcp
```

## Metrics

`/metrics` exposes, among the Go/process defaults:

- `kasas_sync_total{status}` — sync runs by outcome
- `kasas_sync_duration_seconds` — sync duration histogram
- `kasas_transactions_inserted_total` — new transactions inserted
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
transactions   id, account_id, amount, pending, date, description, payee, memo, synced_at
sync_log       id, started_at, completed_at, status, error
```

Type-safe access code is generated from the shared [`queries/`](queries) by
[sqlc](https://sqlc.dev) for both dialects: SQLite into
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
