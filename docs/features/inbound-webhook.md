# Inbound webhook

The **inbound-webhook source** lets an external system *push* transactions into
kasas by POSTing them to an HTTP endpoint, instead of kasas pulling on a schedule.
It is the ingestion mirror of kasas's [outbound webhooks](webhooks.md) — and it
reuses the exact same HMAC signing scheme, so one kasas instance's outbound webhook
can feed another's inbound source unchanged.

Source: [`internal/sources/webhook`](https://github.com/paulmeier/kasas/tree/main/internal/sources/webhook).
Design: [ADR 0008](../architecture/decisions/0008-inbound-webhook-source.md).

## How it works

The inbound webhook is a [`webhook` archetype](../architecture/ingestion.md#archetypes-not-providers)
source — the first **push** source in kasas. Where a `pull` source implements
`Fetch` and the engine calls it on a timer, this source implements `Receive`: an
external POST to its ingest endpoint hands it the request, it authenticates and
parses the body into the neutral [`ImportBatch`](../architecture/ingestion.md#the-importbatch),
and the engine persists that batch through the **same path every source uses** —
idempotent dedup, [events](event-stream.md), [rule](rules.md) auto-labeling,
[history](transaction-history.md), and [provenance](transaction-provenance.md)
stamping all included.

- **Pushed, not polled.** There is no schedule and no "Sync now" — a delivery is
  ingested the instant it arrives. The run still appears in [sync history](sync.md)
  and emits a `sync.completed` event, like a scheduled sync.
- **Namespaced ids.** Accounts and transactions are stored under `webhook:<id>`, so
  ids never collide with another source's. The engine deduplicates by id, so
  **re-delivering the same batch is safe** (it updates, never duplicates).
- **Stable ids for unkeyed rows.** If a transaction omits `external_id`, the source
  derives a deterministic content hash from its fields, so retries still dedupe.
- **Provenance.** Every pushed transaction is stamped `source = "webhook"`,
  immutable thereafter.

!!! info "Inert until you generate a secret"
    The source is always registered, but it **rejects every delivery until you
    generate its signing secret**. An instance you never opt into exposes nothing.

## Authentication

The ingest endpoint is internet-reachable and **does not use the dashboard token**
(the sender does not have it). Instead, every request must carry an
**HMAC-SHA256 signature** computed with a shared secret kasas mints for you — the
identical scheme kasas uses to *sign* its [outbound webhooks](webhooks.md):

| Header | Value |
| --- | --- |
| `X-Kasas-Timestamp` | The Unix time (seconds) you signed at. |
| `X-Kasas-Signature` | `sha256=` + hex `HMAC_SHA256(secret, "<timestamp>.<body>")`. |

The source recomputes the signature over the **raw request body** and compares in
constant time. It also rejects a timestamp more than **5 minutes** from its clock
(replay resistance — and re-delivery is idempotent anyway). Any failure — missing or
malformed signature, wrong secret, stale timestamp — returns a coarse **`401`** that
never reveals which check failed.

### Generating the secret

Generate (and later rotate or reveal) the secret from the **Sources → Inbound
webhook** page, or over the API/MCP:

=== "Dashboard"

    Open **Sources → Inbound webhook**, click **Generate signing secret**, and copy
    the secret and the ingest URL into your sender. Use **Rotate secret** to replace
    it (the old one stops working immediately) or **Reveal secret** to copy it again.

=== "REST"

    ```bash
    # Mint (or rotate) the secret — admin (dashboard token) only.
    curl -X POST https://kasas.example/api/v1/sources/webhook/secret/rotate \
      -H "Authorization: Bearer $KASAS_TOKEN"
    # -> {"secret":"whsec_…","ingest_path":"/api/v1/sources/webhook/ingest"}

    # Reveal the current secret later.
    curl https://kasas.example/api/v1/sources/webhook/secret \
      -H "Authorization: Bearer $KASAS_TOKEN"
    ```

=== "MCP"

    Call `rotate_source_secret` (or `reveal_source_secret`) with
    `{"type": "webhook"}`. Returns the secret and the ingest path.

Revealing and rotating the secret are **admin-only** (the dashboard token, never an
API key) — the secret is the source's entire auth boundary.

## Sending a delivery

POST a [`ImportBatch`](../architecture/ingestion.md#the-importbatch) JSON to the
ingest endpoint. You set the account(s) and their transactions; the `source` field
is ignored (kasas always stamps `webhook`).

```
POST /api/v1/sources/webhook/ingest
```

```json
{
  "accounts": [
    {
      "external_id": "checking",
      "name": "Main checking",
      "currency": "USD",
      "balance": "1250.00",
      "org": { "name": "Acme Bank" },
      "transactions": [
        {
          "external_id": "txn-2026-06-26-001",
          "amount": "-12.50",
          "date": 1750000000,
          "payee": "Blue Bottle",
          "description": "Coffee",
          "memo": "",
          "pending": false
        }
      ]
    }
  ]
}
```

- `amount` is a decimal **string**, negative for money out (kasas never round-trips
  amounts through a float).
- `date` is **Unix seconds**.
- `external_id` on an account/transaction is optional but recommended — provide it
  and re-delivering updates the same row; omit it and kasas derives a stable id from
  the transaction's content.
- Restate the account's `balance` on each delivery if you want it kept current; an
  omitted balance is stored blank.

A valid delivery returns **`202 Accepted`** with a summary
(`{"status":"accepted","accounts":…,"new_transactions":…,"updated_transactions":…}`).
An empty body, `{}`, or `{"accounts":[]}` is an accepted **no-op** (handy as a
verification ping). The body is capped at **1 MiB**.

### Worked example: signing a request

```bash
SECRET="whsec_…"                       # from the rotate/reveal step
BODY='{"accounts":[{"external_id":"checking","name":"Main checking","currency":"USD","transactions":[{"external_id":"t1","amount":"-12.50","date":1750000000,"payee":"Blue Bottle"}]}]}'
TS=$(date +%s)
SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')

curl -X POST https://kasas.example/api/v1/sources/webhook/ingest \
  -H "X-Kasas-Timestamp: $TS" \
  -H "X-Kasas-Signature: sha256=$SIG" \
  -H "Content-Type: application/json" \
  -d "$BODY"
```

```python
import hashlib, hmac, time, requests

secret = "whsec_…"
body = '{"accounts":[{"external_id":"checking","name":"Main checking","currency":"USD",' \
       '"transactions":[{"external_id":"t1","amount":"-12.50","date":1750000000,"payee":"Blue Bottle"}]}]}'
ts = str(int(time.time()))
sig = hmac.new(secret.encode(), f"{ts}.{body}".encode(), hashlib.sha256).hexdigest()

requests.post(
    "https://kasas.example/api/v1/sources/webhook/ingest",
    data=body,
    headers={
        "X-Kasas-Timestamp": ts,
        "X-Kasas-Signature": "sha256=" + sig,
        "Content-Type": "application/json",
    },
)
```

!!! warning "Sign the exact bytes you send"
    The signature is over the **raw body**, so sign the precise byte string you put
    on the wire — do not re-serialize or pretty-print it after signing, or the
    signature will not match and the delivery is rejected with `401`.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| `401 unauthorized` | No secret generated yet; wrong secret; signature over re-serialized/modified body; timestamp more than 5 minutes off; missing `X-Kasas-*` header. |
| `413 payload too large` | Body exceeds the 1 MiB cap — split into multiple deliveries. |
| `400 could not ingest delivery` | Authenticated but unusable: malformed JSON, or an account with neither `external_id` nor `name`. |
| `404 unknown source` | The path type is wrong — the endpoint is `/api/v1/sources/webhook/ingest`. |

Successful deliveries show up on the **Transactions** page, in [sync history](sync.md),
and on the [event stream](event-stream.md) as `transaction.created` (and
`sync.completed`).

## Surfaces

| Surface | What you get |
| --- | --- |
| **REST** | `POST /api/v1/sources/webhook/ingest` (open, HMAC-verified); `GET /api/v1/sources/webhook/secret` and `POST …/secret/rotate` (admin). |
| **MCP** | `reveal_source_secret`, `rotate_source_secret`; the source also appears in `list_sources`. |
| **Dashboard** | **Sources → Inbound webhook**: the ingest URL, and generate / reveal / rotate the secret. |
