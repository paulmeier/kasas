# Deployment

kasas is a single static binary in a tiny container, designed to be boring to run
and forget. This page covers the common deployment shapes.

## Container image

Multi-arch (amd64/arm64) images are published to GHCR on every release, built
`FROM scratch` — ~12 MB pulled, ~24 MB on disk for linux/amd64 (the embedded WASM
dashboard adds ~5 MB).

```sh
docker pull ghcr.io/paulmeier/kasas:latest
```

The image's `HEALTHCHECK` runs [`kasas healthcheck`](../reference/cli.md#healthcheck)
(the `scratch` base has no shell or `curl`).

## Docker Compose

The bundled `docker-compose.yml` mounts `./data` and reads the setup token from the
environment:

```sh
export KASAS_SIMPLEFIN_SETUP_TOKEN="<your base64 setup token>"
docker compose up -d            # or: up -d --build to build locally
```

The access URL is persisted to `./data/secrets.json` on first sync, so the token is
consumed once. Swap the Compose `build:` for `image: ghcr.io/paulmeier/kasas:latest`
to run the published image.

## Volume permissions

The container runs as a non-root user, **UID `65532`**. The mounted data directory
must be writable by that user:

```sh
mkdir -p data && sudo chown -R 65532:65532 data
```

## Postgres

kasas runs on SQLite (default) or Postgres with the **same binary**, selected at
runtime — see [Data Model → multi-dialect storage](../architecture/data-model.md#multi-dialect-storage).
Point it at a server and it creates its schema on first start:

```sh
KASAS_DATABASE_DRIVER=postgres \
KASAS_DATABASE_DSN="postgres://user:pass@host:5432/kasas?sslmode=disable" \
kasas serve
```

The Compose file includes an optional Postgres service behind a profile:

```sh
# set KASAS_DATABASE_DRIVER=postgres + KASAS_DATABASE_DSN in docker-compose.yml, then:
docker compose --profile postgres up -d
```

### Moving an existing SQLite ledger to Postgres

Pointing kasas at a fresh Postgres database creates an **empty** schema — it does
not carry your SQLite data across on its own. To bring an existing ledger with
you, run the built-in migration once:

```sh
kasas -config config.toml migrate-postgres \
  "postgres://user:pass@host:5432/kasas?sslmode=disable"
```

It copies every table — accounts, transactions, rules, events, history, settings,
and the rest — into the (empty) Postgres database with ids preserved, reading the
SQLite file without modifying it. Then set `database.driver=postgres` and
`database.dsn` and restart. The same thing is available from the dashboard under
**Settings → Migrate to Postgres**. See the
[migration guide](migrate-to-postgres.md) for the full walkthrough and caveats.

## Vault

Instead of the local `0600` `secrets.json`, store the SimpleFIN access URL and the
dashboard token in HashiCorp Vault (KV v2). Enable `[vault]` (see
[Configuration → `[vault]`](configuration.md#vault)):

```toml
[vault]
enabled = true
address = "https://vault.example.com:8200"   # or VAULT_ADDR
token   = "…"                                  # or VAULT_TOKEN
mount   = "secret"
path    = "kasas"
```

kasas reads and writes both secrets under one KV path, merging keys so setting one
doesn't disturb the other.

## Tailscale

kasas has a single shared-secret auth model and no per-user accounts, so the
strongest posture is to keep it **off the public internet** and reach it over a
private network. [Tailscale](https://tailscale.com) is an easy fit: run kasas bound
to its tailnet address (or behind a Tailscale sidecar/serve) and only your devices
can reach it.

!!! warning "A tailnet address is still 'beyond loopback'"
    kasas treats any non-loopback bind — including a Tailscale CGNAT
    (`100.64.0.0/10`) address — as exposed, so it **refuses to start** there without
    a [dashboard token](../interfaces/authentication.md) unless you set
    `server.allow_unauthenticated = true` (`KASAS_SERVER_ALLOW_UNAUTHENTICATED=true`).
    Setting a token is the recommended path: it secures the dangerous admin
    operations too, and the [in-place updater](#updating) (when
    `update.allow_apply` is on) is then both tailnet-only and token-gated.

## Unraid

Paul self-hosts kasas on Unraid. Point the data volume at the appdata share and
match ownership to the container's UID:

```sh
mkdir -p /mnt/user/appdata/kasas
chown -R 65532:65532 /mnt/user/appdata/kasas
```

Map the container's `/data` to `/mnt/user/appdata/kasas`, publish port `8080`, and
set `KASAS_SIMPLEFIN_SETUP_TOKEN` (first run) and `KASAS_DASHBOARD_TOKEN`. For
Docker deployments, set `KASAS_UPDATE_CHECK=false` and update by pulling a new
image (below).

## Updating

=== "Docker"

    Pull the new image and recreate the container:

    ```sh
    docker pull ghcr.io/paulmeier/kasas:latest
    docker compose up -d
    ```

    Set `KASAS_UPDATE_CHECK=false` to suppress the outbound release check, since
    you update by pulling.

=== "Binary (self-update)"

    Every release publishes static binaries (linux/darwin × amd64/arm64) with
    SHA-256 checksums. The binary updates itself in place:

    ```sh
    kasas self-update          # download, verify, replace
    kasas self-update -check    # report only; install nothing
    ```

    It verifies the download against the published `.sha256` and refuses on a
    mismatch. Restart afterwards to run the new version. See the
    [CLI reference](../reference/cli.md#self-update).

=== "From the dashboard"

    When a newer release is available the dashboard shows an **"Update & restart"**
    banner. Clicking it calls `POST /api/v1/update`, which performs the same
    download → verify → replace and then **re-execs the new binary in place** (no
    external supervisor); the page reloads onto the new version.

!!! danger "Securing the in-place updater"
    `update.allow_apply` is **off by default**. Turn it on to let the dashboard/API
    replace the running binary with a checksum-verified GitHub release and restart
    it. Even when on, the apply lives in the **admin tier**: it requires the
    [dashboard token](../interfaces/authentication.md) and is refused (`503`) on an
    unsecured instance. Keep kasas on a trusted network (e.g. [Tailscale](#tailscale))
    too, or leave `allow_apply` off and use the `kasas self-update` CLI to upgrade.

While `serve` runs, kasas also checks **once a day** for a newer release and logs a
notice — it never self-modifies from the check. Builds without a release version
(e.g. `dev`) skip the check entirely.
