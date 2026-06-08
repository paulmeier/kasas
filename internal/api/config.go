package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/paulmeier/kasas/internal/config"
)

// ConfigDTO is the JSON representation of the effective configuration shown on the
// dashboard's read-only Settings page. The API is unauthenticated, so secrets are
// never included: the SimpleFIN credential is reported only as a connected flag,
// the Vault token only as a boolean, and any Postgres DSN password is masked.
type ConfigDTO struct {
	Server    ServerConfigDTO    `json:"server"`
	Log       LogConfigDTO       `json:"log"`
	Database  DatabaseConfigDTO  `json:"database"`
	SimpleFIN SimpleFINConfigDTO `json:"simplefin"`
	Sync      SyncConfigDTO      `json:"sync"`
	Vault     VaultConfigDTO     `json:"vault"`
	Secrets   SecretsConfigDTO   `json:"secrets"`
	MCP       MCPConfigDTO       `json:"mcp"`
	Dashboard DashboardConfigDTO `json:"dashboard"`
	Update    UpdateConfigDTO    `json:"update"`
	Events    EventsConfigDTO    `json:"events"`
	Webhooks  WebhooksConfigDTO  `json:"webhooks"`
	Security  SecurityConfigDTO  `json:"security"`
}

// SecurityConfigDTO reports the dashboard-token state for the Settings page. The
// token value itself is never included. TokenSource is "config", "stored", or
// "none"; the Settings page hides the generate/revoke controls when it is "config".
type SecurityConfigDTO struct {
	AuthRequired bool   `json:"auth_required"`
	TokenSource  string `json:"token_source"`
}

// ServerConfigDTO mirrors config.Server.
type ServerConfigDTO struct {
	Addr string `json:"addr"`
}

// LogConfigDTO mirrors config.Log.
type LogConfigDTO struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}

// DatabaseConfigDTO mirrors config.Database, with the DSN password masked.
type DatabaseConfigDTO struct {
	Driver string `json:"driver"`
	Path   string `json:"path"`
	DSN    string `json:"dsn"` // password-redacted; empty when driver=sqlite
}

// SimpleFINConfigDTO reports the connection state only — never the token/URL.
type SimpleFINConfigDTO struct {
	Connected bool `json:"connected"`
}

// SyncConfigDTO mirrors config.Sync (interval as a human-readable duration).
type SyncConfigDTO struct {
	Enabled      bool   `json:"enabled"`
	Interval     string `json:"interval"`
	LookbackDays int    `json:"lookback_days"`
	RunOnStart   bool   `json:"run_on_start"`
}

// VaultConfigDTO mirrors config.Vault, with the token reduced to a boolean.
type VaultConfigDTO struct {
	Enabled      bool   `json:"enabled"`
	Address      string `json:"address"`
	Mount        string `json:"mount"`
	Path         string `json:"path"`
	AccessURLKey string `json:"access_url_key"`
	TokenSet     bool   `json:"token_set"`
}

// SecretsConfigDTO mirrors config.Secrets (just the fallback file path).
type SecretsConfigDTO struct {
	File string `json:"file"`
}

// MCPConfigDTO mirrors config.MCP.
type MCPConfigDTO struct {
	Enabled bool `json:"enabled"`
}

// DashboardConfigDTO mirrors config.Dashboard.
type DashboardConfigDTO struct {
	Enabled bool `json:"enabled"`
}

// UpdateConfigDTO mirrors config.Update.
type UpdateConfigDTO struct {
	Check      bool   `json:"check"`
	AllowApply bool   `json:"allow_apply"`
	Repository string `json:"repository"`
}

// EventsConfigDTO mirrors config.Events.
type EventsConfigDTO struct {
	Enabled              bool `json:"enabled"`
	RetentionDays        int  `json:"retention_days"`
	HistoryRetentionDays int  `json:"history_retention_days"`
}

// WebhooksConfigDTO mirrors config.Webhooks (timeout as a human-readable duration).
type WebhooksConfigDTO struct {
	Enabled     bool   `json:"enabled"`
	Timeout     string `json:"timeout"`
	MaxAttempts int    `json:"max_attempts"`
}

