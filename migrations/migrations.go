// Package migrations embeds the goose SQL migration files so they can be
// applied at runtime without shipping the .sql files alongside the binary.
// Migrations are split by dialect: the goose dir is "sqlite" or "postgres".
package migrations

import "embed"

// FS holds the embedded goose migration files for every supported dialect.
// Apply them with goose.Up(db, "sqlite") or goose.Up(db, "postgres").
//
//go:embed sqlite/*.sql postgres/*.sql
var FS embed.FS
