# Schema Extensions

Rigid schemas kill platform adoption. Schema extensions let **any app attach its
own namespaced metadata** to a transaction — with no schema change, no migration,
and no coordination with kasas or with other apps. This is the seam that lets
integrations innovate independently.

Source: [`internal/extensions`](https://github.com/paulmeier/kasas/tree/main/internal/extensions).

## The model

Extensions are a JSON object on `transactions.extensions`, mapping **dotted keys**
to **arbitrary JSON values** — string, number, boolean, null, object, or array:

```json
{
  "id": "…",
  "amount": "-45.23",
  "extensions": {
    "tax.category": "meal",
    "forecast.recurring": true,
    "custom.myapp.score": 88,
    "budget.envelope": { "name": "Dining", "limit": 300 }
  }
}
```

The dotted prefix is a **namespace** convention: `tax.*` belongs to a tax app,
`forecast.*` to a forecaster, `custom.myapp.*` to your own integration. Apps stay
out of each other's way by owning a namespace.

## Parallel to labels, not a replacement

Extensions and [labels](labels.md) are deliberately separate models:

| | [Labels](labels.md) | Extensions |
| --- | --- | --- |
| Value type | strings only | **any JSON value** |
| Keys | lowercased, canonical | **namespaced, case-preserved** |
| Audience | human categorization | **app-owned data** |
| Mutators | UI, REST, rules, plugins | REST, MCP, rules, plugins (transaction cell is read-only) |

Where labels are normalized and lowercased, extension keys are **case-preserved**
and values keep their exact JSON type, stored **verbatim**. A `PUT` replaces the
whole set (send the full object; `{}` clears it). And like labels, **re-syncs
never touch them**.

## Reading & writing

```sh
# replace a transaction's full extension set
curl -X PUT localhost:8080/api/v1/transactions/<id>/extensions \
  -H 'content-type: application/json' \
  -d '{"extensions":{"tax.category":"meal","forecast.recurring":true,"custom.myapp.score":88}}'

# the extension vocabulary, with per-key transaction counts
curl "localhost:8080/api/v1/extensions"
# -> [{"namespace":"tax","key":"tax.category","transaction_count":17}, …]
```

| Surface | Capability |
| --- | --- |
| REST | `PUT /api/v1/transactions/{id}/extensions`; `GET /api/v1/extensions` |
| MCP | `set_transaction_extensions` (replace); `list_extensions` (vocabulary) |
| Plugins | `kasas.set_extension` / `kasas.remove_extension` with `extensions:write` |
| Dashboard | Displays extensions **read-only** |

!!! note "Why the dashboard is read-only for extensions"
    Extensions are *app-owned* data with arbitrary structure; a generic
    key:value UI can't safely edit a nested JSON object without risking
    corruption. So writes go through the API/MCP/plugins (where the owning app
    knows the shape), and the UI surfaces them for visibility.

## Searching extensions

The [search language](search.md)'s `ext:` field filters on extensions, matching
values **as text** (so `ext:forecast.recurring=true` works regardless of the
underlying JSON type):

```text
ext:tax.category=meal      key equals value (as text)
ext:custom.myapp.score    key present, any value
ext:tax.category~me       value contains
ext:tax.category!=meal    value not equal
```

```sh
curl "localhost:8080/api/v1/transactions/search?q=ext:tax.category=meal"
```

Like every search field, `ext:` is evaluated in Go (not SQL), so it works
identically across REST, MCP, and the in-browser dashboard.

## Events, history, and the write boundary

An extension change routes through the same [emitter](event-stream.md) seam as
everything else: it emits granular `extension.set` / `extension.removed` events
(one per changed key) and appends an `extended`
[history](transaction-history.md) snapshot — so apps watching the event stream
react to extension changes exactly as they react to labels or amounts.

!!! info "RawMessage vs. any"
    Internally, extension values are stored and written as `json.RawMessage` to
    preserve them byte-for-byte. At the MCP and DTO boundary they are decoded to
    `any`, because the MCP SDK's schema generator rejects `json.RawMessage`. The
    write path keeps values verbatim; the read/MCP path presents them as
    structured JSON.
