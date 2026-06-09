package plugins

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeConfigOverlaysAndCoerces(t *testing.T) {
	defaults := map[string]any{
		"keyword": "coffee",
		"limit":   int64(10),
		"ratio":   0.5,
		"enabled": false,
		"tags":    []any{"a"},
	}

	out, err := mergeConfig(defaults, map[string]any{
		"keyword": "tea",
		"limit":   "25",   // string forms coerce to the default's type
		"enabled": "true", // (dashboard form params are always strings)
		"ratio":   1.5,
	})
	require.NoError(t, err)
	assert.Equal(t, "tea", out["keyword"])
	assert.EqualValues(t, 25, out["limit"], "whole string numbers coerce to integers")
	assert.Equal(t, true, out["enabled"])
	assert.Equal(t, 1.5, out["ratio"])
	assert.Equal(t, []any{"a"}, out["tags"], "untouched keys keep their defaults")
	assert.Equal(t, "coffee", defaults["keyword"], "defaults map is never mutated")
}

func TestMergeConfigRejectsBadOverrides(t *testing.T) {
	defaults := map[string]any{"keyword": "coffee", "limit": int64(10), "enabled": false}
	cases := map[string]map[string]any{
		"unknown key":    {"keyowrd": "tea"},
		"not a number":   {"limit": "lots"},
		"not a boolean":  {"enabled": "yep"},
		"object for str": {"keyword": map[string]any{"x": 1}},
	}
	for name, overrides := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := mergeConfig(defaults, overrides)
			assert.Error(t, err)
		})
	}
}

func TestMergeConfigStringifiesScalarsForStringDefaults(t *testing.T) {
	out, err := mergeConfig(map[string]any{"threshold": "200.00"}, map[string]any{"threshold": int64(300)})
	require.NoError(t, err)
	assert.Equal(t, "300", out["threshold"])
}

func TestUserOverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// No file yet: an empty override set, and effectiveConfig returns defaults.
	overrides, err := loadUserOverrides(dir, "budgeting")
	require.NoError(t, err)
	assert.Empty(t, overrides)

	require.NoError(t, saveUserOverrides(dir, "budgeting", map[string]any{"keyword": "tea", "limit": float64(25)}))
	overrides, err = loadUserOverrides(dir, "budgeting")
	require.NoError(t, err)
	assert.Equal(t, "tea", overrides["keyword"])

	eff, err := effectiveConfig(dir, "budgeting", map[string]any{"keyword": "coffee", "limit": int64(10), "enabled": false})
	require.NoError(t, err)
	assert.Equal(t, "tea", eff["keyword"])
	assert.Equal(t, false, eff["enabled"])

	data, err := os.ReadFile(userConfigPath(dir, "budgeting"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "# Configuration overrides", "the saved file carries its explanatory header")
}

func TestEffectiveConfigBrokenFileErrors(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(userConfigPath(dir, "budgeting"), []byte("keyword = "), 0o644))
	_, err := effectiveConfig(dir, "budgeting", map[string]any{"keyword": "coffee"})
	assert.Error(t, err, "a present-but-broken override file must fail the load, not silently use defaults")
}

func TestEffectiveConfigNoDirSkipsFileLayer(t *testing.T) {
	eff, err := effectiveConfig("", "budgeting", map[string]any{"keyword": "coffee"})
	require.NoError(t, err)
	assert.Equal(t, "coffee", eff["keyword"])
}

func TestUserConfigPathStaysInPluginsDir(t *testing.T) {
	assert.Equal(t, filepath.Join("/data/plugins", "budgeting.config.toml"), userConfigPath("/data/plugins", "budgeting"))
}
