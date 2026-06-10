// Package settings makes parts of the kasas configuration editable at runtime
// from the dashboard, REST API, and MCP — permanently. Each editable key is
// declared once here as a Definition; values changed through the Service are
// persisted (non-secret values in the settings table, secret values in the
// secret store alongside source credentials) and applied OVER the config file /
// KASAS_ environment on every boot, so a dashboard change survives restarts and
// wins over static config until it is reset.
//
// Settings change subsystem and source construction, which happens at startup,
// so a stored change takes effect on the next restart (the API exposes a
// restart action to make that one click). Source credentials are NOT settings:
// they already apply live through the source.Credentialed seam.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"time"

	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/sources/bitcoin"
	"github.com/paulmeier/kasas/internal/sources/csv"
	"github.com/paulmeier/kasas/internal/sources/ethereum"
	"github.com/paulmeier/kasas/internal/sources/plaid"
	"github.com/paulmeier/kasas/internal/sources/teller"
	"github.com/paulmeier/kasas/internal/vault"
)

// Kind tells the dashboard which control to render for a setting and how its
// string value is parsed.
type Kind string

const (
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindString   Kind = "string"
	KindDuration Kind = "duration" // Go duration syntax, e.g. "6h", "90s"
	KindJSON     Kind = "json"     // structured value edited as JSON text
)

// Definition declares one editable configuration key: how to display it, parse
// it, and map it onto the config struct. Get/Set are the single source of truth
// for the key's string form, so stored values, display values, and
// restart-required comparisons all agree.
type Definition struct {
	Key     string
	Title   string
	Help    string
	Kind    Kind
	Secret  bool     // stored in the secret store, never echoed back
	Source  string   // owning ingestion source type ("plaid", ...); "" = app setting
	Section string   // dashboard Settings page grouping for app settings
	Enum    []string // allowed values; rendered as a select

	Get func(*config.Config) string
	Set func(*config.Config, string) error
}

// secretKeyPrefix namespaces dashboard-stored secret settings within the shared
// secret store, next to source credentials (e.g. "setting.plaid.secret").
const secretKeyPrefix = "setting."

// definitions is built once; the registry is static.
var definitions = buildDefinitions()

// Definitions returns every editable setting in display order: app settings
// grouped by section first, then per-source settings.
func Definitions() []Definition { return definitions }

