package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedExampleMatchesRepoRoot guards against the embedded copy drifting from
// the canonical repo-root config.example.toml (the one docs link to and releases
// package). go:embed cannot reach outside the package directory, so the file is
// duplicated; if you edit one, copy it over the other.
func TestEmbeddedExampleMatchesRepoRoot(t *testing.T) {
	root, err := os.ReadFile("../../config.example.toml")
	require.NoError(t, err)
	assert.Equal(t, string(root), string(ExampleConfig),
		"internal/config/config.example.toml is out of sync with the repo-root config.example.toml; run: cp config.example.toml internal/config/config.example.toml")
}

// TestLoadSeedsMissingConfigFile checks that pointing Load at a non-existent path
// seeds the annotated example there and still loads cleanly (and that seeding does
// not change the resolved defaults — the example's uncommented values are defaults).
func TestLoadSeedsMissingConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")

	cfg, err := Load(path)
	require.NoError(t, err)

	seeded, err := os.ReadFile(path)
	require.NoError(t, err, "Load should have created the config file")
	assert.Equal(t, string(ExampleConfig), string(seeded))

	// Seeding is behaviour-neutral: the resolved config still matches the defaults.
	assert.Equal(t, ":8080", cfg.Server.Addr)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "/data/kasas.db", cfg.Database.Path)
	assert.True(t, cfg.Sync.Enabled)
}

// TestLoadFallsBackWhenConfigUnseedable checks that when the config file is missing
// AND cannot be created (here its parent is a regular file, so MkdirAll fails
// deterministically), Load does not error — it uses defaults + environment.
func TestLoadFallsBackWhenConfigUnseedable(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o644))
	path := filepath.Join(parent, "config.toml") // parent is a file -> MkdirAll fails

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.Server.Addr)

	_, statErr := os.Stat(path)
	assert.Error(t, statErr, "no file should have been created under a non-directory parent")
}

// TestLoadReadsExistingConfigFile checks Load honours an existing file (and does not
// overwrite it with the example).
func TestLoadReadsExistingConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[log]\nformat = \"text\"\n"), 0o644))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "text", cfg.Log.Format)

	// The user's file is left intact, not clobbered by the seed.
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "[log]\nformat = \"text\"\n", string(contents))
}
