# Plaid

The **Plaid source** ingests accounts and transactions from
[Plaid](https://plaid.com) — a widely-used bank-data API with a token-per-connection
model much like [Teller](teller.md) and SimpleFIN. It runs *alongside*
[SimpleFIN](../architecture/ingestion.md), [Teller](teller.md),
[CSV import](csv-import.md), and any other source, so you can mix providers in one
ledger.

Source: [`internal/sources/plaid`](https://github.com/paulmeier/kasas/tree/main/internal/sources/plaid).

## How it works

Plaid is a [`pull` archetype](../architecture/ingestion.md#archetypes-not-providers)
source, the same archetype as SimpleFIN and Teller. On each [sync](sync.md) the engine
asks it to fetch; it **fans out over every linked bank** (each access token), lists
that bank's accounts with balances, pulls its transactions in the
[lookback window](sync.md), resolves the institution name, and merges everything into
one neutral batch the engine persists.

- **One token per bank.** A Plaid access token represents a single *Item* — one
  institution login. Link several banks through Plaid Link and add each token; the
  source fetches them all and merges the results. An Item that fails is logged and
  skipped, so one broken connection never blocks the rest.
- **Sign-flipped amounts.** Plaid signs **outflows positive** — the opposite of
  kasas (and Teller/SimpleFIN). The source negates each amount so a purchase is
  stored negative and a refund positive, the convention every other source uses. The
  flip is done on the decimal *string*, with no float round-trip, so the value is
  exact — the same "[data is sacred](../architecture/philosophy.md#design-principles)"
  handling every source gets.
- **Clean payee, raw description.** Plaid's enriched `merchant_name` becomes the
  `payee`; the raw transaction `name` stays the `description`.
- **Namespaced ids.** Accounts and transactions are stored under `plaid:<id>` so
  Plaid's ids never collide with another source's. The engine deduplicates by id, so
  overlapping fetches across syncs are idempotent.
- **Balances and institution names are best-effort.** A balance that is absent, or an
  institution name that fails to resolve, is handled gracefully — the account still
  imports with its transactions rather than failing the sync.
- **Transactions are non-fatal.** If an Item's transactions can't be fetched yet
  (Plaid's `PRODUCT_NOT_READY` right after linking), its accounts and balances still
  import and the next sync picks up the transactions.

!!! info "Quiet until connected"
    Plaid is started only when both `client_id` and `secret` are configured. Until an
    access token is present it is **skipped on sync, not errored** — so setting up the
    app credentials and adding bank tokens later never logs failures in between.

## Authentication

Plaid uses two layers of credentials, and the split maps cleanly onto kasas's
config-vs-runtime secret model:

| Credential | What it is | Where it goes |
| --- | --- | --- |
| **`client_id` + `secret`** | Your **app-level** Plaid credentials from the [Plaid Dashboard](https://dashboard.plaid.com), shared by every linked bank. The secret is **per-environment**. Sent in each request body. | `[plaid]` config (infrastructure-level, set once): `plaid.client_id` / `plaid.secret`, or `KASAS_PLAID_CLIENT_ID` / `KASAS_PLAID_SECRET`. |
| **Access token(s)** | A per-**Item** token — one per linked bank — obtained by exchanging a **Plaid Link** `public_token`. Identifies whose data to fetch. | Runtime secrets — add each on the **Sources** page, and/or set `plaid.access_token` / `plaid.access_tokens`. |

The **`environment`** (`sandbox` by default, or `development` / `production`) selects
the Plaid host; the `secret` must match it. A bad or missing credential surfaces as a
Plaid **sync error**, not a startup failure — one misconfigured source never takes
down the rest.

!!! note "Getting an access token"
    Plaid access tokens come from the [Link](https://plaid.com/docs/link/) flow: a
    front-end exchanges a `public_token` for an access token via
    [`/item/public_token/exchange`](https://plaid.com/docs/api/items/#itempublic_tokenexchange).
    kasas consumes the resulting access token; running Link itself is outside kasas
    (use Plaid's [Quickstart](https://plaid.com/docs/quickstart/) or your own Link
    integration). In the sandbox you can mint one with
    [`/sandbox/public_token/create`](https://plaid.com/docs/api/sandbox/#sandboxpublic_tokencreate)
    followed by the exchange.

## Configuration

Add a `[plaid]` block to your [config file](../getting-started/configuration.md). The
source starts when **both** `client_id` and `secret` are set.

=== "Sandbox"

    ```toml
    [plaid]
    client_id   = "your_client_id"
    secret      = "your_sandbox_secret"
    environment = "sandbox"
    access_token = "access-sandbox-…"   # or add it from the Sources page
    ```

=== "Production"

    Set the production secret and environment, then add each bank's access token at
    runtime from the Sources page (it comes from Link, so the dashboard is the natural
    place for it).

    ```toml
    [plaid]
    client_id   = "your_client_id"
    secret      = "your_production_secret"
    environment = "production"
    # access_token left empty — add banks from the dashboard Sources page.
    ```

For several banks, declare them as an array — and/or add more at runtime from the
Sources page:

```toml
[plaid]
client_id     = "your_client_id"
secret        = "your_production_secret"
environment   = "production"
country_codes = ["US"]   # scopes institution-name lookups (optional; default US)
access_tokens = ["access-production-…", "access-production-…"]
```

The singular `access_token` is the env-friendly form (`KASAS_PLAID_ACCESS_TOKEN`).
Config tokens and tokens added at runtime are **unioned** (deduplicated): config
declares your fixed banks, the Sources page adds more without a restart.

## Connecting banks at runtime

Once the source is started, open the dashboard **Sources** page: Plaid lists your
connected banks (each masked) with a **Remove** button, and an **Add** field. For each
bank, run it through Plaid Link to obtain an access token, paste it, and **Add** — no
restart. Repeat for every bank; **Remove** disconnects one. Banks declared in config
show as **from config** (edit the config file to change those).

Over REST, each token is one bank:

```bash
# Add a bank (append a token):
curl -X PUT https://<your-kasas-host>/api/v1/sources/plaid/credential \
  -H 'Content-Type: application/json' -d '{"token":"<access-token>"}'

# Remove a bank by its (masked) entry id from GET /api/v1/sources:
curl -X DELETE https://<your-kasas-host>/api/v1/sources/plaid/credentials/<id>
```

## Managing it

Plaid is a first-class source, so it appears everywhere sources do:

- **Dashboard → Sources** — connection status, the per-bank credential list (add /
  remove), and **Sync now**.
- **REST** — `GET /api/v1/sources` (lists each bank, masked), `POST /api/v1/sources/plaid/sync`,
  `PUT /api/v1/sources/plaid/credential` (add a bank), and
  `DELETE /api/v1/sources/plaid/credentials/{id}` (remove one).
- **MCP** — `list_sources` and `sync_source` (alongside `trigger_sync`, which syncs
  every source). Credential management stays on REST/dashboard, deliberately not MCP.

## Limitations

- **Incremental sync.** kasas re-fetches the [lookback window](sync.md) each run via
  Plaid's `/transactions/get` (idempotent — the engine deduplicates by id), the same
  model as Teller and SimpleFIN. It does not yet use Plaid's cursor-based
  `/transactions/sync`, so **removed** transactions (cancelled pendings) are not
  pruned.
- **Transaction enrichment beyond payee** (category, personal-finance category,
  location) is not yet mapped; it is a candidate for [extensions](schema-extensions.md)
  when the engine persists per-batch extensions.

## Where to go next

- [Ingestion &amp; Sources](../architecture/ingestion.md) — the source/engine contract
  Plaid plugs into.
- [Teller](teller.md) — the closest sibling source (token-per-bank fan-out).
- [Sync Pipeline](sync.md) — the `pull` engine, one run at a time.
- [Transaction Provenance](transaction-provenance.md) — the `source` stamp Plaid
  writes (`plaid`).