// Lookup returns the definition for key, or false when the key is not editable.
func Lookup(key string) (Definition, bool) {
	for _, d := range definitions {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

func buildDefinitions() []Definition {
	defs := []Definition{
		// Logging.
		stringSetting("log.level", "", "Logging", "Log level", "Minimum level kasas logs at.",
			[]string{"debug", "info", "warn", "error"},
			func(c *config.Config) *string { return &c.Log.Level }),
		stringSetting("log.format", "", "Logging", "Log format", "Structured JSON logs, or human-readable text.",
			[]string{"json", "text"},
			func(c *config.Config) *string { return &c.Log.Format }),

		// Sync.
		boolSetting("sync.enabled", "", "Sync", "Background sync", "Run the recurring sync schedule. Disable to sync only on demand.",
			func(c *config.Config) *bool { return &c.Sync.Enabled }),
		durationSetting("sync.interval", "", "Sync", "Interval", "How often every source is synced, e.g. 6h or 30m.",
			func(c *config.Config) *time.Duration { return &c.Sync.Interval }),
		intSetting("sync.lookback_days", "", "Sync", "Lookback days", "How far back transactions are fetched; 0 means everything available.",
			func(c *config.Config) *int { return &c.Sync.LookbackDays }),
		boolSetting("sync.run_on_start", "", "Sync", "Sync on startup", "Trigger a sync immediately when kasas starts.",
			func(c *config.Config) *bool { return &c.Sync.RunOnStart }),

		// MCP.
		boolSetting("mcp.enabled", "", "MCP server", "MCP server", "Serve the built-in Model Context Protocol server at /mcp.",
			func(c *config.Config) *bool { return &c.MCP.Enabled }),

		// Dashboard.
		boolSetting("dashboard.enabled", "", "Dashboard", "Dashboard", "Serve this web dashboard. Disabling it (after a restart) leaves only the REST API and MCP — re-enable via PUT /api/v1/settings/dashboard.enabled or your config.",
			func(c *config.Config) *bool { return &c.Dashboard.Enabled }),

		// Events & history.
		boolSetting("events.enabled", "", "Events & history", "Event stream", "Record every change as an immutable event (powers /events, webhooks, plugins, and transaction history).",
			func(c *config.Config) *bool { return &c.Events.Enabled }),
		intSetting("events.retention_days", "", "Events & history", "Event retention (days)", "Prune events older than this many days; 0 keeps them forever.",
			func(c *config.Config) *int { return &c.Events.RetentionDays }),
		intSetting("events.history_retention_days", "", "Events & history", "History retention (days)", "Prune transaction-history snapshots older than this many days; 0 keeps full history forever.",
			func(c *config.Config) *int { return &c.Events.HistoryRetentionDays }),

		// Webhooks.
		boolSetting("webhooks.enabled", "", "Webhooks", "Webhook delivery", "Deliver committed events to registered webhook endpoints (needs the event stream).",
			func(c *config.Config) *bool { return &c.Webhooks.Enabled }),
		durationSetting("webhooks.timeout", "", "Webhooks", "Delivery timeout", "Bound on each webhook delivery attempt, e.g. 10s.",
			func(c *config.Config) *time.Duration { return &c.Webhooks.Timeout }),
		intSetting("webhooks.max_attempts", "", "Webhooks", "Max attempts", "Retries before a delivery is abandoned (consumers reconcile via the /events cursor).",
			func(c *config.Config) *int { return &c.Webhooks.MaxAttempts }),

		// Plugins.
		boolSetting("plugins.enabled", "", "Plugins", "Plugin system", "Load and run sandboxed plugins reacting to events (needs the event stream). Each installed plugin is still enabled individually.",
			func(c *config.Config) *bool { return &c.Plugins.Enabled }),
		stringSetting("plugins.dir", "", "Plugins", "Plugins directory", "Directory plugins are installed into and loaded from.", nil,
			func(c *config.Config) *string { return &c.Plugins.Dir }),
		durationSetting("plugins.hook_timeout", "", "Plugins", "Hook timeout", "Bound on a single plugin hook invocation, e.g. 5s.",
			func(c *config.Config) *time.Duration { return &c.Plugins.HookTimeout }),
		intSetting("plugins.queue_size", "", "Plugins", "Queue size", "Per-plugin job-queue depth; a slow plugin drops jobs rather than stalling the bus.",
			func(c *config.Config) *int { return &c.Plugins.QueueSize }),
		boolSetting("plugins.registry.enabled", "", "Plugins", "Marketplace", "Browse and install community plugins from the registry (installing stays an explicit admin action).",
			func(c *config.Config) *bool { return &c.Plugins.Registry.Enabled }),
		stringSetting("plugins.registry.url", "", "Plugins", "Registry URL", "The marketplace index.json to fetch (https only).", nil,
			func(c *config.Config) *string { return &c.Plugins.Registry.URL }),
		stringSetting("plugins.registry.ref", "", "Plugins", "Registry ref", "Git ref used to build the registry's raw file-download URLs.", nil,
			func(c *config.Config) *string { return &c.Plugins.Registry.Ref }),

		// Updates.
		boolSetting("update.check", "", "Updates", "Check for updates", "Periodically check GitHub for a newer release and show the dashboard banner.",
			func(c *config.Config) *bool { return &c.Update.Check }),
		boolSetting("update.allow_apply", "", "Updates", "Allow in-place update", "Let the dashboard/API trigger an in-place self-update. Disable for Docker deployments that update by pulling a new image.",
			func(c *config.Config) *bool { return &c.Update.AllowApply }),
		stringSetting("update.repository", "", "Updates", "Repository", "owner/name GitHub repository checked for releases.", nil,
			func(c *config.Config) *string { return &c.Update.Repository }),
	}

	// Per-source settings, surfaced on each source's card on the Sources page.
	// Credentials (tokens, watched addresses) are NOT here — they apply live
	// through the credential endpoints; these are the non-credential knobs that
	// participate in source construction at startup.
	defs = append(defs,
		csvFoldersSetting(),
		stringSetting("csv.gdrive_client_id", csv.SourceType, "", "Google OAuth client ID", "OAuth client ID for the Google Drive backend, shared by all Drive folders.", nil,
			func(c *config.Config) *string { return &c.CSV.GDriveClientID }),
		secretSetting("csv.gdrive_client_secret", csv.SourceType, "Google OAuth client secret", "OAuth client secret for the Google Drive backend.",
			func(c *config.Config) *string { return &c.CSV.GDriveClientSecret }),
		stringSetting("csv.gdrive_redirect_url", csv.SourceType, "", "OAuth redirect URL", "The redirect URL registered with the OAuth client (must point at /api/v1/sources/csv/oauth/callback).", nil,
			func(c *config.Config) *string { return &c.CSV.GDriveRedirectURL }),

		stringSetting("teller.certificate", teller.SourceType, "", "Client certificate", "Path to the Teller client-certificate PEM. Required for the development and production environments; omit for sandbox.", nil,
			func(c *config.Config) *string { return &c.Teller.Certificate }),
		stringSetting("teller.private_key", teller.SourceType, "", "Client private key", "Path to the Teller client private-key PEM. Required alongside the certificate.", nil,
			func(c *config.Config) *string { return &c.Teller.PrivateKey }),

		stringSetting("plaid.client_id", plaid.SourceType, "", "Client ID", "App-level client ID from the Plaid Dashboard. The source activates when client ID and secret are both set.", nil,
			func(c *config.Config) *string { return &c.Plaid.ClientID }),
		secretSetting("plaid.secret", plaid.SourceType, "Secret", "App-level secret from the Plaid Dashboard (one per environment).",
			func(c *config.Config) *string { return &c.Plaid.Secret }),
		stringSetting("plaid.environment", plaid.SourceType, "", "Environment", "Which Plaid API host to use.",
			[]string{"sandbox", "development", "production"},
			func(c *config.Config) *string { return &c.Plaid.Environment }),
		countryCodesSetting(),

		stringSetting("bitcoin.api_url", bitcoin.SourceType, "", "API URL", "mempool.space / Esplora API base URL (default https://mempool.space/api). Setting it also activates the source so addresses can be added from this page.", nil,
			func(c *config.Config) *string { return &c.Bitcoin.APIURL }),

		secretSetting("ethereum.api_key", ethereum.SourceType, "Etherscan API key", "A free Etherscan API key (https://etherscan.io/myapikey). Required to activate the source.",
			func(c *config.Config) *string { return &c.Ethereum.APIKey }),
		stringSetting("ethereum.api_url", ethereum.SourceType, "", "API URL", "Etherscan V2 API base URL (default https://api.etherscan.io/v2/api). A Blockscout instance's /api endpoint also works.", nil,
			func(c *config.Config) *string { return &c.Ethereum.APIURL }),
		intSetting("ethereum.chain_id", ethereum.SourceType, "", "Chain ID", "EVM chain id (default 1 = Ethereum mainnet), e.g. 8453 = Base, 42161 = Arbitrum.",
			func(c *config.Config) *int { return &c.Ethereum.ChainID }),
	)
	return defs
}

func boolSetting(key, source, section, title, help string, field func(*config.Config) *bool) Definition {
	return Definition{
		Key: key, Kind: KindBool, Source: source, Section: section, Title: title, Help: help,
		Get: func(c *config.Config) string { return strconv.FormatBool(*field(c)) },
		Set: func(c *config.Config, raw string) error {
			v, err := strconv.ParseBool(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("%s: want true or false, got %q", key, raw)
			}
			*field(c) = v
			return nil
		},
	}
}

func intSetting(key, source, section, title, help string, field func(*config.Config) *int) Definition {
	return Definition{
		Key: key, Kind: KindInt, Source: source, Section: section, Title: title, Help: help,
		Get: func(c *config.Config) string { return strconv.Itoa(*field(c)) },
		Set: func(c *config.Config, raw string) error {
			v, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("%s: want a whole number, got %q", key, raw)
			}
			*field(c) = v
			return nil
		},
	}
}

func durationSetting(key, source, section, title, help string, field func(*config.Config) *time.Duration) Definition {
	return Definition{
		Key: key, Kind: KindDuration, Source: source, Section: section, Title: title, Help: help,
		Get: func(c *config.Config) string { return (*field(c)).String() },
		Set: func(c *config.Config, raw string) error {
			v, err := time.ParseDuration(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("%s: want a duration like 6h or 30s, got %q", key, raw)
			}
			*field(c) = v
			return nil
		},
	}
}

func stringSetting(key, source, section, title, help string, enum []string, field func(*config.Config) *string) Definition {
	return Definition{
		Key: key, Kind: KindString, Source: source, Section: section, Title: title, Help: help, Enum: enum,
		Get: func(c *config.Config) string { return *field(c) },
		Set: func(c *config.Config, raw string) error {
			v := strings.TrimSpace(raw)
			if len(enum) > 0 && !contains(enum, v) {
				return fmt.Errorf("%s: want one of %s, got %q", key, strings.Join(enum, ", "), raw)
			}
			*field(c) = v
			return nil
		},
	}
}

func secretSetting(key, source, title, help string, field func(*config.Config) *string) Definition {
	d := stringSetting(key, source, "", title, help, nil, field)
	d.Secret = true
	return d
}

// countryCodesSetting maps Plaid's country-code list to a comma-separated string.
func countryCodesSetting() Definition {
	return Definition{
		Key: "plaid.country_codes", Kind: KindString, Source: plaid.SourceType,
		Title: "Country codes", Help: "Comma-separated ISO country codes scoping institution lookup (default US).",
		Get: func(c *config.Config) string { return strings.Join(c.Plaid.CountryCodes, ",") },
		Set: func(c *config.Config, raw string) error {
			var codes []string
			for _, p := range strings.Split(raw, ",") {
				if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
					codes = append(codes, p)
				}
			}
			c.Plaid.CountryCodes = codes
			return nil
		},
	}
}

