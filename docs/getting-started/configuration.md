# Configuration

kasas is configured by a **TOML file** (`-config path`) and/or **environment
variables**. Every key has a sensible default, so you can run with no config at
all, an env-only setup, a file, or a mix.

## Precedence & env mapping

Settings resolve in this order (later wins):

```text
built-in defaults  →  TOML file  →  environment variables
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

## `[server]`

| Key | Default | Description |
| --- | --- | --- |
| `addr` | `:8080` | HTTP listen address for the API, MCP, and dashboard. |

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
| `allow_apply` | `true` | Let the dashboard/API trigger an in-place self-update. |
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
