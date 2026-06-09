# Ethereum

The **Ethereum source** watches one or more Ethereum addresses and records their
on-chain activity as ledger transactions, via the [Etherscan](https://etherscan.io) API.
It runs *alongside* [SimpleFIN](../architecture/ingestion.md), [Teller](teller.md),
[Plaid](plaid.md), [Bitcoin](bitcoin.md), [CSV import](csv-import.md), and any other
source, so on-chain holdings live in the same ledger as your bank accounts.

Source: [`internal/sources/ethereum`](https://github.com/paulmeier/kasas/tree/main/internal/sources/ethereum).

## How it works

Ethereum is a [`pull` archetype](../architecture/ingestion.md#archetypes-not-providers)
source, the same archetype as SimpleFIN, Teller, Plaid, and Bitcoin. On each
[sync](sync.md) the engine asks it to fetch; it **fans out over every watched address**,
reads each address's transactions and ETH balance from Etherscan, and merges everything
into one neutral batch the engine persists.

- **One account per address.** Each watched address becomes an account
  (`ethereum:<address>`), named with a readable truncation of the address, currency
  `ETH`, under a single **Ethereum** institution.
- **Net balance change per transaction.** Ethereum is account-based, so each transaction
  has a signed value from the address's perspective. The source records the address's
  **net native-ETH change**: value **received** as recipient, minus value **sent** as
  sender, minus the **gas** the address paid as sender. Gas is charged to the sender even
  for a **reverted** transaction (which moves no value), so the recorded amounts sum to
  the address's true external-transaction balance change. Conversion from wei is done
  with arbitrary-precision integers — never a float — so the value is exact.
- **Clean direction.** A sent transaction is described "Sent ETH" with the recipient as
  payee; a received one "Received ETH" with the sender as payee.
- **Per-address ids.** A transaction is stored as `ethereum:<address>:<hash>`. Namespacing
  by address means one transaction touching two watched addresses yields one correct row
  per address. The engine deduplicates by id, so overlapping fetches are idempotent.
- **Lookback as a start block.** The [lookback window](sync.md) is mapped once per sync to
  a start block (Etherscan `getblocknobytime`), so a long-lived address isn't re-walked
  from genesis every run; a failed mapping falls back to the full history.
- **Balances are best-effort**, and **one bad address never blocks the rest** — a failing
  address is logged and skipped, an error returned only when *every* address fails.

!!! info "Quiet until an address is added"
    Ethereum is started only when an Etherscan **API key** is configured. Until an address
    is present it is **skipped on sync, not errored**, so you can set the key and add
    addresses later without failures in between.

## Authentication

Ethereum uses two layers, mapping cleanly onto kasas's config-vs-runtime model:

| Credential | What it is | Where it goes |
| --- | --- | --- |
| **API key** | A free [Etherscan API key](https://etherscan.io/myapikey), an **app-level** secret shared by every watched address. Sent as a query parameter (never echoed in responses or errors). | `[ethereum]` config (set once): `ethereum.api_key` or `KASAS_ETHEREUM_API_KEY`. |
| **Address(es)** | The public addresses to watch — **not secrets**, but managed like multi-credentials: one entry each, add/remove individually. | Runtime — add each on the **Sources** page — and/or `ethereum.address` / `ethereum.addresses`. |

Etherscan's **V2** API serves many EVM chains behind one key, selected by **`chain_id`**
(default `1` = Ethereum mainnet; e.g. `8453` = Base, `42161` = Arbitrum). A missing key or
a bad address surfaces as a **sync error**, not a startup failure.

!!! tip "Self-hosting via Blockscout"
    The source speaks Etherscan's `account` / `txlist` dialect, which
    [Blockscout](https://www.blockscout.com/) also implements. Point `api_url` at a
    Blockscout instance's `/api` endpoint to use your own indexer instead of Etherscan.

## Configuration

Add an `[ethereum]` block to your [config file](../getting-started/configuration.md). The
source starts when `api_key` is set.

```toml
[ethereum]
api_key = "your_etherscan_key"          # required to enable (KASAS_ETHEREUM_API_KEY)
chain_id = 1                            # 1 = Ethereum mainnet (default)

# A single address (env-friendly: KASAS_ETHEREUM_ADDRESS):
address = "0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045"

# …and/or a list, for several addresses:
addresses = [
  "0xAb5801a7D398351b8bE11C439e05C5B3259aeC9B",
]
```

To use your own indexer, override `api_url` (default `https://api.etherscan.io/v2/api`):

```toml
[ethereum]
api_url = "https://eth.blockscout.com/api"   # Etherscan-compatible
api_key = "any-nonempty-value"               # still required to enable the source
```

The singular `address` is the env-friendly form. Config addresses and addresses added at
runtime are **unioned** (deduplicated): config declares your fixed addresses, the Sources
page adds more without a restart.

## Watching addresses at runtime

Open the dashboard **Sources** page: Ethereum lists your watched addresses with a
**Remove** button, plus an **Add** field — paste an address and **Add**, no restart.
Addresses declared in config show as **from config**.

Over REST, each address is one credential entry:

```bash
# Watch an address (append it):
curl -X PUT https://<your-kasas-host>/api/v1/sources/ethereum/credential \
  -H 'Content-Type: application/json' -d '{"token":"0x…"}'

# Stop watching one by its entry id from GET /api/v1/sources:
curl -X DELETE https://<your-kasas-host>/api/v1/sources/ethereum/credentials/<id>
```

## Managing it

Ethereum is a first-class source, so it appears everywhere sources do:

- **Dashboard → Sources** — connection status, the watched-address list (add / remove),
  and **Sync now**.
- **REST** — `GET /api/v1/sources`, `POST /api/v1/sources/ethereum/sync`,
  `PUT /api/v1/sources/ethereum/credential` (watch an address), and
  `DELETE /api/v1/sources/ethereum/credentials/{id}` (stop watching one).
- **MCP** — `list_sources` and `sync_source` (alongside `trigger_sync`). Address
  management stays on REST/dashboard, deliberately not MCP.

## Limitations

Deliberate v1 scope, candidates for follow-ups:

- **Native ETH only.** Amounts cover normal (external) transactions. **ERC-20 token**
  transfers, **internal** transactions (contract-moved ETH), and **NFTs** are not yet
  ingested.
- **Confirmed only.** The txlist feed returns confirmed transactions, so there is no
  pending-ETH state.
- **Structural address validation** (`0x` + 40 hex, lowercased); EIP-55 checksum casing
  is accepted but not verified.

## Where to go next

- [Bitcoin](bitcoin.md) — the sibling on-chain source (UTXO model, mempool.space).
- [Ingestion &amp; Sources](../architecture/ingestion.md) — the source/engine contract
  Ethereum plugs into.
- [Sync Pipeline](sync.md) — the `pull` engine, one run at a time.
- [Transaction Provenance](transaction-provenance.md) — the `source` stamp Ethereum
  writes (`ethereum`).
