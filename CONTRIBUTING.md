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

## Adding an ingestion source

kasas is **source-agnostic**. A *source* talks to one provider and normalizes its
data into a neutral `source.ImportBatch`; the generic ingestion engine
(`internal/poller`) owns scheduling, the transactional persist, dedup, events,
rules, and history. A source never touches the database, so the engine's
guarantees hold for every source for free. SimpleFIN (`internal/sources/simplefin`)
is the reference implementation — copy its shape. Background:
[Ingestion & Sources](https://paulmeier.github.io/kasas/architecture/ingestion/).

**1. Implement the source.** Add `internal/sources/<name>/`. Implement
`source.Source` (its `Descriptor()`) plus the capability interface for your
archetype — `source.Puller` for a scheduled pull, the common case:

```go
package mysource

const SourceType = "mysource"

func (s *Source) Descriptor() source.Descriptor {
    return source.Descriptor{Type: SourceType, Archetype: source.ArchetypePull, Title: "My Source"}
}

// Fetch normalizes the provider's data into the engine's neutral batch: map each
// row to source.ImportTxn (universal fields only — provider-specific richness goes
// in ImportTxn.Extensions) and stamp batch.Source = SourceType.
func (s *Source) Fetch(ctx context.Context, since time.Time, cursor string) (*source.ImportBatch, error) { … }

var _ source.Puller = (*Source)(nil) // + source.Credentialed if it has a runtime credential
```

**2. Register it.** Self-register in an `init()` so importing the package wires it
in:

```go
func init() {
    source.Register(descriptor(), func(env source.Env) (source.Source, error) {
        return New(/* read env.Opt("…") and env.Secrets */), nil
    })
}
```

**3. Wire it in.** Import the package from `cmd/kasas` and construct it in
`buildEngine`: `source.New(<type>, env)`, wrap it in a `poller.New`, and append it
to the slice passed to `poller.NewEngine`. The engine runs one poller per source
and exposes them all across REST/MCP/dashboard automatically. **No engine changes
are needed.**

**Build:** `make build` — a source compiles into the binary like any package.

**Test:** unit-test the mapping and fetch in your package, modeled on
[`internal/sources/simplefin/simplefin_test.go`](https://github.com/paulmeier/kasas/blob/main/internal/sources/simplefin/simplefin_test.go)
(`TestToImportBatch`, `TestFetch`, `TestRegisteredAndConstructable`). You do **not**
re-test persistence, dedup, or events — those are the engine's, covered by
`internal/poller`'s tests. Exercise just your source:

```sh
go test ./internal/sources/<name>/
```

> **Two archetypes ship:** `pull` (SimpleFIN and
> [Teller](https://paulmeier.github.io/kasas/features/teller/)) and `file`
> ([CSV import](https://paulmeier.github.io/kasas/features/csv-import/),
> `internal/sources/csv`). A `file` source doesn't need a separate upload
> interface — it implements `source.Puller` and **scans its folder on the sync
> schedule**, abstracting storage behind a small `FileStore` (`List`/`Open`) with
> `local` and `gdrive` backends, and synthesizing a content-hash `ExternalID` per
> row so re-imports dedup. `Credentialed` (pasted secret) and `OAuthCredentialed`
> (browser OAuth, e.g. Google Drive) are optional add-on capabilities. The
> `webhook` and `enrichment` archetypes are reserved in `internal/source`; their
> capability interfaces land there as each is built, and because capabilities are
> independent, adding one never disturbs existing sources.

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
make run      # likewise, frees the port, then runs the server
```

The built `app.wasm.gz` is git-ignored. `go build ./...` and the tests still
work without it (the embed tolerates an absent WASM), but the dashboard won't
load until you `make wasm`. Edit the components in `internal/dashboard/*.go`,
re-run `make wasm`, and refresh.

`make run` frees the configured port before starting (via `make kill-port`),
so it won't fail with "address already in use" when a previous server is still
bound. The port is read from `$KASAS_SERVER_ADDR` or `[server].addr` in
`config.toml` (default 8080); override it with `make run PORT=9000`.

## Building & testing each area

Everything compiles into the one binary (`make build`) and is tested with
`go test`. This maps each feature to where it lives and how to exercise *just* it —
a fast inner loop while you work on one thing; `make test` still runs the whole
suite. Areas with their own workflow (database, dashboard) link to the sections
above.

| Area | Package(s) | Build | Test just it |
| --- | --- | --- | --- |
| **Ingestion sources** | `internal/source`, `internal/sources/*` | `make build` | `go test ./internal/sources/...` — and [Adding a source](#adding-an-ingestion-source) |
| **Ingestion engine** (sync) | `internal/poller` | `make build` | `go test ./internal/poller/` |
| **Search · rules · labels · extensions** | `internal/{search,rules,labels,extensions}` | `go build ./...` | `go test ./internal/search/ ./internal/rules/ ./internal/labels/ ./internal/extensions/` (no DB) |
| **Events & history** | `internal/events` | `make build` | `go test ./internal/events/` |
| **Provenance** | `internal/provenance` | `make build` | `go test ./internal/provenance/` |
| **Webhooks** | `internal/webhooks` | `make build` | `go test ./internal/webhooks/` |
| **Plugins** (Lua) | `internal/plugins` | `make build` | `go test ./internal/plugins/` |
| **REST + MCP** | `internal/api` | `make build` | `go test ./internal/api/` |
| **Dashboard** (WASM) | `internal/dashboard`, `cmd/kasas-wasm` | `make wasm` (see [Dashboard](#dashboard-webassembly)) | `go test ./internal/dashboard/` |
| **Database & migrations** | `internal/db`, `migrations/`, `queries/` | `make generate` then `make build` | `go test ./internal/db/` (Postgres needs a DSN — see [Testing](#testing)) |

Anything that changes behaviour should land with a test, and run clean under the
race detector the way CI does: `go test -race ./internal/<pkg>/`.

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
