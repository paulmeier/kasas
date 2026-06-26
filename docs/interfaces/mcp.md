# MCP Server

kasas embeds a [Model Context Protocol](https://modelcontextprotocol.io/) server,
so an AI agent can drive your ledger directly — list and search transactions,
manage labels and rules, read the event stream and history, and administer
webhooks, API keys, and plugins — as first-class tools. It's the same core logic
as the [REST API](rest-api.md), exposed as MCP tools.

Source: [`internal/api/mcp.go`](https://github.com/paulmeier/kasas/blob/main/internal/api/mcp.go),
built on [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk).
Enabled by `mcp.enabled` (default on).

## Transports

| Transport | Endpoint | Auth |
| --- | --- | --- |
| **Streamable HTTP** | `/mcp` | The [dashboard token](authentication.md) (not API keys). |
| **stdio** | `kasas mcp` subcommand | None — it's a local subprocess. |

For desktop MCP clients that launch a subprocess, run it over stdio:

```sh
kasas -config config.toml mcp
```

For HTTP clients, point them at `/mcp` and send the dashboard token as a bearer
credential, exactly as for REST.

## Connecting a client

Any MCP client can drive kasas. Pick the transport that matches how the client
runs kasas:

- **stdio** — the client launches `kasas mcp` as a subprocess on the same
  machine. No token: the subprocess reads your config directly. Use the
  **absolute path** to the `kasas` binary (desktop clients run with a minimal
  `PATH` and won't find it on their own), and point `-config` at your config file.
- **Streamable HTTP** — the client connects to an already-running kasas at `/mcp`
  and authenticates with the [dashboard token](authentication.md). Use this for a
  remote or always-on instance (e.g. on a home server). The address below assumes
  the default `server.addr` of `:8080`; swap in your real host and port.

### Claude Desktop

Open **Settings → Developer → Edit Config** (this creates/opens
`claude_desktop_config.json` — on macOS at
`~/Library/Application Support/Claude/`, on Windows at `%APPDATA%\Claude\`), add
kasas under `mcpServers`, then restart Claude Desktop. The tools appear under the
🔨 icon.

=== "stdio (local)"

    ```json
    {
      "mcpServers": {
        "kasas": {
          "command": "/usr/local/bin/kasas",
          "args": ["-config", "/path/to/config.toml", "mcp"]
        }
      }
    }
    ```

=== "HTTP (remote)"

    Claude Desktop's config launches local subprocesses, so reach a remote kasas
    by adding it under **Settings → Connectors → Add custom connector** with the
    `/mcp` URL, or bridge stdio→HTTP with
    [`mcp-remote`](https://github.com/geelen/mcp-remote):

    ```json
    {
      "mcpServers": {
        "kasas": {
          "command": "npx",
          "args": [
            "-y", "mcp-remote",
            "http://your-host:8080/mcp",
            "--header", "Authorization: Bearer YOUR_DASHBOARD_TOKEN"
          ]
        }
      }
    }
    ```

### Hermes Agent

Hermes ([Nous Research](https://hermes-agent.nousresearch.com/docs/user-guide/features/mcp))
reads MCP servers from `mcp_servers` in `~/.hermes/config.yaml` (or run
`hermes mcp` for the interactive picker):

=== "stdio (local)"

    ```yaml
    mcp_servers:
      kasas:
        command: "/usr/local/bin/kasas"
        args: ["-config", "/path/to/config.toml", "mcp"]
    ```

=== "HTTP (remote)"

    ```yaml
    mcp_servers:
      kasas:
        url: "http://your-host:8080/mcp"
        headers:
          Authorization: "Bearer YOUR_DASHBOARD_TOKEN"
    ```

### OpenClaw

OpenClaw ([docs](https://docs.openclaw.ai/cli/mcp)) keeps servers under
`mcp.servers` in `~/.openclaw/openclaw.json`. Edit the file directly, or use the
CLI (`openclaw mcp add …`, then `openclaw mcp doctor --probe` to verify):

=== "stdio (local)"

    ```json
    {
      "mcp": {
        "servers": {
          "kasas": {
            "command": "/usr/local/bin/kasas",
            "args": ["-config", "/path/to/config.toml", "mcp"]
          }
        }
      }
    }
    ```

=== "HTTP (remote)"

    ```json
    {
      "mcp": {
        "servers": {
          "kasas": {
            "url": "http://your-host:8080/mcp",
            "transport": "streamable-http",
            "headers": { "Authorization": "Bearer YOUR_DASHBOARD_TOKEN" }
          }
        }
      }
    }
    ```

## Tools

Tools for every read/write/admin operation (the plugin tools only when
[plugins](../features/plugins.md) are enabled, and the two marketplace tools only
when a registry is configured). Each maps onto the same handler logic and DTO shapes
as the corresponding REST route, so responses are identical.

=== "Accounts & transactions"

    | Tool | Description |
    | --- | --- |
    | `list_accounts` | All accounts with balances. |
    | `get_account` | One account by id. |
    | `create_account` · `update_account` · `delete_account` | [Manual account](../features/manual-entry.md) CRUD (synced accounts are read-only). |
    | `list_transactions` | Filter by `account_id`, date range, and `label_key`/`label_value`. |
    | `search_transactions` | The [query language](../features/search.md), including `ext:`. |
    | `create_transaction` · `update_transaction` · `delete_transaction` | [Manual transaction](../features/manual-entry.md) CRUD (synced transactions are read-only). |
    | `list_organizations` | Financial institutions owning the accounts. |

=== "Metadata"

    | Tool | Description |
    | --- | --- |
    | `list_labels` | The [label](../features/labels.md) vocabulary with counts. |
    | `set_transaction_extensions` | Replace a transaction's [extensions](../features/schema-extensions.md). |
    | `list_extensions` | The extension vocabulary with counts. |
    | `get_transaction_relationships` | One transaction's [relationships](../features/transaction-relationships.md) (outbound + inbound edges). |
    | `create_transaction_relationship` · `delete_transaction_relationship` | Assert / remove a directed edge to another transaction. |
    | `list_relationship_kinds` | The relationship-kind vocabulary with counts. |

=== "Rules"

    | Tool | Description |
    | --- | --- |
    | `list_rules` · `create_rule` · `update_rule` · `delete_rule` | [Rule](../features/rules.md) CRUD. |
    | `run_rules` | Run one rule (pass `id`) or all enabled rules (omit it). |

=== "Events & history"

    | Tool | Description |
    | --- | --- |
    | `list_events` | Read the [event stream](../features/event-stream.md): cursor `after`, filter by `type`/`entity_type`/`entity_id`. |
    | `get_transaction_history` | One transaction's [version history](../features/transaction-history.md) with diffs. |
    | `get_transaction_provenance` | One transaction's [provenance](../features/transaction-provenance.md): source, identity, and its transformation lineage. |

=== "Sync & sources"

    | Tool | Description |
    | --- | --- |
    | `sync_status` | The most recent [sync](../features/sync.md) status. |
    | `trigger_sync` | Run a sync now (every source); returns counts. |
    | `sync_source` | Run a sync of one source by type. |
    | `list_sources` | Every ingestion source — active and inactive — with readiness, credential shape, and its editable config. |

=== "Settings"

    | Tool | Description |
    | --- | --- |
    | `list_settings` | Every [editable setting](../getting-started/configuration.md#settings-from-the-dashboard) with its value, override state, and restart-pending flag. Secrets are never returned. |
    | `set_setting` | Permanently set one setting by key; validated, persisted, applies at the next restart. |
    | `reset_setting` | Remove a setting's stored override (back to the config file/env value). |
    | `restart_kasas` | Restart kasas in place so pending setting changes apply (the connection drops briefly). |

=== "Admin"

    | Tool | Description |
    | --- | --- |
    | `list_api_keys` · `create_api_key` · `revoke_api_key` | [API key](authentication.md#api-keys) management. |
    | `list_webhooks` · `create_webhook` · `update_webhook` · `delete_webhook` · `test_webhook` | [Webhook](../features/webhooks.md) management. |
    | `list_plugins` · `get_plugin` · `enable_plugin` · `disable_plugin` · `reload_plugin` · `uninstall_plugin` | [Plugin](../features/plugins.md) lifecycle (when enabled; uninstall runs the cleanup hook). |
    | `browse_plugin_registry` · `install_plugin` | [Community marketplace](../features/plugins.md#the-community-marketplace) (when a registry is configured). |

## One core, two surfaces

The MCP tools are thin wrappers over the very same functions the REST handlers
call — `search.Parse` + the matcher for `search_transactions`, the
[emitter](../features/event-stream.md) for any write, the shared DTO converters
for output. There is no second implementation of search, rules, or labels to drift
out of sync. Anything you can do over REST, an agent can do over MCP, with
identical semantics and identical event/history side effects.

!!! info "Why extension values are `any`, not raw JSON"
    The MCP SDK's schema generator rejects `json.RawMessage`, so tool inputs and
    outputs that carry free-form data (event `data`, extension values, history
    snapshots) use `any` / `map[string]any` rather than raw bytes. The values are
    the same JSON; only the Go type at the boundary differs from the internal
    storage type.
