-- name: UpsertOrganization :exec
INSERT INTO organizations (id, domain, name, sfin_url)
VALUES (sqlc.arg(id), sqlc.arg(domain), sqlc.arg(name), sqlc.arg(sfin_url))
ON CONFLICT (id) DO UPDATE SET
    domain   = excluded.domain,
    name     = excluded.name,
    sfin_url = excluded.sfin_url;

-- name: ListOrganizations :many
SELECT * FROM organizations
ORDER BY name;

-- name: GetOrganization :one
SELECT * FROM organizations
WHERE id = sqlc.arg(id);
