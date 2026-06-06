-- +goose Up
-- +goose StatementBegin
-- A plugin is third-party code kasas loads from the plugins directory and runs in
-- a sandboxed language runtime, reacting to committed events off the bus (the
-- async, post-commit counterpart to the rules engine and webhooks). The plugin's
-- code and manifest live on disk; this table is the durable CONTROL PLANE that has
-- to survive restarts: whether the plugin is enabled, the capabilities the operator
-- has approved, operator config overrides, and per-plugin run health.
--
-- `name` is the stable identity (the plugin directory name) and is unique.
-- `runtime` and `version` mirror the manifest (refreshed on each discovery).
-- `enabled` gates execution and defaults to 0 — running third-party code is opt-in
-- per plugin. `granted_capabilities` is a JSON array of approved capability strings
-- (the manifest only *requests* capabilities; this is what was actually granted, so
-- the future marketplace flow can approve a subset). `config` is a JSON object of
-- operator overrides merged over the manifest's [config]. The last_* columns record
-- run health without a per-invocation table, mirroring webhooks: `last_status` is a
-- coarse code (0 = never run, 1 = ok, 2 = error), `last_error` the most recent
-- failure message, and last_run_at/last_success_at are unix seconds (0 = never).
CREATE TABLE plugins (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    name                 TEXT NOT NULL,
    runtime              TEXT NOT NULL DEFAULT '',
    version              TEXT NOT NULL DEFAULT '',
    enabled              INTEGER NOT NULL DEFAULT 0,
    granted_capabilities TEXT NOT NULL DEFAULT '[]',
    config               TEXT NOT NULL DEFAULT '{}',
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    last_status          INTEGER NOT NULL DEFAULT 0,
    last_error           TEXT NOT NULL DEFAULT '',
    last_run_at          INTEGER NOT NULL DEFAULT 0,
    last_success_at      INTEGER NOT NULL DEFAULT 0
) STRICT;
-- +goose StatementEnd

-- +goose StatementBegin
-- name is the lookup key during discovery/reconcile and must be unique.
CREATE UNIQUE INDEX idx_plugins_name ON plugins (name);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE plugins;
-- +goose StatementEnd
