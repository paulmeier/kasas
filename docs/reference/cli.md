# CLI & Subcommands

kasas is a single binary with a handful of subcommands. The default is `serve`.

```text
kasas [-config path] [command]
```

- `-config path` — path to a TOML [config file](../getting-started/configuration.md).
  Also read from `KASAS_CONFIG`. Configuration can come entirely from environment
  variables, so the flag is optional.
- Every subcommand loads configuration and sets up logging first.

## serve

```sh
kasas -config config.toml serve     # (or just: kasas -config config.toml)
```

The default. Runs the HTTP server (REST API, MCP, dashboard) **and** the
background [sync](../features/sync.md) scheduler, plus the retention pruners, the
[webhook dispatcher](../features/webhooks.md), the [plugin manager](../features/plugins.md),
and the daily [update check](#self-update) — each only if enabled. Runs until
`SIGINT`/`SIGTERM`, then shuts down gracefully (see
[lifecycle](../architecture/overview.md#startup-lifecycle)).

## sync

```sh
KASAS_SIMPLEFIN_SETUP_TOKEN="..." kasas -config config.toml sync
```

Runs exactly **one** [sync](../features/sync.md) and exits — useful for a cron job
or a one-shot backfill. Applies the same insert/refresh/rules logic as a scheduled
run.

## migrate

```sh
kasas -config config.toml migrate
```

Applies the embedded [goose migrations](../architecture/data-model.md#migrations)
for the active dialect and exits. Migrations also run automatically on `serve`, so
this is for running them explicitly.

## migrate-postgres

```sh
kasas -config config.toml migrate-postgres \
  "postgres://user:pass@host:5432/kasas?sslmode=disable"
```

Copies the current **SQLite** ledger into a **Postgres** database and exits — the
one-time move from the default embedded backend to Postgres. It applies the kasas
schema to the target, then copies every table (accounts, transactions, rules,
events, history, settings, …) **with ids preserved**, and prints a per-table row
count. See the [migration guide](../getting-started/migrate-to-postgres.md) for the
full walkthrough (the same thing is available from the dashboard's **Settings →
Migrate to Postgres** panel).

The target DSN may be passed as the first argument or with `-dsn`. Requirements:

- the active `database.driver` must be `sqlite` (there must be a ledger to copy from);
- the target Postgres database must be **empty** (kasas refuses a non-empty one
  rather than risk a half-merged ledger);
- the source SQLite database is only **read** — it is left untouched, so you can
  verify Postgres before switching.

It does **not** change your configuration: after it succeeds, set
`database.driver=postgres` and `database.dsn` (or `KASAS_DATABASE_DRIVER` /
`KASAS_DATABASE_DSN`) and restart kasas to run on Postgres.

## mcp

```sh
kasas -config config.toml mcp
```

Serves the [MCP server](../interfaces/mcp.md) over **stdio**, for desktop MCP
clients that launch a subprocess. (Over HTTP, MCP is mounted at `/mcp` by `serve`.)

## healthcheck

```sh
kasas healthcheck
```

Probes the local `/healthz` endpoint and exits non-zero on failure. It backs the
container `HEALTHCHECK`, which can't shell out in a `scratch` image.

## self-update

```sh
kasas self-update          # download, verify, and replace the running binary
kasas self-update -check   # report whether a newer release exists; install nothing
```

Fetches the latest GitHub release, downloads the asset matching your OS/arch,
verifies it against the published `.sha256` (refusing to proceed on a mismatch or
missing checksum), and atomically replaces the binary. You need write access to its
directory; restart afterwards to run the new version. A `dev`/source build that
isn't a released version is reported rather than overwritten.

See [Deployment → Updating](../getting-started/deployment.md#updating) for the
Docker, dashboard, and config (`update.check`, `update.allow_apply`) angles.

## version

```sh
kasas version     # -> kasas 2.9.0
```

Prints the build version and exits. The version is stamped at build time via
`-ldflags "-X main.version=…"`.
