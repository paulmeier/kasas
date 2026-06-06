// Package config loads kasas configuration from a TOML file and the
// environment. Environment variables take precedence and are prefixed with
// KASAS_, with nested keys joined by underscores (e.g. KASAS_SERVER_ADDR,
// KASAS_SIMPLEFIN_ACCESS_URL).
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the fully resolved application configuration.
type Config struct {
	Server    Server
	Log       Log
	Database  Database
	SimpleFIN SimpleFIN
	Sync      Sync
	Vault     Vault
	Secrets   Secrets
	MCP       MCP
	Dashboard Dashboard
	Update    Update
	Events    Events
	Webhooks  Webhooks
}

// Server holds HTTP server settings.
type Server struct {
	Addr string
}

// Log holds logging settings.
type Log struct {
	Level  string // debug | info | warn | error
	Format string // json | text
}

// Database selects and configures the storage backend.
type Database struct {
	Driver string // "sqlite" (default) or "postgres"
	Path   string // SQLite database file path (driver=sqlite)
	DSN    string // Postgres connection string (driver=postgres), e.g.
	// postgres://user:pass@host:5432/kasas?sslmode=disable
}

// SimpleFIN holds credentials for talking to a SimpleFIN bridge. Provide
// either a one-time SetupToken (claimed for an access URL on first run) or a
// fully formed AccessURL. The resolved access URL is persisted to the secret
// store so the setup token is only used once.
type SimpleFIN struct {
	SetupToken string
	AccessURL  string
}

// Sync controls the background polling schedule.
type Sync struct {
	Enabled      bool
	Interval     time.Duration
	LookbackDays int  // how far back to fetch transactions; 0 means all available
	RunOnStart   bool // trigger a sync immediately on startup
}

// Vault configures an optional HashiCorp Vault KV v2 backend for the SimpleFIN
// access URL. When disabled, the local Secrets file is used instead.
type Vault struct {
	Enabled      bool
	Address      string
	Token        string
	Mount        string
	Path         string
	AccessURLKey string
}

// Secrets configures the local file fallback used when Vault is disabled.
type Secrets struct {
	File string
}

// MCP toggles the built-in Model Context Protocol server.
type MCP struct {
	Enabled bool
}

// Dashboard toggles the built-in read-only web dashboard (served at /) and holds
// the optional access token. When Token is non-empty, the REST API, dashboard,
// and MCP-over-HTTP server require it (sent as "Authorization: Bearer <token>").
// A config/env token is authoritative; when it is empty, a token generated from
// the Settings page (stored in the secret store) is used instead. When no token
// is set anywhere, those surfaces are unauthenticated and kasas logs a warning.
type Dashboard struct {
	Enabled bool
	Token   string
}

// Events controls the canonical event stream (REST /api/v1/events, the SSE
// /api/v1/events/stream tail, and the list_events MCP tool). When Enabled (the
// default) every meaningful change is recorded as an immutable, append-only event.
// RetentionDays, when > 0, prunes events older than that many days on a periodic
// schedule; 0 (the default) keeps them forever so the stream stays fully
// replayable.
//
// HistoryRetentionDays prunes the immutable transaction history
// (transaction_versions, exposed at /api/v1/transactions/{id}/history and the
// get_transaction_history MCP tool) on the same schedule. History recording rides
// on Enabled, but its retention is independent of RetentionDays: history is meant
// to be kept far longer than the noisy event log, so 0 (the default) keeps every
// transaction's full history forever.
type Events struct {
	Enabled              bool
	RetentionDays        int
	HistoryRetentionDays int
}

// Webhooks controls outbound event delivery: kasas POSTs each matching event to
// the HTTP endpoints registered via the REST/MCP/dashboard webhook APIs, HMAC-signed.
// Enabled (the default) starts the delivery dispatcher; it is effective only when
// events.enabled is also true, since webhooks consume the in-process event bus.
// Timeout bounds each delivery attempt and MaxAttempts caps the retries before a
// delivery is abandoned (consumers reconcile any gap via the /events cursor).
type Webhooks struct {
	Enabled     bool
	Timeout     time.Duration
	MaxAttempts int
}

// Update controls the periodic check for newer releases. It only logs a notice
// when a newer version is published; upgrading is done explicitly via the
// `kasas self-update` command. Docker deployments should disable it and update
// by pulling a new image instead.
type Update struct {
	Check      bool   // periodically check GitHub for a newer release
	AllowApply bool   // allow the dashboard/API to trigger an in-place self-update
	Repository string // "owner/name" GitHub repo to check
}

