package vault_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/vault"
)

func TestFileStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store := vault.NewFileStore(path)
	ctx := context.Background()

	// Missing file reads as empty, not an error.
	got, err := store.AccessURL(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)

	const url = "https://user:pass@bridge.simplefin.org/simplefin"
	require.NoError(t, store.SetAccessURL(ctx, url))

	got, err = store.AccessURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, url, got)
}

func TestFileStoreDashboardToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store := vault.NewFileStore(path)
	ctx := context.Background()

	// Missing file reads as empty, not an error.
	got, err := store.DashboardToken(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)

	require.NoError(t, store.SetDashboardToken(ctx, "tok-123"))
	got, err = store.DashboardToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "tok-123", got)

	// Empty clears it.
	require.NoError(t, store.SetDashboardToken(ctx, ""))
	got, err = store.DashboardToken(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestFileStoreSecretsCoexist verifies the access URL and dashboard token are
// stored side by side: setting one never clobbers the other.
func TestFileStoreSecretsCoexist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store := vault.NewFileStore(path)
	ctx := context.Background()

	const url = "https://user:pass@bridge.simplefin.org/simplefin"
	require.NoError(t, store.SetAccessURL(ctx, url))
	require.NoError(t, store.SetDashboardToken(ctx, "dash-tok"))

	gotURL, err := store.AccessURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, url, gotURL)

	gotTok, err := store.DashboardToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "dash-tok", gotTok)

	// Rewriting the token leaves the access URL intact, and vice versa.
	require.NoError(t, store.SetDashboardToken(ctx, "dash-tok-2"))
	gotURL, err = store.AccessURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, url, gotURL)

	require.NoError(t, store.SetAccessURL(ctx, "https://second"))
	gotTok, err = store.DashboardToken(ctx)
	require.NoError(t, err)
	assert.Equal(t, "dash-tok-2", gotTok)
}

func TestFileStoreCreatesDirAndIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "secrets.json")
	store := vault.NewFileStore(path)

	require.NoError(t, store.SetAccessURL(context.Background(), "https://x"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	// The access URL embeds credentials, so the file must be owner-only.
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestFileStoreOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	store := vault.NewFileStore(path)
	ctx := context.Background()

	require.NoError(t, store.SetAccessURL(ctx, "https://first"))
	require.NoError(t, store.SetAccessURL(ctx, "https://second"))

	got, err := store.AccessURL(ctx)
	require.NoError(t, err)
	assert.Equal(t, "https://second", got)
}

func TestFileStoreCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))

	_, err := vault.NewFileStore(path).AccessURL(context.Background())
	require.Error(t, err)
}

func TestNewSelectsStore(t *testing.T) {
	t.Run("file store when vault disabled", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "secrets.json")
		store, err := vault.New(vault.Config{Enabled: false}, path)
		require.NoError(t, err)
		_, ok := store.(*vault.FileStore)
		assert.True(t, ok, "expected a *FileStore when Vault is disabled")

		// And it is functional.
		require.NoError(t, store.SetAccessURL(context.Background(), "https://x"))
		got, err := store.AccessURL(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "https://x", got)
	})

	t.Run("vault store when enabled", func(t *testing.T) {
		// Constructing the client does not connect, so this succeeds offline.
		store, err := vault.New(vault.Config{
			Enabled: true,
			Address: "http://127.0.0.1:8200",
			Token:   "test-token",
			Mount:   "secret",
			Path:    "kasas",
		}, "")
		require.NoError(t, err)
		require.NotNil(t, store)
		_, isFile := store.(*vault.FileStore)
		assert.False(t, isFile, "expected a Vault-backed store when enabled")
	})
}
