-- +goose Up
-- +goose StatementBegin
-- Per-plugin network grants for net:fetch (ADR 0002). A plugin declares the hosts
-- it may reach in its manifest's [net].allow list; that allowlist is code/identity
-- and lives on disk. This column is the OPERATOR's complementary decision: the
-- subset of those hosts the operator has granted PRIVATE/LAN access to, so a
-- declared host that resolves to an RFC 1918 / loopback / link-local address is
-- only reachable when the operator explicitly approved it for this plugin at enable
-- time. It is a JSON array of hostnames, mirroring granted_capabilities (the other
-- piece of per-plugin operator state), and defaults to '[]' (deny every private
-- target until granted). Public hosts on the allowlist never need an entry here.
ALTER TABLE plugins ADD COLUMN net_grants TEXT NOT NULL DEFAULT '[]';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE plugins DROP COLUMN net_grants;
-- +goose StatementEnd
