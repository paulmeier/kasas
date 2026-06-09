# Development

This page orients you in the codebase and the build. The canonical, step-by-step
contributor guide — local Postgres, the gated integration test, the seeder, the
release flow — lives in
[**CONTRIBUTING.md**](https://github.com/paulmeier/kasas/blob/main/CONTRIBUTING.md).

## Prerequisites

- **Go 1.25+** — the only hard requirement. Pure-Go SQLite and Postgres drivers,
  **no CGO**.
- **Docker** (optional) — for a local Postgres or building the image.
- Installed on demand: [`sqlc`](https://sqlc.dev) (regenerates DB code) and
  [`golangci-lint`](https://golangci-lint.run) v2.

```sh
git clone git@github.com:paulmeier/kasas.git && cd kasas
make build && ./bin/kasas version
make help          # list every target
```

## Common tasks

```sh
make wasm       # build + gzip the dashboard WebAssembly (embedded by the server)
make build      # static binary -> bin/kasas (builds the WASM first)
make run        # frees the port, then runs against ./config.toml
make generate   # regenerate sqlc code after editing queries/ or migrations/
make test       # run the suite (SQLite); the Postgres test is skipped unless DSN set
make test-race  # with the race detector
make lint       # golangci-lint
make seed       # populate the DB with demo data (no SimpleFIN bridge needed)
```

## Project layout

```text
cmd/kasas/           main entrypoint and subcommands
cmd/kasas-wasm/      dashboard WebAssembly client (GOOS=js GOARCH=wasm)
internal/api/        chi routes, REST handlers, MCP server, DTOs
internal/dashboard/  go-app dashboard UI + handler (served at /)
internal/config/     viper configuration
internal/vault/      secret store (Vault KV v2, with local-file fallback)
internal/auth/       dashboard-token guard
internal/apikeys/    scoped API keys
internal/source/     ingestion SDK: ImportBatch, capability interfaces, registry
internal/sources/    built-in sources (simplefin today) — normalize to ImportBatch
internal/poller/     ingestion engine: gocron scheduler + transactional persist
internal/db/         SQLite sqlc output + Store interface + Postgres adapter
internal/db/pg/      Postgres sqlc output
internal/events/     event bus, transactional emitter, diffs
internal/search/     the search query language (pure Go; also compiled to WASM)
internal/rules/      rules engine
internal/labels/     label normalization
internal/extensions/ schema-extension helpers
internal/webhooks/   webhook dispatcher
internal/plugins/    plugin manager, host facade, Lua + JS/TS runtimes
internal/selfupdate/ release check + in-place apply
internal/testutil/   shared test database + fixtures
migrations/          embedded goose migrations (per-dialect)
queries/             sqlc query definitions (shared + per-dialect)
docs/                this documentation site (MkDocs)
```

## Database changes

Schema lives in `migrations/{sqlite,postgres}/` (goose); queries in `queries/`
(shared, plus per-dialect dirs for [JSON label filtering](../architecture/data-model.md#multi-dialect-storage)).
After editing either, run `make generate`. Keep the two migration dialects
equivalent and design Postgres columns so the generated structs match SQLite's
(`bigint` for integers) — the `internal/db/postgres_store.go` adapter relies on it.

## Testing

The data layer runs against **real SQLite** (no mocks) via `internal/testutil`;
the **Postgres** integration test is gated behind `KASAS_TEST_POSTGRES_DSN` and
skipped otherwise. CI runs `go test -race` against both SQLite and a Postgres
service container, plus gofmt, lint, and a build/image stage, on every PR.

## Commits & releases

The repo uses [Conventional Commits](https://www.conventionalcommits.org/) with
[release-please](https://github.com/googleapis/release-please): your commit
history drives the version bump and `CHANGELOG.md`; merging the release PR tags a
release and publishes binaries plus a multi-arch image to GHCR. The PR **title** is
what counts on squash-merge.

## Working on these docs

The documentation site is [MkDocs Material](https://squidfunk.github.io/mkdocs-material/).
Source is the Markdown under `docs/` plus `mkdocs.yml` at the repo root; diagrams
are [Mermaid](https://mermaid.js.org/) fenced code blocks.

```sh
python3 -m venv .venv && . .venv/bin/activate
pip install "mkdocs-material==9.7.6"

mkdocs serve            # live preview at http://127.0.0.1:8000
mkdocs build --strict   # what CI gates on — fails on broken links / nav
```

On every push to `main` that touches `docs/`, `mkdocs.yml`, or the workflow, the
[`Docs` workflow](https://github.com/paulmeier/kasas/blob/main/.github/workflows/docs.yml)
builds the site and publishes it (via `mkdocs gh-deploy`) to the `docs` branch,
which GitHub Pages serves. You don't edit the `docs` branch by hand — it's
generated. Use a `docs:` Conventional Commit for documentation changes.
