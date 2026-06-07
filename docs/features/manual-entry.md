# Manual Entry

kasas usually mirrors a bank feed, but it is also a ledger you can **author
directly**. You can create accounts and transactions by hand, edit them, and
delete them — from the [dashboard](../interfaces/dashboard.md), the
[REST API](../interfaces/rest-api.md), or the [MCP server](../interfaces/mcp.md).
This lets you record cash spending or a split alongside synced data, or run kasas
as a fully standalone manual ledger with no SimpleFIN connection at all.

## Manual vs. synced: the `source` column

Every account and transaction carries a `source`: `simplefin` for rows the bridge
owns, `manual` for rows you created. That one field is the whole rule:

!!! info "Only `manual` rows are editable"
    A manual transaction or account can be edited and deleted. A **synced** one is
    **owned by the bridge** and its core fields are read-only — editing it would be
    silently overwritten on the next [sync](sync.md), and deleting it would just
    reappear. Attempting either returns **`409 Conflict`**. (You can still
    [label](labels.md), [extend](schema-extensions.md), and
    [relate](transaction-relationships.md) synced transactions — that metadata is
    yours and survives re-syncs.)

A manual transaction may live in **any** account — including a synced one (handy
for recording a cash purchase against your real checking account). Only the
*transaction's* own `source` decides whether it is editable.

## What you can do

| Action | REST | MCP |
| --- | --- | --- |
| Create an account | `POST /api/v1/accounts` | `create_account` |
| Edit / delete an account | `PUT`/`DELETE /api/v1/accounts/{id}` | `update_account` / `delete_account` |
| Create a transaction | `POST /api/v1/transactions` | `create_transaction` |
| Edit / delete a transaction | `PUT`/`DELETE /api/v1/transactions/{id}` | `update_transaction` / `delete_transaction` |

A transaction needs an `account_id` (must exist), an `amount` (a signed decimal
like `-12.34`, stored verbatim — never rounded), and a `date` (`YYYY-MM-DD`,
RFC3339, or unix seconds); `description`, `payee`, `memo`, and `pending` are
optional. An account needs a `name` and `currency`, with an optional starting
`balance`. Bad input is rejected with **`400`**, an unknown id with **`404`**.

In the dashboard, an **Add account** card and an **+ Add transaction** button open
a small form; manual rows and cards reveal **Edit** / **Delete** controls on hover.

## It rides the same rails as everything else

Manual entry is not a side door — it flows through the same
[transactional emitter](event-stream.md) as the sync poller, so it inherits the
whole platform:

- **Events** — a create emits `transaction.created` / `account.created`; an edit
  emits `transaction.updated`; a delete emits `transaction.deleted` /
  `account.deleted`. Deleting an account emits a `transaction.deleted` for **each**
  of its transactions first (the database cascade is made explicit on the stream),
  then `account.deleted`. [Webhooks](webhooks.md) and existing
  [plugin](plugins.md) `OnTransactionCreate`/`OnTransactionUpdate` hooks fire for
  manual rows too.
- **History** — a manual transaction gets a v1 `imported` snapshot; each edit adds
  an `edited` version with a field-level diff. See
  [Transaction History](transaction-history.md).
- **Provenance** — its lineage reads **"imported from manual"**, then the ordered
  edits. See [Transaction Provenance](transaction-provenance.md).

## Limitations

- **Account balances are static.** A manual account's balance is a value you
  maintain; kasas does not recompute it from the account's transactions. (A derived
  running-balance is a possible future addition.)
- **Plugins don't receive delete hooks.** There is no `OnTransactionDelete` hook
  today, so plugins do not react to manual deletions (creates and edits do reach
  them via the existing hooks).
