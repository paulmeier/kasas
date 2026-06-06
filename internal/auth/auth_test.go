package auth_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/auth"
	"github.com/paulmeier/kasas/internal/vault"
)

func newStore(t *testing.T) vault.SecretStore {
	t.Helper()
	return vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
}

// TestNoTokenIsUnsecured: with neither a config token nor a stored one, auth is
// off and nothing validates.
func TestNoTokenIsUnsecured(t *testing.T) {
	g, err := auth.New("", newStore(t))
	require.NoError(t, err)

	assert.False(t, g.Required())
	assert.Equal(t, auth.SourceNone, g.Source())
	assert.False(t, g.Valid("anything"))
	assert.False(t, g.Valid(""))
}

// TestConfigTokenWins: a config token is authoritative — it validates, the store
// is ignored, and managing the token from the dashboard is refused.
func TestConfigTokenWins(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.SetDashboardToken(context.Background(), "stored-token-value"))

	g, err := auth.New("config-token-value", store)
	require.NoError(t, err)

	assert.True(t, g.Required())
	assert.Equal(t, auth.SourceConfig, g.Source())
	assert.True(t, g.Valid("config-token-value"))
	assert.False(t, g.Valid("stored-token-value"))

	_, genErr := g.Generate(context.Background())
	assert.ErrorIs(t, genErr, auth.ErrConfigManaged)
	assert.ErrorIs(t, g.SetToken(context.Background(), "another-long-enough-token"), auth.ErrConfigManaged)
	assert.ErrorIs(t, g.ClearToken(context.Background()), auth.ErrConfigManaged)
}

// TestNewLoadsStoredToken: a token already in the store is picked up at startup.
func TestNewLoadsStoredToken(t *testing.T) {
	store := newStore(t)
	require.NoError(t, store.SetDashboardToken(context.Background(), "preexisting-token"))

	g, err := auth.New("", store)
	require.NoError(t, err)

	assert.True(t, g.Required())
	assert.Equal(t, auth.SourceStored, g.Source())
	assert.True(t, g.Valid("preexisting-token"))
	assert.False(t, g.Valid("wrong"))
}

// TestGenerateStoresAndPersists: generating a token enables auth, validates, and
// survives a restart (a fresh Guard over the same store sees it).
func TestGenerateStoresAndPersists(t *testing.T) {
	store := newStore(t)
	g, err := auth.New("", store)
	require.NoError(t, err)

	tok, err := g.Generate(context.Background())
	require.NoError(t, err)
	require.NotEmpty(t, tok)

	assert.True(t, g.Required())
	assert.Equal(t, auth.SourceStored, g.Source())
	assert.True(t, g.Valid(tok))

	// A new Guard over the same store loads the generated token.
	g2, err := auth.New("", store)
	require.NoError(t, err)
	assert.True(t, g2.Valid(tok))
}

// TestSetAndClearToken: a custom token can be set then revoked, returning to the
// unsecured state.
func TestSetAndClearToken(t *testing.T) {
	g, err := auth.New("", newStore(t))
	require.NoError(t, err)

	require.NoError(t, g.SetToken(context.Background(), "my-custom-long-token"))
	assert.True(t, g.Valid("my-custom-long-token"))
	assert.Equal(t, auth.SourceStored, g.Source())

	require.NoError(t, g.ClearToken(context.Background()))
	assert.False(t, g.Required())
	assert.Equal(t, auth.SourceNone, g.Source())
	assert.False(t, g.Valid("my-custom-long-token"))
}

// TestSetTokenRejectsShort: a too-short custom token is refused.
func TestSetTokenRejectsShort(t *testing.T) {
	g, err := auth.New("", newStore(t))
	require.NoError(t, err)

	err = g.SetToken(context.Background(), "short")
	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrConfigManaged)
	assert.False(t, g.Required())
}

// TestGeneratedTokensAreUnique guards against an accidental constant token.
func TestGeneratedTokensAreUnique(t *testing.T) {
	g, err := auth.New("", newStore(t))
	require.NoError(t, err)

	a, err := g.Generate(context.Background())
	require.NoError(t, err)
	b, err := g.Generate(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
}
