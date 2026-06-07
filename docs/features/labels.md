# Labels

Labels are kasas's built-in categorization: strict **`key: value`** pairs attached
to a transaction. They are the metadata *you* own — a sync refreshes everything
the source reports but **never touches your labels**.

Source: [`internal/labels`](https://github.com/paulmeier/kasas/tree/main/internal/labels).

## The model

A transaction's labels are a JSON object of `key → value`, both non-empty strings,
stored in the `transactions.labels` column:

```json
{ "category": "food", "trip": "japan-2024", "status": "reviewed" }
```

A "simple tag" is just a label with a conventional key — e.g. `tag: groceries`.
There is no separate tag type; one `key:value` model covers both.

## Normalization

Every write runs through `labels.Normalize`, which makes labels predictable and
collision-free:

- **Keys** are lowercased, trimmed, stripped of quotes / backslashes / control
  characters, and capped at 50 runes — so `Category`, `category `, and `"category"`
  all converge to `category`.
- **Values** are trimmed and capped at 50 runes; their case is **preserved**.
- Pairs with an empty key or value are dropped (the "strict" in strict
  `key:value`).
- At most 50 labels per transaction; keys are processed in sorted order for
  deterministic output.

## Filtering

Label keys are canonical (lowercase), so **filtering by key is case-insensitive**;
value matching in the REST drill-down is **exact** (case-sensitive). The filter is
pushed down to JSON SQL in *both* the SQLite and Postgres backends, so a consumer
can build its own views without scanning every row:

```sh
# every transaction carrying the key, any value
curl "localhost:8080/api/v1/transactions?label_key=category"
# scoped to one exact value
curl "localhost:8080/api/v1/transactions?label_key=category&label_value=food"
```

For richer matching — value contains, not-equal, presence, combined with other
fields and booleans — use the [search language](search.md)'s `label:` field, which
matches **case-insensitively**:

```text
label:category=food        label value equals (case-insensitive)
category:food              shorthand for the above
label:category             key present, any value
label:store~whole         value contains
label:category!=food      value not equal
```

## Editing labels

| Surface | How |
| --- | --- |
| Dashboard | Click a transaction's **Labels** cell and type `category: food`; typeahead suggests existing labels. |
| REST | `PUT /api/v1/transactions/{id}/labels` with `{"labels":{"category":"food"}}` — replaces the whole set. |
| Rules | A [rule](rules.md) applies labels automatically on match. |
| Plugins | A [plugin](plugins.md) with the `labels:write` capability calls `kasas.apply_labels`. |

Every one of these routes through the same [emitter](event-stream.md) seam, so a
label change — wherever it originates — emits granular `label.applied` /
`label.removed` events (one per changed key) and appends a `labeled`
[history](transaction-history.md) snapshot. The diff between old and new labels is
computed once, centrally, and reused by all writers.

## The label vocabulary

```sh
# every label in use, with how many transactions carry it
curl "localhost:8080/api/v1/labels"
# -> [{"key":"category","value":"food","transaction_count":42}, …]

# remove a key from every transaction (add ?value= to scope to one value)
curl -X DELETE "localhost:8080/api/v1/labels/category"
```

A bulk vocabulary delete emits a single coarse `label.removed` event with
`entity_type: "label"` (versus the granular per-transaction events of a single
edit). The vocabulary is also available via the `list_labels` MCP tool and the
dashboard **Labels** page.

## Labels vs. extensions

Labels are for **human categorization** — strict, lowercase-keyed strings you
filter and group by. When an *application* needs to attach richer, typed, or
namespaced data, that's what [schema extensions](schema-extensions.md) are for.
They're parallel models, not competitors:

| | Labels | [Extensions](schema-extensions.md) |
| --- | --- | --- |
| Value type | strings only | any JSON (string, number, bool, null, object, array) |
| Keys | lowercased, canonical | namespaced, case-preserved |
| Audience | you (categorization) | apps (integration data) |
| Set by | UI, REST, rules, plugins | REST, MCP, plugins (UI shows read-only) |
