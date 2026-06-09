# Bitcoin

The **Bitcoin source** watches one or more public Bitcoin addresses and records their
on-chain activity as ledger transactions. It needs **no API key** and runs *alongside*
[SimpleFIN](../architecture/ingestion.md), [Teller](teller.md), [Plaid](plaid.md),
[CSV import](csv-import.md), and any other source, so on-chain holdings live in the same
ledger as your bank accounts.

Source: [`internal/sources/bitcoin`](https://github.com/paulmeier/kasas/tree/main/internal/sources/bitcoin).

## How it works

Bitcoin is a [`pull` archetype](../architecture/ingestion.md#archetypes-not-providers)
source, the same archetype as SimpleFIN, Teller, and Plaid. On each [sync](sync.md) the
engine asks it to fetch; it **fans out over every watched address**, reads each
address's transaction history and confirmed balance from a
[mempool.space](https://mempool.space/docs/api/rest) / Esplora API, and merges
everything into one neutral batch the engine persists.

- **One account per address.** Each watched address becomes an account
  (`bitcoin:<address>`), named with a readable truncation of the address, currency
  `BTC`, under a single **Bitcoin** institution.
- **Net amount per transaction.** Bitcoin's UTXO model has no single signed amount for
  an address, so the source computes the address's **net delta** for each transaction —
  the value of outputs paying it minus the value of inputs spending from it — in
  satoshis, then renders it as an **exact BTC decimal** (received **+**, sent **−**,
  matching the convention every other source uses). The conversion is done with
  arbitrary-precision integers, never a float, so the value is exact — the same
  "[data is sacred](../architecture/philosophy.md#design-principles)" handling
  everywhere.
- **Per-address ids.** A transaction is stored as `bitcoin:<address>:<txid>`. Because
  the id is namespaced by address, one transaction that touches two watched addresses
  (e.g. a transfer between your own wallets) yields one correct row per address — each
  with that address's own net amount. The engine deduplicates by id, so overlapping
  fetches across syncs are idempotent.
- **Pending support.** Unconfirmed (mempool) transactions import as `pending`; when they
  confirm, the next sync updates them in place.
- **Balances are best-effort.** The confirmed balance (funded − spent) is filled in when
  available; if it can't be read, the account still imports with its transactions.
- **One bad address never blocks the rest.** An address whose history can't be read is
  logged and skipped; an error is returned only when *every* address fails.

!!! info "Quiet until an address is added"
    Bitcoin is started when at least one address — or a custom `api_url` — is configured.
    Until an address is present it is **skipped on sync, not errored**, so you can enable
    it and add addresses later without failures in between.

## Addresses are the credential

A Bitcoin address is public, so it is not a secret — but it fits kasas's
**multi-credential** model exactly: each watched address is one entry you add and remove
individually, fanned out over on each sync, just like a Teller or Plaid bank. So
addresses are managed wherever credentials are: in config, or at runtime on the
**Sources** page. Supported formats are legacy (`1…`), P2SH (`3…`), and bech32 (`bc1…`);
addresses are validated structurally on add (the node is the final arbiter — a
well-formed but nonexistent address simply yields no transactions).

## Configuration

Add a `[bitcoin]` block to your [config file](../getting-started/configuration.md). The
source starts when at least one address or a custom `api_url` is set.

```toml
[bitcoin]
# A single address (env-friendly form: KASAS_BITCOIN_ADDRESS):
address = "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"

# …and/or a list, for several addresses:
addresses = [
  "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa",
  "3J98t1WpEZ73CNmQviecrnyiWrnqRhWNLy",
]
```

To **self-host** the data source, point `api_url` at your own mempool.space / Esplora
instance (the default is `https://mempool.space/api`). Setting `api_url` alone is also
the way to enable Bitcoin with no config addresses, then add them all from the dashboard:

```toml
[bitcoin]
api_url = "https://mempool.mynode.lan/api"
# addresses added from the dashboard Sources page
```

Config addresses and addresses added at runtime are **unioned** (deduplicated): config
declares your fixed addresses, the Sources page adds more without a restart.

## Watching addresses at runtime

Open the dashboard **Sources** page: Bitcoin lists your watched addresses with a
**Remove** button, plus an **Add** field — paste an address and **Add**, no restart.
Addresses declared in config show as **from config** (edit the config file to change
those).

Over REST, each address is one credential entry:

```bash
# Watch an address (append it):
curl -X PUT https://<your-kasas-host>/api/v1/sources/bitcoin/credential \
  -H 'Content-Type: application/json' -d '{"token":"bc1q…"}'

# Stop watching one by its entry id from GET /api/v1/sources:
curl -X DELETE https://<your-kasas-host>/api/v1/sources/bitcoin/credentials/<id>
```

## Managing it

Bitcoin is a first-class source, so it appears everywhere sources do:

- **Dashboard → Sources** — connection status, the watched-address list (add / remove),
  and **Sync now**.
- **REST** — `GET /api/v1/sources` (lists each address), `POST /api/v1/sources/bitcoin/sync`,
  `PUT /api/v1/sources/bitcoin/credential` (watch an address), and
  `DELETE /api/v1/sources/bitcoin/credentials/{id}` (stop watching one).
- **MCP** — `list_sources` and `sync_source` (alongside `trigger_sync`, which syncs every
  source). Address management stays on REST/dashboard, deliberately not MCP.

## Limitations

These are deliberate v1 scope, candidates for follow-ups:

- **Native BTC only.** The amount is the address's net BTC delta. Per-input/output
  detail, the fee, and counterparties are not yet mapped (candidates for
  [extensions](schema-extensions.md) when the engine persists per-batch extensions).
- **Address-by-address.** Watch individual addresses; **xpub / output-descriptor**
  wallet expansion (deriving an address gap-limit) is not implemented.
- **Structural validation.** Addresses are checked for prefix/length/alphabet, not full
  base58check / bech32 checksums — the node rejects a truly invalid address at sync.

## Where to go next

- [Ethereum](ethereum.md) — the sibling on-chain source (account model, Etherscan).
- [Ingestion &amp; Sources](../architecture/ingestion.md) — the source/engine contract
  Bitcoin plugs into.
- [Sync Pipeline](sync.md) — the `pull` engine, one run at a time.
- [Transaction Provenance](transaction-provenance.md) — the `source` stamp Bitcoin
  writes (`bitcoin`).
