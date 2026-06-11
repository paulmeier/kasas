// Package config loads kasas configuration from a TOML file and the
// environment. Environment variables take precedence and are prefixed with
// KASAS_, with nested keys joined by underscores (e.g. KASAS_SERVER_ADDR,
// KASAS_SIMPLEFIN_ACCESS_URL).
package config

import (
	"fmt"
	"net/url"
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
	CSV       CSV
	Teller    Teller
	Plaid     Plaid
	Bitcoin   Bitcoin
	Ethereum  Ethereum
	Sync      Sync
	Vault     Vault
	Secrets   Secrets
	MCP       MCP
	Dashboard Dashboard
	Update    Update
	Events    Events
	Webhooks  Webhooks
	Plugins   Plugins
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

// CSV configures the CSV file-import source: a set of folder profiles (each one
// account) plus the Google Drive OAuth app credentials shared by all Drive
// folders. When no folders are configured the source is not started. Folders are
// loaded from the config file (an array of tables); the Drive credentials may also
// come from the environment (KASAS_CSV_GDRIVE_CLIENT_ID, ...).
type CSV struct {
	Folders            []CSVFolder `mapstructure:"folders" json:"folders"`
	GDriveClientID     string      `mapstructure:"gdrive_client_id" json:"gdrive_client_id"`
	GDriveClientSecret string      `mapstructure:"gdrive_client_secret" json:"gdrive_client_secret"`
	GDriveRedirectURL  string      `mapstructure:"gdrive_redirect_url" json:"gdrive_redirect_url"`
}

// CSVFolder is one import profile: a backend location (a local directory or a
// Google Drive folder) plus how to map its columns. Each folder maps to one
// account. The mapstructure tags load it from TOML; the json tags carry it to the
// source through the registry env.
type CSVFolder struct {
	Name     string     `mapstructure:"name" json:"name"`
	Backend  string     `mapstructure:"backend" json:"backend"`
	Path     string     `mapstructure:"path" json:"path"`
	FolderID string     `mapstructure:"folder_id" json:"folder_id"`
	Account  string     `mapstructure:"account" json:"account"`
	Org      string     `mapstructure:"org" json:"org"`
	Currency string     `mapstructure:"currency" json:"currency"`
	Mapping  CSVMapping `mapstructure:"mapping" json:"mapping"`
}

// CSVMapping declares how a folder's CSV columns map to transaction fields. Every
// field is optional; omitted columns are auto-detected from common header names.
type CSVMapping struct {
	HasHeader         *bool  `mapstructure:"has_header" json:"has_header,omitempty"`
	Delimiter         string `mapstructure:"delimiter" json:"delimiter,omitempty"`
	DateColumn        string `mapstructure:"date_column" json:"date_column,omitempty"`
	DateFormat        string `mapstructure:"date_format" json:"date_format,omitempty"`
	AmountColumn      string `mapstructure:"amount_column" json:"amount_column,omitempty"`
	DebitColumn       string `mapstructure:"debit_column" json:"debit_column,omitempty"`
	CreditColumn      string `mapstructure:"credit_column" json:"credit_column,omitempty"`
	DescriptionColumn string `mapstructure:"description_column" json:"description_column,omitempty"`
	PayeeColumn       string `mapstructure:"payee_column" json:"payee_column,omitempty"`
	MemoColumn        string `mapstructure:"memo_column" json:"memo_column,omitempty"`
}

// Teller configures the Teller (https://teller.io) ingestion source, a pull
// source alongside SimpleFIN. Each access token is one bank enrollment from Teller
// Connect: provide a single one as AccessToken (env-friendly) and/or a list as
// AccessTokens (config-file array) for several banks; more can also be added at
// runtime from the dashboard Sources page (stored in the secret store, unioned
// with these). Certificate and PrivateKey are filesystem paths to the mutual-TLS
// client certificate Teller requires for its development and production
// environments (omit both for the sandbox). The source is started when at least
// one access token or a certificate is set.
type Teller struct {
	AccessToken  string   `mapstructure:"access_token"`
	AccessTokens []string `mapstructure:"access_tokens"`
	Certificate  string   `mapstructure:"certificate"`
	PrivateKey   string   `mapstructure:"private_key"`
}

// Plaid configures the Plaid (https://plaid.com) ingestion source, a pull source
// alongside SimpleFIN. ClientID and Secret are the app-level credentials (one pair
// per environment, from the Plaid Dashboard) shared by every linked bank; the source
// is started only when both are set. Environment selects the API host (sandbox,
// development, or production). Each access token is one linked Item (bank login):
// provide a single one as AccessToken (env-friendly) and/or a list as AccessTokens
// (config-file array), and more can be added at runtime from the dashboard Sources
// page (stored in the secret store, unioned with these). CountryCodes scopes the
// institution-name lookup (default US).
type Plaid struct {
	ClientID     string   `mapstructure:"client_id"`
	Secret       string   `mapstructure:"secret"`
	Environment  string   `mapstructure:"environment"`
	CountryCodes []string `mapstructure:"country_codes"`
	AccessToken  string   `mapstructure:"access_token"`
	AccessTokens []string `mapstructure:"access_tokens"`
}

// Bitcoin configures the Bitcoin address-watching ingestion source, a pull source
// alongside the bank sources. It needs no API key: provide one or more public
// addresses to watch, as a single Address (env-friendly) and/or a list of Addresses
// (config-file array), and kasas records each on-chain transaction touching them. More
// addresses can be added at runtime from the dashboard Sources page (stored in the
// secret store, unioned with these). APIURL overrides the mempool.space / Esplora API
// base URL (default https://mempool.space/api) so a self-hoster can use their own node.
// The source is started when at least one address or a custom APIURL is set.
type Bitcoin struct {
	Address   string   `mapstructure:"address"`
	Addresses []string `mapstructure:"addresses"`
	APIURL    string   `mapstructure:"api_url"`
}

// Ethereum configures the Ethereum address-watching ingestion source, a pull source
// alongside the bank sources. APIKey is a free Etherscan API key (an app-level secret
// shared by every watched address); the source is started only when it is set. Provide
// addresses to watch as a single Address (env-friendly) and/or a list of Addresses
// (config-file array); more can be added at runtime from the dashboard Sources page.
// ChainID selects the EVM chain via Etherscan V2 (default 1 = Ethereum mainnet), and
// APIURL overrides the API base URL (default https://api.etherscan.io/v2/api; a
// Blockscout instance's /api also works).
type Ethereum struct {
	APIKey    string   `mapstructure:"api_key"`
	APIURL    string   `mapstructure:"api_url"`
	ChainID   int      `mapstructure:"chain_id"`
	Address   string   `mapstructure:"address"`
	Addresses []string `mapstructure:"addresses"`
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

// Plugins controls the plugin runtime: kasas loads plugins from Dir and runs them
// in a sandboxed language VM, reacting to committed events off the bus (the async
// counterpart to the rules engine and webhooks). It is effective only when
// events.enabled is also true, since plugins consume the in-process event bus.
// Enabled defaults to FALSE because a plugin is third-party code; enabling the
// subsystem and each individual plugin is opt-in. HookTimeout bounds a single hook
// invocation, and QueueSize is the per-plugin job-queue depth (a slow plugin drops
// jobs rather than stalling the bus; consumers reconcile via the /events cursor).
type Plugins struct {
	Enabled     bool
	Dir         string
	HookTimeout time.Duration
	QueueSize   int
	Registry    PluginRegistry
	Net         PluginNet
}

// PluginNet bounds plugin network egress (the net:fetch capability, ADR 0002).
// These are host-owned ceilings — a plugin declares WHICH hosts it may reach in
// its manifest, but never how fast, how long, or how much: Timeout caps a single
// request (a plugin may ask for less, never more), MaxResponseBytes caps the body
// read, RatePerMinute throttles requests per plugin, and MaxRedirects bounds the
// redirect chain (each hop re-validated against the allowlist).
type PluginNet struct {
	Timeout          time.Duration
	MaxResponseBytes int
	RatePerMinute    int
	MaxRedirects     int
}

// PluginRegistry controls the community-plugin marketplace: browsing a published
// registry index and installing plugins into Plugins.Dir from the dashboard. It is
// effective only when Plugins.Enabled is true. Enabled defaults to TRUE (pointing
// at the official kasas-plugins registry), but installing a plugin is still an
// explicit, admin-only action, and an installed plugin starts disabled — so turning
// the marketplace on never runs third-party code by itself. URL is the index.json
// to fetch; Ref is the git ref used to build raw file-download URLs.
type PluginRegistry struct {
	Enabled bool
	URL     string
	Ref     string
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
	v.SetDefault("csv.gdrive_client_id", "")
	v.SetDefault("csv.gdrive_client_secret", "")
	v.SetDefault("csv.gdrive_redirect_url", "")
	v.SetDefault("teller.access_token", "")
	v.SetDefault("teller.certificate", "")
	v.SetDefault("teller.private_key", "")
	v.SetDefault("plaid.client_id", "")
	v.SetDefault("plaid.secret", "")
	v.SetDefault("plaid.environment", "sandbox")
	v.SetDefault("plaid.access_token", "")
	v.SetDefault("bitcoin.address", "")
	v.SetDefault("bitcoin.api_url", "")
	v.SetDefault("ethereum.api_key", "")
	v.SetDefault("ethereum.api_url", "")
	v.SetDefault("ethereum.chain_id", 1)
	v.SetDefault("ethereum.address", "")
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
	v.SetDefault("plugins.enabled", false) // opt-in: plugins are third-party code
	v.SetDefault("plugins.dir", "/data/plugins")
	v.SetDefault("plugins.hook_timeout", "5s")
	v.SetDefault("plugins.queue_size", 256)
	v.SetDefault("plugins.registry.enabled", true)
	v.SetDefault("plugins.registry.url", "https://raw.githubusercontent.com/paulmeier/kasas-plugins/main/registry/index.json")
	v.SetDefault("plugins.registry.ref", "main")
	v.SetDefault("plugins.net.timeout", "10s")
	v.SetDefault("plugins.net.max_response_bytes", 5*1024*1024) // 5 MiB
	v.SetDefault("plugins.net.rate_per_minute", 60)
	v.SetDefault("plugins.net.max_redirects", 5)

	v.SetEnvPrefix("KASAS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// A configured-but-missing file is seeded with the annotated example so a fresh
	// deployment gets a real, editable config.toml at a known path (the Docker image
	// sets KASAS_CONFIG=/data/config.toml). ensureConfigFile returns "" if it cannot
	// create the file, in which case defaults + environment are used.
	if path != "" {
		path = ensureConfigFile(path)
	}
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

	pluginHookTimeout, err := time.ParseDuration(v.GetString("plugins.hook_timeout"))
	if err != nil {
		return nil, fmt.Errorf("invalid plugins.hook_timeout %q: %w", v.GetString("plugins.hook_timeout"), err)
	}

	pluginNetTimeout, err := time.ParseDuration(v.GetString("plugins.net.timeout"))
	if err != nil {
		return nil, fmt.Errorf("invalid plugins.net.timeout %q: %w", v.GetString("plugins.net.timeout"), err)
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
		CSV: CSV{
			GDriveClientID:     v.GetString("csv.gdrive_client_id"),
			GDriveClientSecret: v.GetString("csv.gdrive_client_secret"),
			GDriveRedirectURL:  v.GetString("csv.gdrive_redirect_url"),
		},
		Teller: Teller{
			AccessToken: v.GetString("teller.access_token"),
			Certificate: v.GetString("teller.certificate"),
			PrivateKey:  v.GetString("teller.private_key"),
		},
		Plaid: Plaid{
			ClientID:    v.GetString("plaid.client_id"),
			Secret:      v.GetString("plaid.secret"),
			Environment: v.GetString("plaid.environment"),
			AccessToken: v.GetString("plaid.access_token"),
		},
		Bitcoin: Bitcoin{
			Address: v.GetString("bitcoin.address"),
			APIURL:  v.GetString("bitcoin.api_url"),
		},
		Ethereum: Ethereum{
			APIKey:  v.GetString("ethereum.api_key"),
			APIURL:  v.GetString("ethereum.api_url"),
			ChainID: v.GetInt("ethereum.chain_id"),
			Address: v.GetString("ethereum.address"),
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
		Plugins: Plugins{
			Enabled:     v.GetBool("plugins.enabled"),
			Dir:         v.GetString("plugins.dir"),
			HookTimeout: pluginHookTimeout,
			QueueSize:   v.GetInt("plugins.queue_size"),
			Registry: PluginRegistry{
				Enabled: v.GetBool("plugins.registry.enabled"),
				URL:     v.GetString("plugins.registry.url"),
				Ref:     v.GetString("plugins.registry.ref"),
			},
			Net: PluginNet{
				Timeout:          pluginNetTimeout,
				MaxResponseBytes: v.GetInt("plugins.net.max_response_bytes"),
				RatePerMinute:    v.GetInt("plugins.net.rate_per_minute"),
				MaxRedirects:     v.GetInt("plugins.net.max_redirects"),
			},
		},
	}

	// CSV folder profiles are an array of tables, loaded structurally rather than
	// key-by-key. Drive credentials above may also come from the environment.
	if err := v.UnmarshalKey("csv.folders", &cfg.CSV.Folders); err != nil {
		return nil, fmt.Errorf("invalid csv.folders config: %w", err)
	}

	// Teller access tokens are an optional string array (one per bank), loaded
	// structurally; the singular teller.access_token above is the env-friendly form.
	if err := v.UnmarshalKey("teller.access_tokens", &cfg.Teller.AccessTokens); err != nil {
		return nil, fmt.Errorf("invalid teller.access_tokens config: %w", err)
	}

	// Plaid access tokens and country codes are optional string arrays loaded
	// structurally; the singular plaid.access_token above is the env-friendly form.
	if err := v.UnmarshalKey("plaid.access_tokens", &cfg.Plaid.AccessTokens); err != nil {
		return nil, fmt.Errorf("invalid plaid.access_tokens config: %w", err)
	}
	if err := v.UnmarshalKey("plaid.country_codes", &cfg.Plaid.CountryCodes); err != nil {
		return nil, fmt.Errorf("invalid plaid.country_codes config: %w", err)
	}

	// Bitcoin and Ethereum watched addresses are optional string arrays loaded
	// structurally; the singular address keys above are the env-friendly form.
	if err := v.UnmarshalKey("bitcoin.addresses", &cfg.Bitcoin.Addresses); err != nil {
		return nil, fmt.Errorf("invalid bitcoin.addresses config: %w", err)
	}
	if err := v.UnmarshalKey("ethereum.addresses", &cfg.Ethereum.Addresses); err != nil {
		return nil, fmt.Errorf("invalid ethereum.addresses config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks the configuration's cross-field invariants. Load calls it on
// every boot; the settings service calls it again after applying dashboard-stored
// overrides so an invalid override is rejected before it is persisted.
func (c *Config) Validate() error {
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
	if c.Ethereum.ChainID < 1 {
		return fmt.Errorf("ethereum.chain_id must be at least 1, got %d", c.Ethereum.ChainID)
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
	if c.Plugins.Enabled {
		if c.Plugins.Dir == "" {
			return fmt.Errorf("plugins.dir must not be empty when plugins are enabled")
		}
		if c.Plugins.HookTimeout <= 0 {
			return fmt.Errorf("plugins.hook_timeout must be positive")
		}
		if c.Plugins.QueueSize < 1 {
			return fmt.Errorf("plugins.queue_size must be at least 1, got %d", c.Plugins.QueueSize)
		}
		if c.Plugins.Registry.Enabled {
			u, err := url.Parse(c.Plugins.Registry.URL)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("plugins.registry.url must be an absolute URL when the registry is enabled, got %q", c.Plugins.Registry.URL)
			}
			if u.Scheme != "https" {
				return fmt.Errorf("plugins.registry.url must use https, got %q", c.Plugins.Registry.URL)
			}
		}
	}
	return nil
}
