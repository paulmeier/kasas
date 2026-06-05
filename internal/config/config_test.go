package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, ":8080", cfg.Server.Addr)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "/data/kasas.db", cfg.Database.Path)
	assert.Equal(t, 6*time.Hour, cfg.Sync.Interval)
	assert.Equal(t, 90, cfg.Sync.LookbackDays)
	assert.True(t, cfg.Sync.Enabled)
	assert.True(t, cfg.MCP.Enabled)
	assert.True(t, cfg.Dashboard.Enabled)
	assert.False(t, cfg.Vault.Enabled)
	assert.Equal(t, "secret", cfg.Vault.Mount)
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("KASAS_SERVER_ADDR", ":9999")
	t.Setenv("KASAS_SYNC_INTERVAL", "15m")
	t.Setenv("KASAS_VAULT_ENABLED", "true")
	t.Setenv("KASAS_SIMPLEFIN_SETUP_TOKEN", "abc123")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, ":9999", cfg.Server.Addr)
	assert.Equal(t, 15*time.Minute, cfg.Sync.Interval)
	assert.True(t, cfg.Vault.Enabled)
	assert.Equal(t, "abc123", cfg.SimpleFIN.SetupToken)
}

func TestLoadPostgresDriver(t *testing.T) {
	t.Setenv("KASAS_DATABASE_DRIVER", "postgres")
	t.Setenv("KASAS_DATABASE_DSN", "postgres://u:p@localhost:5432/kasas?sslmode=disable")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "postgres", cfg.Database.Driver)
	assert.Equal(t, "postgres://u:p@localhost:5432/kasas?sslmode=disable", cfg.Database.DSN)
}

func TestLoadFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `
[server]
addr = ":7777"

[log]
level = "debug"

[sync]
interval = "2h"
lookback_days = 30
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, ":7777", cfg.Server.Addr)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, 2*time.Hour, cfg.Sync.Interval)
	assert.Equal(t, 30, cfg.Sync.LookbackDays)
}

func TestLoadEnvWinsOverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[server]\naddr = \":7777\"\n"), 0o600))
	t.Setenv("KASAS_SERVER_ADDR", ":6666")

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, ":6666", cfg.Server.Addr)
}

func TestLoadErrors(t *testing.T) {
	t.Run("invalid interval", func(t *testing.T) {
		t.Setenv("KASAS_SYNC_INTERVAL", "not-a-duration")
		_, err := Load("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sync.interval")
	})

	t.Run("invalid log format", func(t *testing.T) {
		t.Setenv("KASAS_LOG_FORMAT", "xml")
		_, err := Load("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "log.format")
	})

	t.Run("missing config file", func(t *testing.T) {
		_, err := Load("/no/such/config.toml")
		require.Error(t, err)
	})
}

func TestValidate(t *testing.T) {
	valid := func() *Config {
		return &Config{
			Server:   Server{Addr: ":8080"},
			Database: Database{Driver: "sqlite", Path: "/data/kasas.db"},
			Sync:     Sync{Interval: time.Hour},
			Log:      Log{Format: "json"},
		}
	}
	require.NoError(t, valid().validate())

	// Postgres with a DSN is also valid.
	pg := valid()
	pg.Database = Database{Driver: "postgres", DSN: "postgres://localhost/kasas"}
	require.NoError(t, pg.validate())

	tests := map[string]func(*Config){
		"empty addr":           func(c *Config) { c.Server.Addr = "" },
		"sqlite empty path":    func(c *Config) { c.Database.Path = "" },
		"postgres without dsn": func(c *Config) { c.Database = Database{Driver: "postgres"} },
		"unknown driver":       func(c *Config) { c.Database.Driver = "mysql" },
		"zero interval":        func(c *Config) { c.Sync.Interval = 0 },
		"negative interval":    func(c *Config) { c.Sync.Interval = -time.Second },
		"bad log format":       func(c *Config) { c.Log.Format = "yaml" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := valid()
			mutate(c)
			require.Error(t, c.validate())
		})
	}
}
