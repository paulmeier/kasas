package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// This file is the user-facing configuration layer of a plugin. A plugin's
// manifest [config] block declares the configurable keys and their defaults;
// the OPERATOR overrides them in a per-plugin TOML file that lives NEXT TO the
// plugin's directory:
//
//	<plugins.dir>/<name>/             # the plugin (replaced wholesale on update)
//	<plugins.dir>/<name>.config.toml  # the operator's overrides (survives updates)
//
// The file is editable both by hand (apply with a plugin reload or restart) and
// from the plugin's own dashboard page via Host.SetConfig — a dashboard save
// OVERWRITES the file, so the file is always the single source of truth for the
// overrides. Keeping it outside the plugin directory means a marketplace
// update (an atomic directory swap) or the integrity-hashed file set can never
// collide with operator state.

// maxUserConfigBytes bounds the override file so a misbehaving plugin can't
// grow it without bound through SetConfig (and a hand-edited file of this size
// is certainly a mistake).
const maxUserConfigBytes = 64 << 10

// userConfigPath returns the plugin's override file path. name is a validated
// slug (nameRE), so the path always stays inside dir.
func userConfigPath(dir, name string) string {
	return filepath.Join(dir, name+".config.toml")
}

// loadUserOverrides reads and parses a plugin's override file. A missing file
// is simply an empty override set; a present-but-broken file is an error, so
// the plugin fails to load and the operator notices (mirroring a broken
// manifest) instead of silently running on defaults.
func loadUserOverrides(dir, name string) (map[string]any, error) {
	data, err := os.ReadFile(userConfigPath(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(userConfigPath(dir, name)), err)
	}
	if len(data) > maxUserConfigBytes {
		return nil, fmt.Errorf("%s is too large (%d bytes, max %d)", filepath.Base(userConfigPath(dir, name)), len(data), maxUserConfigBytes)
	}
	out := map[string]any{}
	if err := toml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(userConfigPath(dir, name)), err)
	}
	return out, nil
}

// mergeConfig overlays overrides onto the manifest defaults and returns the
// effective config. The [config] block is the SCHEMA of what is configurable:
// an override key with no default is an error (a typo would otherwise silently
// configure nothing), and each value is coerced to its default's scalar type so
// a string from a dashboard form ("true", "42") lands as the type the plugin
// code expects.
func mergeConfig(defaults, overrides map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(defaults))
	for k, v := range defaults {
		out[k] = v
	}
	// Deterministic key order so the first error reported is stable.
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		def, ok := defaults[k]
		if !ok {
			return nil, fmt.Errorf("unknown config key %q (configurable keys need a default in the manifest's [config] block)", k)
		}
		cv, err := coerceConfigValue(k, def, overrides[k])
		if err != nil {
			return nil, err
		}
		out[k] = cv
	}
	return out, nil
}

// coerceConfigValue converts an override value to the type of its manifest
// default. Scalar defaults (bool, number, string) are enforced strictly but
// accept string forms ("true", "3.5") because dashboard form params are always
// strings; a structured default (array/table) takes the override as-is — deep
// shapes are the plugin's own contract.
func coerceConfigValue(key string, def, v any) (any, error) {
	switch def.(type) {
	case bool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			if b, err := strconv.ParseBool(strings.TrimSpace(x)); err == nil {
				return b, nil
			}
		}
		return nil, fmt.Errorf("config key %q must be a boolean (got %T)", key, v)
	case int, int64, float64:
		switch x := v.(type) {
		case int, int64, float64:
			return x, nil
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(x), 64); err == nil {
				return f, nil
			}
		}
		return nil, fmt.Errorf("config key %q must be a number (got %T)", key, v)
	case string:
		switch x := v.(type) {
		case string:
			return x, nil
		case bool:
			return strconv.FormatBool(x), nil
		case int64:
			return strconv.FormatInt(x, 10), nil
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64), nil
		}
		return nil, fmt.Errorf("config key %q must be a string (got %T)", key, v)
	default:
		return v, nil
	}
}

// saveUserOverrides writes the override file atomically (temp file + rename on
// the same filesystem), so a crash mid-save can never leave a half-written
// config that would fail the next load.
func saveUserOverrides(dir, name string, overrides map[string]any) error {
	body, err := toml.Marshal(overrides)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	header := "# Configuration overrides for the \"" + name + "\" plugin.\n" +
		"# Saved by kasas when the plugin's dashboard page changes its settings;\n" +
		"# also editable by hand (reload the plugin to apply). Keys must exist in\n" +
		"# the plugin manifest's [config] block.\n\n"
	data := append([]byte(header), body...)
	if len(data) > maxUserConfigBytes {
		return fmt.Errorf("config too large (%d bytes, max %d)", len(data), maxUserConfigBytes)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create plugins dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+name+".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed away
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), userConfigPath(dir, name))
}

// effectiveConfig loads a plugin's override file and merges it over the
// manifest defaults. An empty dir (no plugins directory configured, e.g. some
// tests) skips the file layer entirely and returns the defaults.
func effectiveConfig(dir, name string, defaults map[string]any) (map[string]any, error) {
	if dir == "" {
		return mergeConfig(defaults, nil)
	}
	overrides, err := loadUserOverrides(dir, name)
	if err != nil {
		return nil, err
	}
	return mergeConfig(defaults, overrides)
}
