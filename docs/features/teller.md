# Teller

The **Teller source** ingests accounts and transactions from
[Teller](https://teller.io) — a US bank-data API with a token-per-connection
model much like SimpleFIN. It runs *alongside* [SimpleFIN](../architecture/ingestion.md),
[CSV import](csv-import.md), and any other source, so you can mix providers in one
ledger.

Source: [`internal/sources/teller`](https://github.com/paulmeier/kasas/tree/main/internal/sources/teller).

## How it works

Teller is a [`pull` archetype](../architecture/ingestion.md#archetypes-not-providers)
source, the same archetype as SimpleFIN. On each [sync](sync.md) the engine asks it
to fetch; it **fans out over every linked bank** (each access token), lists that
bank's accounts and, for each, pulls the balance and the transactions in the
[lookback window](sync.md), merging everything into one neutral batch the engine
persists.

- **One token per bank.** A Teller access token covers a single enrollment (one
  bank login). Link several banks through Teller Connect and add each token — the
  source fetches them all and merges the results. A bank that fails is logged and
  skipped, so one broken connection never blocks the rest.
- **Verbatim amounts.** A transaction's signed amount is stored exactly as Teller
  returns it (negative = money out), with no float round-trip — the same
  "[data is sacred](../architecture/philosophy.md#design-principles)" handling every
  source gets.
- **Clean payee, raw description.** Teller's enriched counterparty becomes the
  `payee`; the raw bank text stays the `description`.
- **Namespaced ids.** Accounts and transactions are stored under `teller:<id>` so
  Teller's ids never collide with another source's. The engine deduplicates by id,
  so overlapping fetches across syncs are idempotent.
- **Balances are best-effort.** A balance that fails to load is logged and the
  account still imports with its transactions, rather than failing the whole sync.

!!! info "Quiet until connected"
    Teller is started only when you configure it (an access token or a client
    certificate). Until an access token is present it is **skipped on sync, not
    errored** — so configuring the mTLS certificate for production and adding the
    token later never logs failures in between.

## Authentication

Teller uses two credentials, and the split maps cleanly onto kasas's config-vs-runtime
secret model:

| Credential | What it is | Where it goes |
| --- | --- | --- |
| **Access token(s)** | A per-enrollment token from **Teller Connect** — one per linked bank. Add as many as you have banks. Sent to the API as the HTTP basic-auth username. | Runtime secrets — add each on the **Sources** page, and/or set `teller.access_token` / `teller.access_tokens`. |
| **Client certificate** | A mutual-TLS certificate + private key, **required for the `development` and `production` environments** (all requests that touch real user data). Not needed in `sandbox`. | Filesystem paths in `[teller]` config (infrastructure-level, set once). |

A bad or missing certificate surfaces as a Teller **sync error**, not a startup
failure — one misconfigured source never takes down the rest.

## Configuration

Add a `[teller]` block to your [config file](../getting-started/configuration.md).
The source starts when an access token **or** a certificate is set.

=== "Production / development (mTLS)"

    Download the client certificate from the Teller dashboard, put the PEM files on
    disk, and reference them. Then add the per-bank access token at runtime from the
    Sources page (it comes from Teller Connect, so the dashboard is the natural place
    for it).

    ```toml
    [teller]
    certificate = "/data/teller/certificate.pem"
    private_key = "/data/teller/private_key.pem"
    # access_token left empty — set it from the dashboard Sources page.
    ```

=== "Sandbox (no certificate)"

    The sandbox needs no certificate; just provide a sandbox access token.

    ```toml
    [teller]
    access_token = "test_token_…"
    ```

For several banks, declare them as an array — and/or add more at runtime from the
Sources page:

```toml
[teller]
access_tokens = ["token_chase_…", "token_amex_…"]
certificate   = "/data/teller/certificate.pem"
private_key   = "/data/teller/private_key.pem"
```

The singular `access_token` is the env-friendly form (`KASAS_TELLER_ACCESS_TOKEN`).
Config tokens and tokens added at runtime are **unioned** (deduplicated): config
declares your fixed banks, the Sources page adds more without a restart.

## Connecting banks at runtime

Once the source is started, open the dashboard **Sources** page: Teller lists your
connected banks (each masked) with a **Remove** button, and an **Add** field. For
each bank, link it through Teller Connect (in your Teller app/flow) to obtain an
access token, paste it, and **Add** — no restart. Repeat for every bank; **Remove**
disconnects one. Banks declared in config show as **from config** (edit the config
file to change those).

Over REST, each token is one bank:

```bash
# Add a bank (append a token):
curl -X PUT https://<your-kasas-host>/api/v1/sources/teller/credential \
  -H 'Content-Type: application/json' -d '{"token":"<access-token>"}'

# Remove a bank by its (masked) entry id from GET /api/v1/sources:
curl -X DELETE https://<your-kasas-host>/api/v1/sources/teller/credentials/<id>
```

## Managing it

Teller is a first-class source, so it appears everywhere sources do:

- **Dashboard → Sources** — connection status, the per-bank credential list (add /
  remove), and **Sync now**.
- **REST** — `GET /api/v1/sources` (lists each bank, masked), `POST /api/v1/sources/teller/sync`,
  `PUT /api/v1/sources/teller/credential` (add a bank), and
  `DELETE /api/v1/sources/teller/credentials/{id}` (remove one).
- **MCP** — `list_sources` and `sync_source` (alongside `trigger_sync`, which syncs
  every source). Credential management stays on REST/dashboard, deliberately not MCP.

## Limitations

- **Transaction enrichment beyond payee** (category, counterparty type) is not yet
  mapped; it is a candidate for [extensions](schema-extensions.md) when the engine
  persists per-batch extensions.

## Where to go next

- [Ingestion &amp; Sources](../architecture/ingestion.md) — the source/engine contract
  Teller plugs into.
- [Sync Pipeline](sync.md) — the `pull` engine, one run at a time.
- [Transaction Provenance](transaction-provenance.md) — the `source` stamp Teller
  writes (`teller`).
