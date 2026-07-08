# Migrating from SQLite to Postgres

kasas runs on either [SQLite (the default) or Postgres](../architecture/data-model.md#multi-dialect-storage)
from the same binary. Pointing a fresh install at Postgres just creates an **empty**
schema — it does not carry an existing SQLite ledger across. When you want to move a
ledger you have already built up (from the embedded SQLite default to a shared
Postgres server, say), kasas has a built-in migration that does the whole copy for
you, available both from the command line and from the dashboard.

The migration:

- applies the kasas schema to the target Postgres database (the same embedded
  migrations `serve` runs on first start), so the destination needs to be nothing
  more than an empty, reachable database;
- copies **every** table — organizations, accounts, transactions, sync history,
  rules, the event stream, transaction history, API keys, webhooks, plugins,
  settings, and market data — **with primary keys and the event/history sequences
  preserved**, so cursors, ids, and references all still line up afterward;
- only ever **reads** the SQLite file, leaving it untouched, so you can verify
  Postgres and fall back to SQLite if anything looks off;
- runs the copy in a single transaction, so a failure part-way leaves the target's
  data empty rather than half-populated.

!!! note "One direction, one time"
    This is a one-time SQLite → Postgres move, not an ongoing sync. It refuses to
    write into a Postgres database that already holds kasas data, so it can't merge
    two ledgers or be run twice into the same target.

## Before you start

- **Have the target database ready.** Create an empty Postgres database and a
  connection string (DSN) for it, e.g.
  `postgres://user:pass@host:5432/kasas?sslmode=disable`. The database must exist;
  kasas creates the tables, not the database itself.
- **Migrate while kasas is quiet.** The copy is a point-in-time snapshot. For the
  cleanest result, run it when nothing is actively writing — pause scheduled syncs,
  or run the CLI form with the server stopped.
- **Keep your SQLite file.** It is left unchanged; keep it until you have confirmed
  Postgres is serving the data you expect.

## From the command line

Run `migrate-postgres` with your existing (SQLite) configuration, passing the target
DSN:

```sh
kasas -config config.toml migrate-postgres \
  "postgres://user:pass@host:5432/kasas?sslmode=disable"
```

It prints a per-table row count as it goes:

```text
Migration complete. Rows copied:
  organizations          1
  accounts               4
  transactions           9127
  sync_log               215
  rules                  6
  events                 18342
  transaction_versions   20890
  …
Copied 67598 rows across 13 tables.
Next: set database.driver=postgres and database.dsn (or KASAS_DATABASE_DRIVER / KASAS_DATABASE_DSN) and restart kasas.
```

The DSN can also be given with `-dsn` instead of as a positional argument. The
command refuses to run unless the active `database.driver` is `sqlite` (there has to
be a SQLite ledger to copy from).

## From the dashboard

On the SQLite backend, the **Settings** page shows a **Migrate to Postgres** panel
(admin-only — it requires a configured dashboard token). Paste the target DSN, click
**Migrate to Postgres**, and confirm. When it finishes it shows the same per-table
row counts. The request runs server-side with a long timeout, so a large ledger is
fine; leave the tab open until it reports back.

The panel disappears once kasas is running on Postgres — there is nothing left to
migrate from.

## Switch over

The migration deliberately does **not** change your configuration — so nothing
changes until you decide to cut over. When the copy looks good, point kasas at
Postgres and restart:

=== "Config file"

    ```toml
    [database]
    driver = "postgres"
    dsn    = "postgres://user:pass@host:5432/kasas?sslmode=disable"
    ```

=== "Environment"

    ```sh
    KASAS_DATABASE_DRIVER=postgres \
    KASAS_DATABASE_DSN="postgres://user:pass@host:5432/kasas?sslmode=disable" \
    kasas serve
    ```

On restart kasas re-applies its migrations against Postgres (a no-op — they were
applied during the copy) and serves from Postgres. Confirm your accounts and recent
transactions are present, then retire the old SQLite file at your leisure.

## Troubleshooting

| Message | Cause | Fix |
| --- | --- | --- |
| `destination table "…" already contains data` | The target Postgres database already holds kasas data. | Migrate into an **empty** database, or drop/recreate the target's schema first. |
| `database.driver is "postgres"` | You ran `migrate-postgres` while already configured for Postgres. | Run it with your original SQLite configuration — it copies *from* SQLite. |
| `connect to postgres: …` | The DSN is wrong or the server is unreachable. | Check the host, port, credentials, database name, and `sslmode`. |
| `database migration is only available when kasas is running on SQLite` (dashboard) | The instance is already on Postgres. | Nothing to do — you have already migrated. |
