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
	assert.Empty(t, cfg.Dashboard.Token)
	assert.False(t, cfg.Vault.Enabled)
	assert.Equal(t, "secret", cfg.Vault.Mount)
	assert.True(t, cfg.Events.Enabled)
	assert.Equal(t, 0, cfg.Events.RetentionDays, "keep events forever by default")
	assert.Equal(t, 0, cfg.Events.HistoryRetentionDays, "keep transaction history forever by default")
}

func TestLoadEventsConfig(t *testing.T) {
	t.Setenv("KASAS_EVENTS_ENABLED", "false")
	t.Setenv("KASAS_EVENTS_RETENTION_DAYS", "30")
	t.Setenv("KASAS_EVENTS_HISTORY_RETENTION_DAYS", "365")

	cfg, err := Load("")
	require.NoError(t, err)
	assert.False(t, cfg.Events.Enabled)
	assert.Equal(t, 30, cfg.Events.RetentionDays)
	assert.Equal(t, 365, cfg.Events.HistoryRetentionDays)
}

func TestNegativeRetentionRejected(t *testing.T) {
	t.Setenv("KASAS_EVENTS_RETENTION_DAYS", "-1")
	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events.retention_days")
}

func TestNegativeHistoryRetentionRejected(t *testing.T) {
	t.Setenv("KASAS_EVENTS_HISTORY_RETENTION_DAYS", "-1")
	_, err := Load("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "events.history_retention_days")
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("KASAS_SERVER_ADDR", ":9999")
	t.Setenv("KASAS_SYNC_INTERVAL", "15m")
	t.Setenv("KASAS_VAULT_ENABLED", "true")
	t.Setenv("KASAS_SIMPLEFIN_SETUP_TOKEN", "abc123")
	t.Setenv("KASAS_DASHBOARD_TOKEN", "s3cret-dashboard-token")

	cfg, err := Load("")
	require.NoError(t, err)

	assert.Equal(t, ":9999", cfg.Server.Addr)
	assert.Equal(t, 15*time.Minute, cfg.Sync.Interval)
	assert.True(t, cfg.Vault.Enabled)
	assert.Equal(t, "abc123", cfg.SimpleFIN.SetupToken)
	assert.Equal(t, "s3cret-dashboard-token", cfg.Dashboard.Token)
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

func TestLoadTellerTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `
[teller]
access_token = "tok_single"
access_tokens = ["tok_a", "tok_b"]
certificate = "/data/teller/cert.pem"
private_key = "/data/teller/key.pem"
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "tok_single", cfg.Teller.AccessToken, "the singular env-friendly token")
	assert.Equal(t, []string{"tok_a", "tok_b"}, cfg.Teller.AccessTokens, "the config-file token array")
	assert.Equal(t, "/data/teller/cert.pem", cfg.Teller.Certificate)
	assert.Equal(t, "/data/teller/key.pem", cfg.Teller.PrivateKey)
}

func TestLoadPlaidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `
[plaid]
client_id = "cid_123"
secret = "sec_abc"
environment = "production"
country_codes = ["US", "CA"]
access_token = "access-prod-single"
access_tokens = ["access-prod-a", "access-prod-b"]
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "cid_123", cfg.Plaid.ClientID)
	assert.Equal(t, "sec_abc", cfg.Plaid.Secret)
	assert.Equal(t, "production", cfg.Plaid.Environment)
	assert.Equal(t, []string{"US", "CA"}, cfg.Plaid.CountryCodes)
	assert.Equal(t, "access-prod-single", cfg.Plaid.AccessToken, "the singular env-friendly token")
	assert.Equal(t, []string{"access-prod-a", "access-prod-b"}, cfg.Plaid.AccessTokens, "the config-file token array")
}

func TestPlaidEnvironmentDefaultsToSandbox(t *testing.T) {
	cfg, err := Load("")
	require.NoError(t, err)
	assert.Equal(t, "sandbox", cfg.Plaid.Environment)
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
