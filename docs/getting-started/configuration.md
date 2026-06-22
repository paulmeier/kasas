# Configuration

kasas is configured by a **TOML file** (`-config path`) and/or **environment
variables**, and most of it is also editable at runtime from the
**[dashboard Settings page](../interfaces/dashboard.md)** (or
`PUT /api/v1/settings/{key}`, or the `set_setting` MCP tool). Every key has a
sensible default, so you can run with no config at all, an env-only setup, a
file, or a mix.

## Precedence & env mapping

Settings resolve in this order (later wins):

```text
built-in defaults  →  TOML file  →  environment variables  →  dashboard-stored settings
```

To map any key to an environment variable: **uppercase it, prefix `KASAS_`, and
join sections with underscores.**

| TOML key | Environment variable |
| --- | --- |
| `server.addr` | `KASAS_SERVER_ADDR` |
| `database.driver` | `KASAS_DATABASE_DRIVER` |
| `simplefin.setup_token` | `KASAS_SIMPLEFIN_SETUP_TOKEN` |

The full annotated template is
[`config.example.toml`](https://github.com/paulmeier/kasas/blob/main/config.example.toml).

### Where the config file lives (Docker / Unraid)

The Docker image sets `KASAS_CONFIG=/data/config.toml` and **seeds that file with
the annotated example on first run** if it is missing — so there is always a real,
editable config file inside the persisted data volume. On Unraid that is
`/mnt/user/appdata/kasas/config.toml`; with the bundled `docker-compose.yml` it is
`./data/config.toml`. Edit it and restart to apply.

On Unraid the container template also sets most keys as `KASAS_*` environment
variables, and **environment variables win over the file** (see the precedence
above), so the seeded `config.toml` mainly serves as an in-place reference there —
change those values from the Unraid template, the dashboard Settings page, or by
clearing the corresponding env var so the file (or a dashboard setting) takes over.

## Settings from the dashboard

A setting changed from the dashboard, the REST API, or MCP is **permanent**: it
is persisted (non-secret values in the database's `settings` table, secrets like
a Plaid app secret in the [secret store](#secrets)) and re-applied **over** the
config file and environment on every boot, until you reset it. Each editable
setting shows where its value comes from (an `overridden` chip) and offers a
one-click reset back to your config.

Because subsystems and sources are constructed at startup, a changed setting
takes effect at the **next restart** — the dashboard shows a *restart pending*
banner with a one-click in-place restart (`POST /api/v1/system/restart`), the
same re-exec mechanism a dashboard-triggered self-update uses.

Three things stay file/env-only, because kasas needs them *before* it can read
its stored settings: `[server].addr`, the whole `[database]` section, and the
secret-store choice (`[secrets]` / `[vault]`). Source **credentials** (tokens,
watched addresses) are not settings either — they apply live, no restart, via
the [Sources page](../interfaces/dashboard.md) or the credential endpoints.

Validation runs before anything is stored: a value that doesn't parse, or that
would make the combined configuration invalid, is rejected with a `400`. If a
stored value ever turns stale (e.g. after a downgrade), boot skips it with a
warning rather than refusing to start.

## `[server]`

| Key | Default | Description |
| --- | --- | --- |
| `addr` | `:8080` | HTTP listen address (`host:port`) for the API, MCP, and dashboard. |
| `allow_unauthenticated` | `false` | Allow serving on a non-loopback address with **no** dashboard token. Otherwise kasas refuses to start in that configuration (it would expose your ledger). Set a token, bind `addr` to `127.0.0.1`, or set this `true` to run open on purpose. See [Authentication](../interfaces/authentication.md#unauthenticated-by-default). |

## `[log]`

| Key | Default | Description |
| --- | --- | --- |
| `level` | `info` | `debug` · `info` · `warn` · `error`. |
| `format` | `json` | `json` for log pipelines, or `text` for humans. |

## `[database]`

| Key | Default | Description |
| --- | --- | --- |
| `driver` | `sqlite` | Backend: `sqlite` or `postgres`. |
| `path` | `/data/kasas.db` | SQLite file path (driver=sqlite); parent dir is created. |
| `dsn` | — | Postgres connection string (driver=postgres). |

See [Data Model → multi-dialect storage](../architecture/data-model.md#multi-dialect-storage)
and [Deployment → Postgres](deployment.md#postgres).

## `[simplefin]`

Credentials for the built-in **[SimpleFIN](../architecture/ingestion.md) source**
— the first source kasas ships with. Provide **one** of these on first run (or set
the credential later from the dashboard / API — no restart needed). Each source
carries its own config section. See the [sync pipeline](../features/sync.md).

| Key | Default | Description |
| --- | --- | --- |
| `setup_token` | — | One-time base64 setup token; claimed for an access URL on first sync, then consumed. |
| `access_url` | — | A previously claimed access URL with embedded credentials. |

## `[csv]`

The **[CSV file-import](../features/csv-import.md) source**: import transactions
from CSV files in local folders or Google Drive. Configure one `[[csv.folders]]`
entry per account; the source is started only when at least one folder is set. The
Google Drive keys are needed only for `gdrive` folders. Full details — column
mapping and the Google OAuth setup — are on the
[CSV File Import](../features/csv-import.md) page.

| Key | Default | Description |
| --- | --- | --- |
| `gdrive_client_id` | — | OAuth client id for the Google Drive backend (also `KASAS_CSV_GDRIVE_CLIENT_ID`). |
| `gdrive_client_secret` | — | OAuth client secret (also `KASAS_CSV_GDRIVE_CLIENT_SECRET`). |
| `gdrive_redirect_url` | — | The registered OAuth callback, `https://<host>/api/v1/sources/csv/oauth/callback`. |

Each `[[csv.folders]]` entry: `name`, `backend` (`local` \| `gdrive`), `path`
(local) or `folder_id` (Drive), `account`, optional `org`/`currency`, and an
optional `[csv.folders.mapping]` (column mapping; omitted columns are auto-detected).

## `[sync]`

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Run the background sync scheduler. |
| `interval` | `6h` | Poll interval (any Go duration: `30m`, `1h`, `12h`). |
| `lookback_days` | `90` | How far back to fetch transactions; `0` = all available. |
| `run_on_start` | `true` | Run one sync immediately at startup. |

## `[secrets]`

| Key | Default | Description |
| --- | --- | --- |
| `file` | `/data/secrets.json` | Local JSON file (written `0600`) for the SimpleFIN access URL + dashboard token, when Vault is disabled. |

This is the [secret store](../interfaces/authentication.md) fallback; see
[`[vault]`](#vault) for the alternative.

## `[vault]`

Optionally store the access URL + dashboard token in HashiCorp Vault (KV v2)
instead of the local file. See [Deployment → Vault](deployment.md#vault).

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `false` | Use Vault for secrets. |
| `address` | `http://127.0.0.1:8200` | Vault address (falls back to `VAULT_ADDR`). |
| `token` | — | Vault token (falls back to `VAULT_TOKEN`). |
| `mount` | `secret` | KV v2 mount path. |
| `path` | `kasas` | Secret path within the mount. |
| `access_url_key` | `simplefin_access_url` | Key name for the access URL. |

## `[mcp]`

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Mount the [MCP server](../interfaces/mcp.md) at `/mcp`. |

## `[dashboard]`

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Serve the [web dashboard](../interfaces/dashboard.md) at `/`. |
| `token` | — | [Auth token](../interfaces/authentication.md) for the API, dashboard, and MCP. Empty = unauthenticated (with a startup warning). |

## `[update]`

The [self-update](../reference/cli.md#self-update) check and in-place apply.

| Key | Default | Description |
| --- | --- | --- |
| `check` | `true` | Daily check for a newer release (logs + dashboard banner). Never modifies the binary. |
| `allow_apply` | `false` | Let the dashboard/API trigger an in-place self-update (replaces the running binary). Off by default; turn on to opt in. Even when on, the apply requires the dashboard token. |
| `repository` | `paulmeier/kasas` | GitHub repo to check for releases. |

## `[events]`

The [event stream](../features/event-stream.md) and
[transaction history](../features/transaction-history.md).

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Record the event stream and history. Gates webhooks, plugins, and history. |
| `retention_days` | `0` | Prune events older than N days; `0` = keep forever (fully replayable). |
| `history_retention_days` | `0` | Prune history snapshots older than N days; `0` = keep forever (independent of `retention_days`). |

## `[webhooks]`

[Webhook](../features/webhooks.md) delivery (effective only when `events.enabled`).

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `true` | Run the webhook dispatcher. |
| `timeout` | `10s` | Per-attempt HTTP timeout. |
| `max_attempts` | `5` | Attempts per delivery (exponential backoff). |

## `[plugins]`

The [plugin runtime](../features/plugins.md) (effective only when `events.enabled`).
**Disabled by default** — a plugin is third-party code, and even when the subsystem
is on, each plugin must be enabled individually.

| Key | Default | Description |
| --- | --- | --- |
| `enabled` | `false` | Make plugins discoverable. |
| `dir` | `/data/plugins` | Directory scanned for plugin subdirectories. |
| `hook_timeout` | `5s` | Wall-clock limit for a single hook invocation. |
| `queue_size` | `256` | Per-plugin job-queue depth (drops rather than stalls the bus). |