// csvFoldersSetting maps the CSV folder profiles to a JSON array, validated by
// the CSV source's own constructor so a stored value is one it will accept at
// the next boot.
func csvFoldersSetting() Definition {
	return Definition{
		Key: "csv.folders", Kind: KindJSON, Source: csv.SourceType,
		Title: "Folder profiles",
		Help: `JSON array of folder profiles, one per account. Example: [{"name":"Checking","backend":"local","path":"/data/import","currency":"USD"}] — ` +
			`backend "gdrive" uses "folder_id" instead of "path"; an optional "mapping" object overrides column detection.`,
		Get: func(c *config.Config) string {
			if len(c.CSV.Folders) == 0 {
				return ""
			}
			raw, err := json.Marshal(c.CSV.Folders)
			if err != nil {
				return ""
			}
			return string(raw)
		},
		Set: func(c *config.Config, raw string) error {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				c.CSV.Folders = nil
				return nil
			}
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.DisallowUnknownFields()
			var folders []config.CSVFolder
			if err := dec.Decode(&folders); err != nil {
				return fmt.Errorf("csv.folders: invalid JSON: %w", err)
			}
			if err := validateCSVFolders(folders); err != nil {
				return fmt.Errorf("csv.folders: %w", err)
			}
			c.CSV.Folders = folders
			return nil
		},
	}
}

