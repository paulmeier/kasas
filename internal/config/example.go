package config

import (
	_ "embed"
	"os"
	"path/filepath"
)

// ExampleConfig is the annotated example configuration, embedded so the binary can
// seed it on first run (see ensureConfigFile). It is a byte-identical copy of the
// repo-root config.example.toml — a copy because go:embed cannot reach a file
// outside the package directory; the test TestEmbeddedExampleMatchesRepoRoot fails
// if the two drift apart.
//
//go:embed config.example.toml
var ExampleConfig []byte

// ensureConfigFile makes sure the config file at path exists, seeding it with the
// annotated example on first run so a fresh deployment has a real file to edit at a
// known location. The Docker image points KASAS_CONFIG at /data/config.toml (the
// persisted volume), so this turns "no config file anywhere" into "an editable,
// fully documented config.toml in your app-data directory".
//
// Seeding never changes behaviour: the example's uncommented values are the built-in
// defaults, and environment variables still win over the file — so on an env-driven
// deployment the seeded file is purely documentation. It returns the path to read,
// or "" when the file is absent and cannot be created (a read-only data dir, say),
// in which case Load falls back to defaults + environment rather than failing.
func ensureConfigFile(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path // already exists; read it (a parse error surfaces in ReadInConfig)
	}
	// Missing (or unreachable): seed the annotated example so there is a real file to
	// edit. Best-effort — fall back to defaults + environment if it cannot be created.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return ""
		}
	}
	if err := os.WriteFile(path, ExampleConfig, 0o644); err != nil {
		return ""
	}
	return path
}