// toConfigDTO builds the redacted view of the effective configuration. connected
// reflects whether a SimpleFIN access URL is currently stored; security reports
// the dashboard-token state.
func toConfigDTO(cfg *config.Config, connected bool, security SecurityConfigDTO) ConfigDTO {
	return ConfigDTO{
		Server: ServerConfigDTO{Addr: cfg.Server.Addr},
		Log:    LogConfigDTO{Level: cfg.Log.Level, Format: cfg.Log.Format},
		Database: DatabaseConfigDTO{
			Driver: cfg.Database.Driver,
			Path:   cfg.Database.Path,
			DSN:    redactDSN(cfg.Database.DSN),
		},
		SimpleFIN: SimpleFINConfigDTO{Connected: connected},
		Sync: SyncConfigDTO{
			Enabled:      cfg.Sync.Enabled,
			Interval:     cfg.Sync.Interval.String(),
			LookbackDays: cfg.Sync.LookbackDays,
			RunOnStart:   cfg.Sync.RunOnStart,
		},
		Vault: VaultConfigDTO{
			Enabled:      cfg.Vault.Enabled,
			Address:      cfg.Vault.Address,
			Mount:        cfg.Vault.Mount,
			Path:         cfg.Vault.Path,
			AccessURLKey: cfg.Vault.AccessURLKey,
			TokenSet:     cfg.Vault.Token != "",
		},
		Secrets:   SecretsConfigDTO{File: cfg.Secrets.File},
		MCP:       MCPConfigDTO{Enabled: cfg.MCP.Enabled},
		Dashboard: DashboardConfigDTO{Enabled: cfg.Dashboard.Enabled},
		Update: UpdateConfigDTO{
			Check:      cfg.Update.Check,
			AllowApply: cfg.Update.AllowApply,
			Repository: cfg.Update.Repository,
		},
		Events: EventsConfigDTO{
			Enabled:              cfg.Events.Enabled,
			RetentionDays:        cfg.Events.RetentionDays,
			HistoryRetentionDays: cfg.Events.HistoryRetentionDays,
		},
		Webhooks: WebhooksConfigDTO{
			Enabled:     cfg.Webhooks.Enabled,
			Timeout:     cfg.Webhooks.Timeout.String(),
			MaxAttempts: cfg.Webhooks.MaxAttempts,
		},
		Security: security,
	}
}

// redactDSN masks the password in a Postgres connection string for display.
// A URL-form DSN ("postgres://user:pass@host/db") has its password replaced; an
// empty DSN (sqlite) stays empty; anything else (keyword-form or unparseable) is
// reduced to "(set)" so a password can never leak through display.
func redactDSN(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return ""
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return "(set)"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	return u.String()
}

// handleGetConfig returns the effective configuration with secrets redacted. The
// SimpleFIN connection state is resolved live from the secret store (best-effort:
// a read error reports "not connected" rather than failing the whole page).
func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.config == nil {
		s.writeError(w, http.StatusServiceUnavailable, "configuration is not available")
		return
	}
	connected := false
	if s.sources != nil {
		if ok, err := s.sources.CredentialConfigured(r.Context(), "simplefin"); err != nil {
			s.logger.Error("config: read credential status", "error", err)
		} else {
			connected = ok
		}
	}

	security := SecurityConfigDTO{TokenSource: "none"}
	if s.auth != nil {
		security.AuthRequired = s.auth.Required()
		security.TokenSource = s.auth.Source()
	}

	s.writeJSON(w, http.StatusOK, toConfigDTO(s.config, connected, security))
}

// setCredentialRequest is the body of PUT /simplefin/credential. token may be a
// one-time setup token or a ready access URL (auto-detected server-side).
type setCredentialRequest struct {
	Token string `json:"token"`
}

// handleSetSimpleFINCredential stores a SimpleFIN credential so the next sync uses
// it (no restart). A failure (e.g. an invalid/expired setup token, or the bridge
// rejecting the claim) is surfaced to the caller so the UI can show why.
func (s *Server) handleSetSimpleFINCredential(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeError(w, http.StatusServiceUnavailable, "credential management is not available")
		return
	}

	var req setCredentialRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)) // 16 KiB is plenty
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		s.writeError(w, http.StatusBadRequest, "token is required")
		return
	}

	if err := s.sources.SetCredential(r.Context(), "simplefin", req.Token); err != nil {
		s.logger.Warn("set simplefin credential failed", "error", err)
		s.writeError(w, http.StatusBadRequest, "could not set SimpleFIN credential: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"connected": true})
}