// validateCSVFolders runs the folder profiles through the CSV source's real
// constructor (the same JSON hand-off buildEngine uses), so anything stored here
// is something the source will accept.
func validateCSVFolders(folders []config.CSVFolder) error {
	raw, err := json.Marshal(config.CSV{Folders: folders})
	if err != nil {
		return err
	}
	var cc csv.Config
	if err := json.Unmarshal(raw, &cc); err != nil {
		return err
	}
	_, err = csv.New(csv.Options{Config: cc})
	return err
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// Clone deep-copies a config via JSON round-trip (every field is exported and
// JSON-representable). Used to validate candidate overrides without touching
// the live config, and to keep the pre-override base for comparison.
func Clone(c *config.Config) *config.Config {
	raw, err := json.Marshal(c)
	if err != nil {
		panic(fmt.Sprintf("settings: marshal config: %v", err))
	}
	out := &config.Config{}
	if err := json.Unmarshal(raw, out); err != nil {
		panic(fmt.Sprintf("settings: unmarshal config: %v", err))
	}
	return out
}

// LoadOverrides reads every stored override: the settings table rows plus the
// secret-store values for secret-typed definitions. Rows for keys this build
// does not define are carried through untouched by Apply (it ignores them), so
// upgrades and downgrades are safe.
func LoadOverrides(ctx context.Context, store db.Store, secrets vault.SecretStore) (map[string]string, error) {
	out := map[string]string{}
	rows, err := store.ListSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	for _, def := range definitions {
		if !def.Secret {
			continue
		}
		v, err := secrets.SecretValue(ctx, secretKeyPrefix+def.Key)
		if err != nil {
			return nil, fmt.Errorf("read secret setting %s: %w", def.Key, err)
		}
		if v != "" {
			out[def.Key] = v
		}
	}
	return out, nil
}

// Apply applies stored overrides onto cfg, key by key in definition order. An
// override for a key this build does not define is ignored; one whose value no
// longer parses is skipped with a warning — a bad stored value must never
// prevent kasas from starting. The combined result is then validated; on
// failure cfg is restored untouched and the error returned so the caller can
// fall back to the file/env config.
func Apply(cfg *config.Config, overrides map[string]string, logger *slog.Logger) error {
	if len(overrides) == 0 {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	snapshot := Clone(cfg)
	applied := 0
	for _, def := range definitions {
		raw, ok := overrides[def.Key]
		if !ok {
			continue
		}
		if err := def.Set(cfg, raw); err != nil {
			logger.Warn("ignoring invalid stored setting", "key", def.Key, "error", err)
			continue
		}
		applied++
	}
	if applied == 0 {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		*cfg = *snapshot
		return fmt.Errorf("stored settings produce an invalid configuration: %w", err)
	}
	return nil
}
