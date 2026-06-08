# CSV File Import

The **CSV source** ingests transactions from CSV files in a folder — a **local
directory** or a **Google Drive folder** — so you can bring in any institution
that offers a CSV/statement export, with no per-bank API. It runs *alongside*
[SimpleFIN](../architecture/ingestion.md) (and any other source); enable as many
folders as you like, one per account.

Source: [`internal/sources/csv`](https://github.com/paulmeier/kasas/tree/main/internal/sources/csv).

## How it works

CSV is a [`file` archetype](../architecture/ingestion.md#archetypes-not-providers)
source, but it is triggered like a pull: on each [sync](sync.md) the engine asks it
to fetch, and it **scans every configured folder**, parses the CSV files it finds,
maps each row to a transaction, and returns them as one neutral batch the engine
persists. There is nothing to upload — drop (or sync) files into the folder and the
next sync picks them up.

- **Idempotent re-import.** Each row is given a deterministic **content-hash id**
  (`csv:<hash of account + date + amount + description + payee + memo>`). Files are
  re-scanned in full every sync, but a row that was already imported collides on
  its id and is skipped — so re-running, overlapping exports, and re-downloaded
  Drive files never double-count.
- **One folder → one account.** Every row in a folder belongs to the account you
  name for that folder (id `csv:<slug>`), with the institution and currency you
  set. Use several folders for several accounts.
- **Unmapped rows are skipped, not fatal.** A row with an unparseable date or
  amount is counted and skipped; a single bad file is logged and skipped. The rest
  still import.

!!! info "Quiet until configured"
    A source with no folders simply isn't started, and a source that isn't
    connected yet (e.g. Google Drive without a token) is **skipped on sync, not
    errored** — so a CSV-only setup never logs SimpleFIN failures, and vice versa.

## Configuration

Add a `[csv]` block to your [config file](../getting-started/configuration.md) with
one `[[csv.folders]]` entry per account:

```toml
[[csv.folders]]
name = "Chase Checking"        # display name; also the basis for the account id
backend = "local"              # "local" (default) or "gdrive"
path = "/data/imports/chase"   # local backend: the directory to scan
account = "Chase Checking"     # the account these rows belong to
org = "Chase"                  # institution name (optional; defaults to the account)
currency = "USD"               # ISO code (optional; defaults to USD)

# Column mapping is optional — omitted columns are auto-detected from the header.
[csv.folders.mapping]
date_column = "Transaction Date"
amount_column = "Amount"
description_column = "Description"
date_format = "01/02/2006"     # Go layout; omit to try common formats
```

### Column mapping

Every mapping field is optional. When omitted, the column is **auto-detected** from
common header names (`date`, `amount`, `debit`/`credit`, `description`, `payee`,
`memo`, and aliases like `Transaction Date`, `Withdrawal`, `Deposit`, `Details`).
Set a field explicitly when auto-detection can't tell:

| Key | Purpose |
| --- | --- |
| `has_header` | `true` (default) treats the first row as a header. Set `false` and map by index. |
| `delimiter` | Field separator (default `,`). |
| `date_column` | The date column, by **header name** or **0-based index**. |
| `date_format` | A [Go time layout](https://pkg.go.dev/time#pkg-constants) (e.g. `02/01/2006`). Omit to try common formats (US month-first for slash/dash dates). |
| `amount_column` | A single **signed** amount column (negative = outflow). |
| `debit_column` / `credit_column` | Use *instead of* `amount_column` when outflows and inflows are separate columns; the amount becomes `credit − debit`. |
| `description_column`, `payee_column`, `memo_column` | The text fields. |

!!! warning "Amount format"
    Amounts are preserved as text (no float round-trip) and assume `.` as the
    decimal separator (US-style); `$`, thousands separators, `+`, and surrounding
    parentheses (treated as negative) are stripped. Two genuinely identical rows in
    one statement (same date, amount, and text) collapse to one, since they hash
    alike — split them across files or add a distinguishing memo if that matters.

## Google Drive

The Google Drive backend lists and downloads CSVs from a Drive folder over the
Drive REST API, refreshing access tokens automatically. It uses an **OAuth
user-token** flow (it reaches your own *My Drive*) and adds **no Google SDK
dependency**.

**1. Create an OAuth client.** In the
[Google Cloud Console](https://console.cloud.google.com/apis/credentials), enable
the Drive API and create an **OAuth 2.0 Client ID** (type *Web application*). Add an
authorized redirect URI pointing at your kasas instance:

```
https://<your-kasas-host>/api/v1/sources/csv/oauth/callback
```

**2. Configure kasas.** Put the client credentials and that exact redirect URL in
`[csv]` (the client id/secret may also come from the environment —
`KASAS_CSV_GDRIVE_CLIENT_ID`, `KASAS_CSV_GDRIVE_CLIENT_SECRET`), and add a `gdrive`
folder:

```toml
[csv]
gdrive_client_id = "…apps.googleusercontent.com"
gdrive_client_secret = "…"
gdrive_redirect_url = "https://your-kasas-host/api/v1/sources/csv/oauth/callback"

[[csv.folders]]
name = "Amex (Drive)"
backend = "gdrive"
folder_id = "1AbCdEf…"          # the Drive folder id (from its URL)
account = "Amex Gold"
currency = "USD"
```

**3. Connect.** Open the dashboard **Sources** page and click **Connect CSV files**;
authorize kasas in Google, and it stores a refresh token (one Google account covers
all your Drive folders). No restart needed.

!!! tip "Headless / paste alternative"
    If you'd rather not run the browser flow, obtain a refresh token yourself and
    paste it into the source's credential field on the Sources page (or
    `PUT /api/v1/sources/csv/credential`). The redirect URL is only needed for the
    browser flow.

## Managing it

CSV is a first-class source, so it appears everywhere sources do:

- **Dashboard → Sources** — connection status, **Sync now**, the Drive connect
  button, and (for Drive) the paste-a-token fallback.
- **REST** — `GET /api/v1/sources`, `POST /api/v1/sources/csv/sync`,
  `PUT /api/v1/sources/csv/credential`, and the OAuth `…/oauth/start` +
  `…/oauth/callback` endpoints.
- **MCP** — `list_sources` and `sync_source` (alongside `trigger_sync`, which syncs
  every source).

## Limitations

- Files are **re-scanned in full** each sync (cheap at self-hoster scale);
  incremental file-skipping is a future optimization.
- One **Google account** per instance (one refresh token), shared by all `gdrive`
  folders.
- Folder profiles are **config-file** defined today (editing them needs a restart);
  a dashboard editor is future work.

## Where to go next

- [Ingestion & Sources](../architecture/ingestion.md) — the source/engine contract
  CSV plugs into.
- [Rules Engine](rules.md) — auto-label and enrich imported rows.
- [Transaction Provenance](transaction-provenance.md) — the `source` stamp CSV
  writes (`csv`).
