package settings_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/settings"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
)

func defaultConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("")
	require.NoError(t, err)
	return cfg
}

func newService(t *testing.T) (*settings.Service, vault.SecretStore) {
	t.Helper()
	store := testutil.NewStore(t)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	base := defaultConfig(t)
	boot := settings.Clone(base)
	return settings.NewService(store, secrets, base, boot), secrets
}

func TestApplyOverrides(t *testing.T) {
	cfg := defaultConfig(t)
	require.False(t, cfg.Plugins.Enabled)

	err := settings.Apply(cfg, map[string]string{
		"plugins.enabled":    "true",
		"sync.interval":      "1h",
		"not.a.setting":      "ignored", // unknown keys are carried, never fatal
		"sync.lookback_days": "soup",    // unparseable values are skipped, never fatal
	}, nil)
	require.NoError(t, err)
	assert.True(t, cfg.Plugins.Enabled)
	assert.Equal(t, "1h0m0s", cfg.Sync.Interval.String())
	assert.Equal(t, 90, cfg.Sync.LookbackDays, "bad override left the file/env value in place")
}

func TestApplyRevertsOnInvalidCombination(t *testing.T) {
	cfg := defaultConfig(t)
	// webhooks.max_attempts=0 parses fine but fails Validate (webhooks enabled).
	err := settings.Apply(cfg, map[string]string{
		"webhooks.max_attempts": "0",
		"plugins.enabled":       "true",
	}, nil)
	require.Error(t, err)
	assert.Equal(t, 5, cfg.Webhooks.MaxAttempts, "config restored untouched")
	assert.False(t, cfg.Plugins.Enabled, "config restored untouched")
}

func TestServiceSetPersistsAndFlagsRestart(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	st, restart, err := svc.Set(ctx, "plugins.enabled", "true")
	require.NoError(t, err)
	assert.Equal(t, "true", st.Value)
	assert.True(t, st.Overridden)
	assert.True(t, st.RestartRequired, "running process was built with plugins disabled")
	assert.True(t, restart)

	// Durations normalize to their canonical form before storage.
	st, _, err = svc.Set(ctx, "sync.interval", "90m")
	require.NoError(t, err)
	assert.Equal(t, "1h30m0s", st.Value)

	all, restartAll, err := svc.List(ctx)
	require.NoError(t, err)
	assert.True(t, restartAll)
	byKey := map[string]settings.Status{}
	for _, s := range all {
		byKey[s.Key] = s
	}
	assert.True(t, byKey["plugins.enabled"].Overridden)
	assert.False(t, byKey["mcp.enabled"].Overridden)
	assert.False(t, byKey["mcp.enabled"].RestartRequired)
}

func TestServiceRejectsBadValues(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()

	_, _, err := svc.Set(ctx, "no.such.key", "x")
	assert.ErrorIs(t, err, settings.ErrUnknownKey)

	_, _, err = svc.Set(ctx, "plugins.enabled", "maybe")
	assert.Error(t, err)

	// Parses, but the combined config fails validation: nothing is stored.
	_, _, err = svc.Set(ctx, "webhooks.max_attempts", "0")
	assert.Error(t, err)
	all, restart, err := svc.List(ctx)
	require.NoError(t, err)
	assert.False(t, restart)
	for _, s := range all {
		assert.False(t, s.Overridden, "key %s should have no override", s.Key)
	}

	_, _, err = svc.Set(ctx, "log.level", "loud")
	assert.Error(t, err, "enum values are enforced")

	_, _, err = svc.Set(ctx, "csv.folders", `[{"name":"a","backend":"weird","path":"/x"}]`)
	assert.Error(t, err, "folder profiles run through the CSV source's validation")

	_, _, err = svc.Set(ctx, "csv.folders", `[{"name":"a","backend":"local","path":"/x"}]`)
	assert.NoError(t, err)
}

func TestServiceSecretsNeverEcho(t *testing.T) {
	svc, secrets := newService(t)
	ctx := context.Background()

	st, _, err := svc.Set(ctx, "plaid.secret", "super-secret")
	require.NoError(t, err)
	assert.Empty(t, st.Value, "secret values are never echoed")
	assert.True(t, st.Set)
	assert.True(t, st.Overridden)

	v, err := secrets.SecretValue(ctx, "setting.plaid.secret")
	require.NoError(t, err)
	assert.Equal(t, "super-secret", v, "secret stored in the secret store, not the DB")

	st, _, err = svc.Reset(ctx, "plaid.secret")
	require.NoError(t, err)
	assert.False(t, st.Set)
	assert.False(t, st.Overridden)
	v, err = secrets.SecretValue(ctx, "setting.plaid.secret")
	require.NoError(t, err)
	assert.Empty(t, v)
}

func TestServiceResetClearsRestartAfterReboot(t *testing.T) {
	store := testutil.NewStore(t)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	base := defaultConfig(t)
	boot := settings.Clone(base)
	svc := settings.NewService(store, secrets, base, boot)
	ctx := context.Background()

	_, restart, err := svc.Set(ctx, "plugins.enabled", "true")
	require.NoError(t, err)
	assert.True(t, restart)

	// Simulate the restart: boot config now includes the override.
	overrides, err := settings.LoadOverrides(ctx, store, secrets)
	require.NoError(t, err)
	booted := settings.Clone(base)
	require.NoError(t, settings.Apply(booted, overrides, nil))
	svc = settings.NewService(store, secrets, base, booted)

	all, restartAll, err := svc.List(ctx)
	require.NoError(t, err)
	assert.False(t, restartAll, "after a restart nothing is pending")
	for _, s := range all {
		if s.Key == "plugins.enabled" {
			assert.True(t, s.Overridden)
			assert.False(t, s.RestartRequired)
		}
	}

	// Resetting flips restart-required back on: next boot reverts to base.
	st, restart, err := svc.Reset(ctx, "plugins.enabled")
	require.NoError(t, err)
	assert.False(t, st.Overridden)
	assert.Equal(t, "false", st.Value)
	assert.True(t, st.RestartRequired)
	assert.True(t, restart)
}
