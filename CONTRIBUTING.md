# Contributing to kasas

Thanks for helping out! This guide covers configuring kasas for **local
development and testing**, the conventions CI enforces, and how releases work.

## Prerequisites

- **Go** 1.25+ (the build needs nothing else — pure-Go SQLite and Postgres
  drivers, no CGO).
- **Docker** (optional) — only for running a local Postgres or building the
  container image.
- Developer tools (installed on demand):
  - [`sqlc`](https://sqlc.dev) — regenerates DB code: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`
  - [`golangci-lint`](https://golangci-lint.run) v2 — linting: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2` (or `brew install golangci-lint`)

`make help` lists every task.

## Getting started

```sh
git clone git@github.com:paulmeier/kasas.git
cd kasas
make build          # -> bin/kasas
./bin/kasas version
```

## Configuration

kasas reads a TOML file (`-config path`) and/or environment variables. Env vars
are prefixed `KASAS_`, sections joined by `_`, and win over the file
(`[server].addr` → `KASAS_SERVER_ADDR`). Copy the documented example to start:

```sh
cp config.example.toml config.toml   # git-ignored
```

For local runs the handy knobs are:

| Setting | Env | Dev tip |
| --- | --- | --- |
| `server.addr` | `KASAS_SERVER_ADDR` | e.g. `127.0.0.1:8080` |
| `log.level` / `log.format` | `KASAS_LOG_LEVEL` / `KASAS_LOG_FORMAT` | `debug` / `text` are nicer locally |
| `database.driver` | `KASAS_DATABASE_DRIVER` | `sqlite` (default) or `postgres` |
| `database.path` | `KASAS_DATABASE_PATH` | e.g. `./dev.db` |
| `sync.run_on_start` | `KASAS_SYNC_RUN_ON_START` | `false` to avoid syncing on boot |
| `simplefin.setup_token` | `KASAS_SIMPLEFIN_SETUP_TOKEN` | a real base64 token to sync live data |
| `secrets.file` | `KASAS_SECRETS_FILE` | e.g. `./dev-secrets.json` |

You can develop without any SimpleFIN credentials — the service starts, the API
serves (empty data), and a sync simply fails with a clear "no access URL"
message. To exercise a real sync without a bank, point `simplefin.access_url` at
a local mock server that returns a [SimpleFIN `/accounts`](https://www.simplefin.org/protocol.html)
JSON payload.

## Running locally

### SQLite (default — zero dependencies)

```sh
KASAS_DATABASE_PATH=./dev.db \
KASAS_SECRETS_FILE=./dev-secrets.json \
KASAS_LOG_FORMAT=text \
KASAS_SYNC_RUN_ON_START=false \
go run ./cmd/kasas serve
# in another shell:
curl localhost:8080/api/v1/accounts
```

### Postgres

Start a throwaway Postgres (or use the Compose service: `docker compose --profile postgres up -d`):

```sh
docker run -d --name kasas-dev-pg \
  -e POSTGRES_USER=kasas -e POSTGRES_PASSWORD=kasas -e POSTGRES_DB=kasas \
  -p 5432:5432 postgres:16-alpine
```

Then point kasas at it (migrations run automatically on start):

```sh
KASAS_DATABASE_DRIVER=postgres \
KASAS_DATABASE_DSN="postgres://kasas:kasas@localhost:5432/kasas?sslmode=disable" \
KASAS_SECRETS_FILE=./dev-secrets.json \
go run ./cmd/kasas serve
```

Other subcommands: `migrate` (apply migrations and exit), `sync` (one sync),
`mcp` (MCP over stdio), `healthcheck`.

### Seeding demo data

Without a real SimpleFIN bridge, populate the configured database with realistic
demo accounts and transactions so the dashboard and API have something to show:

```sh
make seed                # upsert demo data into the DB named by config.toml (re-runnable)
make seed-reset          # wipe ./data and seed from a clean slate (local SQLite)
make seed SEED_EXTRA=60  # add 60 extra synthetic transactions (exercise "Load more")
```

The seeder (`scripts/seed`) is a small Go program: it reads `config.toml`,
applies migrations, and upserts fixtures (transaction dates are relative to now,
so the data always looks current). It works against SQLite or Postgres.

## Database changes (migrations + sqlc)

Schema lives in `migrations/sqlite/` and `migrations/postgres/` (goose); queries
live in `queries/` (shared by both dialects). After editing either, regenerate
the type-safe Go:

```sh
make generate   # runs sqlc for both engines
```

Keep the two migration dialects equivalent, and write queries with
`sqlc.arg(...)` (never bare `?`) so they generate for both engines. Design
Postgres columns so the generated structs match SQLite's (e.g. `bigint` for
integers) — the Postgres adapter in `internal/db/postgres_store.go` relies on it
and will fail to compile if they drift.

## Dashboard (WebAssembly)

The read-only web dashboard (`internal/dashboard`) is a [go-app](https://go-app.dev)
PWA. Its UI is compiled to WebAssembly from `cmd/kasas-wasm` and embedded
(gzipped) into the server binary:

```sh
make wasm     # GOOS=js GOARCH=wasm build -> internal/dashboard/web/app.wasm.gz
make build    # builds the WASM first, then the server (which embeds it)
make run      # likewise, then runs the server
```

The built `app.wasm.gz` is git-ignored. `go build ./...` and the tests still
work without it (the embed tolerates an absent WASM), but the dashboard won't
load until you `make wasm`. Edit the components in `internal/dashboard/*.go`,
re-run `make wasm`, and refresh.

## Testing

```sh
make test        # full suite (SQLite); Postgres test is skipped
make test-race   # with the race detector
make cover       # HTML coverage report
```

The data layer is tested against **real SQLite** (no mocks) via
`internal/testutil`. The **Postgres** integration test
(`internal/db/pgstore_test.go`) is gated: it runs only when
`KASAS_TEST_POSTGRES_DSN` is set, and is skipped otherwise.

```sh
docker run -d --name kasas-test-pg \
  -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=kasas -p 5432:5432 postgres:16-alpine
KASAS_TEST_POSTGRES_DSN="postgres://postgres:postgres@localhost:5432/kasas?sslmode=disable" \
  go test ./internal/db/
```

CI runs this automatically against a Postgres service container on every PR.

## Linting & formatting

```sh
make lint        # golangci-lint (config in .golangci.yml)
gofmt -w .       # or rely on goimports via golangci-lint formatters
```

## Commit messages & releases

This repo uses **[Conventional Commits](https://www.conventionalcommits.org/)**.
Versioning and `CHANGELOG.md` are automated by
[release-please](https://github.com/googleapis/release-please): it opens a
"release" PR that bumps the version and changelog from your commit history;
merging that PR tags a release, which publishes binaries and a multi-arch image
to GHCR.

Use these prefixes (the PR **title** is what counts when squash-merging):

| Prefix | Effect | Example |
| --- | --- | --- |
| `feat:` | minor bump, "Features" | `feat: add CSV export endpoint` |
| `fix:` | patch bump, "Bug Fixes" | `fix: handle empty SimpleFIN org id` |
| `perf:` / `refactor:` / `docs:` | changelog entry | `docs: document Postgres setup` |
| `test:` / `ci:` / `chore:` | no release | `chore: bump deps` |
| `feat!:` / `fix!:` (or `BREAKING CHANGE:` footer) | breaking change | `feat!: rename sync endpoint` |

Pre-1.0, breaking changes bump the minor version. To cut the first release,
land a `feat:`/`fix:` commit (or add `Release-As: 0.1.0` to a commit footer).

## What CI checks on every PR

The **CI** workflow must pass before merge:

- **Format** — `gofmt` (run `make fmt` to fix)
- **Lint** — `golangci-lint`
- **Test** — `go test -race` against SQLite *and* a Postgres service container
- **Build** — static binary builds + the Docker image builds

## Pre-PR checklist

- [ ] `make lint` and `make test` (or `make test-race`) pass
- [ ] `make generate` re-run if you touched `queries/` or `migrations/`
- [ ] Docs updated if behaviour changed
- [ ] PR title is a Conventional Commit