// Load reads configuration from the given file (optional) and the environment.
// A missing config file is not an error; defaults and env vars are used.
func Load(path string) (*Config, error) {
	v := viper.New()

	v.SetDefault("server.addr", ":8080")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.path", "/data/kasas.db")
	v.SetDefault("database.dsn", "")
	v.SetDefault("simplefin.setup_token", "")
	v.SetDefault("simplefin.access_url", "")
	v.SetDefault("sync.enabled", true)
	v.SetDefault("sync.interval", "6h")
	v.SetDefault("sync.lookback_days", 90)
	v.SetDefault("sync.run_on_start", true)
	v.SetDefault("vault.enabled", false)
	v.SetDefault("vault.address", "")
	v.SetDefault("vault.token", "")
	v.SetDefault("vault.mount", "secret")
	v.SetDefault("vault.path", "kasas")
	v.SetDefault("vault.access_url_key", "simplefin_access_url")
	v.SetDefault("secrets.file", "/data/secrets.json")
	v.SetDefault("mcp.enabled", true)
	v.SetDefault("dashboard.enabled", true)
	v.SetDefault("dashboard.token", "")
	v.SetDefault("update.check", true)
	v.SetDefault("update.allow_apply", true)
	v.SetDefault("update.repository", "paulmeier/kasas")
	v.SetDefault("events.enabled", true)
	v.SetDefault("events.retention_days", 0)
	v.SetDefault("events.history_retention_days", 0)
	v.SetDefault("webhooks.enabled", true)
	v.SetDefault("webhooks.timeout", "10s")
	v.SetDefault("webhooks.max_attempts", 5)

	v.SetEnvPrefix("KASAS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("read config %q: %w", path, err)
		}
	}

	interval, err := time.ParseDuration(v.GetString("sync.interval"))
	if err != nil {
		return nil, fmt.Errorf("invalid sync.interval %q: %w", v.GetString("sync.interval"), err)
	}

	webhookTimeout, err := time.ParseDuration(v.GetString("webhooks.timeout"))
	if err != nil {
		return nil, fmt.Errorf("invalid webhooks.timeout %q: %w", v.GetString("webhooks.timeout"), err)
	}

	cfg := &Config{
		Server: Server{Addr: v.GetString("server.addr")},
		Log:    Log{Level: v.GetString("log.level"), Format: v.GetString("log.format")},
		Database: Database{
			Driver: v.GetString("database.driver"),
			Path:   v.GetString("database.path"),
			DSN:    v.GetString("database.dsn"),
		},
		SimpleFIN: SimpleFIN{
			SetupToken: v.GetString("simplefin.setup_token"),
			AccessURL:  v.GetString("simplefin.access_url"),
		},
		Sync: Sync{
			Enabled:      v.GetBool("sync.enabled"),
			Interval:     interval,
			LookbackDays: v.GetInt("sync.lookback_days"),
			RunOnStart:   v.GetBool("sync.run_on_start"),
		},
		Vault: Vault{
			Enabled:      v.GetBool("vault.enabled"),
			Address:      v.GetString("vault.address"),
			Token:        v.GetString("vault.token"),
			Mount:        v.GetString("vault.mount"),
			Path:         v.GetString("vault.path"),
			AccessURLKey: v.GetString("vault.access_url_key"),
		},
		Secrets: Secrets{File: v.GetString("secrets.file")},
		MCP:     MCP{Enabled: v.GetBool("mcp.enabled")},
		Dashboard: Dashboard{
			Enabled: v.GetBool("dashboard.enabled"),
			Token:   v.GetString("dashboard.token"),
		},
		Update: Update{
			Check:      v.GetBool("update.check"),
			AllowApply: v.GetBool("update.allow_apply"),
			Repository: v.GetString("update.repository"),
		},
		Events: Events{
			Enabled:              v.GetBool("events.enabled"),
			RetentionDays:        v.GetInt("events.retention_days"),
			HistoryRetentionDays: v.GetInt("events.history_retention_days"),
		},
		Webhooks: Webhooks{
			Enabled:     v.GetBool("webhooks.enabled"),
			Timeout:     webhookTimeout,
			MaxAttempts: v.GetInt("webhooks.max_attempts"),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr must not be empty")
	}
	switch c.Database.Driver {
	case "sqlite":
		if c.Database.Path == "" {
			return fmt.Errorf("database.path must not be empty when database.driver is sqlite")
		}
	case "postgres":
		if c.Database.DSN == "" {
			return fmt.Errorf("database.dsn must not be empty when database.driver is postgres")
		}
	default:
		return fmt.Errorf("database.driver must be sqlite or postgres, got %q", c.Database.Driver)
	}
	if c.Sync.Interval <= 0 {
		return fmt.Errorf("sync.interval must be positive")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("log.format must be json or text, got %q", c.Log.Format)
	}
	if c.Update.Check && !strings.Contains(c.Update.Repository, "/") {
		return fmt.Errorf("update.repository must be in owner/name form, got %q", c.Update.Repository)
	}
	if c.Events.RetentionDays < 0 {
		return fmt.Errorf("events.retention_days must not be negative, got %d", c.Events.RetentionDays)
	}
	if c.Events.HistoryRetentionDays < 0 {
		return fmt.Errorf("events.history_retention_days must not be negative, got %d", c.Events.HistoryRetentionDays)
	}
	if c.Webhooks.Enabled {
		if c.Webhooks.Timeout <= 0 {
			return fmt.Errorf("webhooks.timeout must be positive")
		}
		if c.Webhooks.MaxAttempts < 1 {
			return fmt.Errorf("webhooks.max_attempts must be at least 1, got %d", c.Webhooks.MaxAttempts)
		}
	}
	return nil
}
