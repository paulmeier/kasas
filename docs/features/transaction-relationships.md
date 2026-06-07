# Transaction Relationships

Transactions rarely exist in isolation. A refund refunds a purchase; a transfer
moves money from one account to another; a paycheck pairs with its tax
withholding. Relationships make those connections **explicit and queryable**, so
downstream apps can reconcile transfers, trace refunds, or roll a paycheck up with
its deductions.

Source: [`internal/relationships`](https://github.com/paulmeier/kasas/tree/main/internal/relationships).

## The model

A relationship is a **directed edge** from one transaction to another, stored as a
JSON array on `transactions.relationships`. Each edge is `{kind, target}`, asserted
**outbound** from the transaction that owns the array (the *subject* / "from" side):

```json
{
  "id": "txn_refund",
  "amount": "129.99",
  "relationships": [
    { "kind": "refund_of", "target": "txn_purchase" }
  ]
}
```

`kind` is a **freeform, normalized verb** — a lowercase identifier such as
`refund_of`, `transfer_to`, or `withholding_for`. kasas is a substrate for many
apps, so the vocabulary is **not** hard-coded; pick conventions that fit your
domain. `target` is the id of the related transaction (which must exist).

### One home per edge; the reverse is derived

An edge lives on exactly one transaction — its subject. The **inbound** direction
("what points at me?") is *derived by scanning*, never stored, so an edge can never
disagree with itself. Asking a transaction for its relationships returns its full
neighborhood, each entry tagged with a `direction`:

```sh
curl "localhost:8080/api/v1/transactions/txn_purchase/relationships"
# -> { "id": "txn_purchase", "relationships": [
#      { "kind": "refund_of", "direction": "inbound", "other_transaction_id": "txn_refund" }
#    ] }
```

The refund's view is the mirror image (`"direction": "outbound"`). Because the
reverse is computed, you assert each fact once, from the side that owns it.

## Parallel to labels and extensions, but relational

Relationships join [labels](labels.md) and [schema extensions](schema-extensions.md)
as per-transaction JSON the [sync pipeline](sync.md) never touches. The difference
is what they describe:

| | [Labels](labels.md) | [Extensions](schema-extensions.md) | Relationships |
| --- | --- | --- | --- |
| Shape | key → string | key → any JSON | edge → another transaction |
| Describes | one transaction | one transaction | a **pair** of transactions |
| Direction | — | — | directed (outbound / derived inbound) |

Like both, relationships survive re-syncs untouched and are exposed across every
surface.

## Reading & writing

A `kind` is normalized on write (`"Refund Of"` → `refund_of`); a `target` must
reference an existing transaction and cannot be the subject itself. Adding an edge
that already exists, or removing one that does not, is an idempotent no-op.

```sh
# assert: this refund is a refund_of that purchase
curl -X POST localhost:8080/api/v1/transactions/txn_refund/relationships \
  -H 'content-type: application/json' \
  -d '{"kind":"refund_of","target":"txn_purchase"}'

# remove it
curl -X DELETE "localhost:8080/api/v1/transactions/txn_refund/relationships?kind=refund_of&target=txn_purchase"

# the relationship-kind vocabulary, with per-kind edge counts
curl "localhost:8080/api/v1/relationships"
# -> [{"kind":"refund_of","count":12}, …]
```

| Surface | Capability |
| --- | --- |
| REST | `GET/POST/DELETE /api/v1/transactions/{id}/relationships`; `GET /api/v1/relationships` |
| MCP | `get_transaction_relationships`, `create_transaction_relationship`, `delete_transaction_relationship`, `list_relationship_kinds` |
| Dashboard | Per-row link button opens an **editable** modal (inbound + outbound, with an add form + remove) |

To remove an inbound edge, act on the transaction that *owns* it (its subject) —
the dashboard does this for you when you remove an inbound row.

## Searching relationships

The [search language](search.md) gains two fields, evaluated in Go like every
other field (so they work identically across REST, MCP, and the in-browser
dashboard):

```text
rel:refund_of            transactions that ARE a refund_of something (outbound)
rel:transfer_to=txn_123  an outbound transfer_to edge pointing at txn_123
related:txn_123          any transaction connected to txn_123 (either direction)
```

`rel:` matches the **subject** side, so `rel:refund_of` finds the refunds
themselves — not what they refund. `related:` is direction-agnostic: it returns a
transaction's whole neighborhood.

```sh
curl "localhost:8080/api/v1/transactions/search?q=rel:refund_of"
```

## Events, and why not history

Creating or removing an edge routes through the same [emitter](event-stream.md)
seam as everything else, emitting `relationship.created` / `relationship.removed`
events (entity: the subject transaction) — so webhooks, plugins, and the SSE tail
react to them like any other change.

!!! note "Relationships do not record transaction history"
    An edge is not a field of one transaction's own state — it is a fact *between*
    two. Recording it in the per-transaction
    [version history](transaction-history.md) would mean writing a half-truth to
    one endpoint's timeline. So relationships stay out of history (the same
    reasoning that keeps [provenance](transaction-provenance.md) a derived view),
    while the event stream still makes every edge change observable.

## Out of scope (for now)

- **Automatic detection** — proposing transfer pairs (opposite amounts across
  accounts in a date window) or refund matches. The [rules engine](rules.md)
  can't do this: a rule matches a single transaction and has no way to name a
  target. This is a natural future addition as a dedicated reconciler.
- **Inverse-kind synthesis** — auto-presenting `refund_of` as `refunded_by` on the
  target. Today kinds are freeform and asserted from one side; consumers choose
  their own conventions.
