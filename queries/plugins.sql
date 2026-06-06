-- name: InsertPlugin :one
-- Registers a newly-discovered plugin. Discovery seeds granted_capabilities from
-- the manifest's requested set; enabled defaults to 0 (running third-party code is
-- opt-in). RETURNING * yields the generated id and timestamps.
INSERT INTO plugins (name, runtime, version, enabled, granted_capabilities, config, created_at, updated_at)
VALUES (
    sqlc.arg(name), sqlc.arg(runtime), sqlc.arg(version), sqlc.arg(enabled),
    sqlc.arg(granted_capabilities), sqlc.arg(config), sqlc.arg(created_at), sqlc.arg(updated_at)
)
RETURNING *;

-- name: GetPlugin :one
SELECT * FROM plugins WHERE id = sqlc.arg(id);

-- name: GetPluginByName :one
SELECT * FROM plugins WHERE name = sqlc.arg(name);

-- name: ListPlugins :many
SELECT * FROM plugins ORDER BY name;

-- name: ListEnabledPlugins :many
SELECT * FROM plugins WHERE enabled = 1 ORDER BY name;

-- name: SetPluginEnabled :execrows
-- Toggles execution. :execrows lets the caller detect a missing id.
UPDATE plugins SET enabled = sqlc.arg(enabled), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: UpdatePluginManifest :execrows
-- Refreshes the manifest-derived fields on re-discovery WITHOUT touching operator
-- state (enabled, granted_capabilities, config).
UPDATE plugins
SET runtime = sqlc.arg(runtime), version = sqlc.arg(version), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdatePluginGrantedCapabilities :execrows
-- Sets the approved capability grant (the marketplace approval flow writes this).
UPDATE plugins
SET granted_capabilities = sqlc.arg(granted_capabilities), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id);

-- name: UpdatePluginConfig :execrows
-- Sets the operator config overrides (a JSON object merged over the manifest's).
UPDATE plugins SET config = sqlc.arg(config), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: UpdatePluginRunStatus :exec
-- Records the outcome of the most recent hook invocation on the plugin row (the
-- lean alternative to a per-invocation table). last_success_at is only advanced on
-- a successful run; the caller passes the existing value otherwise.
UPDATE plugins
SET last_status     = sqlc.arg(last_status),
    last_error      = sqlc.arg(last_error),
    last_run_at     = sqlc.arg(last_run_at),
    last_success_at = sqlc.arg(last_success_at)
WHERE id = sqlc.arg(id);

-- name: DeletePlugin :execrows
DELETE FROM plugins WHERE id = sqlc.arg(id);
