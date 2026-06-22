# Quick Start

Get kasas running, connected to your accounts, and syncing in about a minute.

kasas ingests through [pluggable sources](../architecture/ingestion.md);
**[SimpleFIN](https://www.simplefin.org/)** is the first and the one this guide
uses.

## Prerequisites

- A **SimpleFIN setup token** — a base64 string from your
  [SimpleFIN bridge](https://www.simplefin.org/). It's claimed once on first sync.
- **Docker** (for the container route) or **Go 1.25+** (to build locally).

## Docker

Prebuilt multi-arch (amd64/arm64) images are published to GHCR on every release.

```sh
docker pull ghcr.io/paulmeier/kasas:latest
```

1. Set your setup token and start the service (the bundled Compose file builds
   locally; swap in the GHCR `image:` to use the published one):

    ```sh
    export KASAS_SIMPLEFIN_SETUP_TOKEN="<your base64 setup token>"
    docker compose up -d --build
    ```

2. The token is claimed on first sync and the resulting access URL is persisted to
   `./data/secrets.json`, so the token is only ever used once. Check it worked:

    ```sh
    curl localhost:8080/api/v1/sync      # latest sync status
    curl localhost:8080/api/v1/accounts  # synced accounts
    ```

3. Open the [dashboard](../interfaces/dashboard.md) at
   [http://localhost:8080](http://localhost:8080).

!!! warning "Volume permissions"
    The container runs as UID `65532`. The mounted data directory must be writable
    by that user:

    ```sh
    mkdir -p data && sudo chown -R 65532:65532 data
    ```

    On [Unraid](deployment.md#unraid), point the volume at
    `/mnt/user/appdata/kasas` and match ownership.

## Local build

Requires Go 1.25+ to build; the running service needs nothing else.

```sh
cp config.example.toml config.toml      # edit as needed
make build
./bin/kasas -config config.toml serve
```

Or run a single sync and exit:

```sh
KASAS_SIMPLEFIN_SETUP_TOKEN="..." ./bin/kasas -config config.toml sync
```

!!! tip "No bank yet? Seed demo data"
    You can explore the dashboard and API without a SimpleFIN credential. Populate
    the database with realistic demo accounts and transactions:

    ```sh
    make seed                # re-runnable
    make seed SEED_EXTRA=60  # add 60 extra synthetic transactions
    ```

## Secure it

By default kasas is **unauthenticated for reads** — fine on a trusted network, but
anyone who can reach the port can read your data. (The dangerous admin operations —
plugin enable, self-update, API-key/webhook/settings changes, MCP-over-HTTP — always
require a token, and kasas refuses to start unauthenticated on a non-loopback bind
unless you opt in; the Docker image ships that opt-in so it boots out of the box. See
[Authentication](../interfaces/authentication.md#unauthenticated-by-default).) Set a
[dashboard token](../interfaces/authentication.md) to require auth for everything
(and consider keeping kasas behind [Tailscale](deployment.md#tailscale)):

```sh
export KASAS_DASHBOARD_TOKEN="$(openssl rand -base64 32)"
# then send it on every request:
curl -H "Authorization: Bearer $KASAS_DASHBOARD_TOKEN" localhost:8080/api/v1/accounts
```

## Next steps

<div class="grid cards" markdown>

-   :material-tune: **[Configuration](configuration.md)** — every option, with env
    var mappings and precedence.

-   :material-server-network: **[Deployment](deployment.md)** — Compose, Unraid,
    Postgres, Vault, Tailscale, and updating.

-   :material-sitemap: **[Architecture](../architecture/overview.md)** — how it all
    works, with diagrams.

-   :material-api: **[REST API](../interfaces/rest-api.md)** — start building on it.

</div>
