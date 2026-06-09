// Package api exposes the kasas REST API, health/metrics endpoints, and the
// built-in MCP server over HTTP.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/selfupdate"
)

// Syncer triggers an on-demand sync.
type Syncer interface {
	Sync(ctx context.Context) (poller.SyncResult, error)
}

// SourceManager manages all ingestion sources at runtime: listing them, syncing
// one, and managing each one's credential (pasted or via the browser OAuth flow).
// Implemented by *poller.Engine. When provided, it powers the dashboard's Sources
// page, the /sources REST endpoints, and the connected status in /config.
type SourceManager interface {
	// Sources lists every configured source with its readiness and credential shape.
	Sources(ctx context.Context) ([]poller.SourceStatus, error)
	// SyncSource runs a single source by type.
	SyncSource(ctx context.Context, typ string) (poller.SyncResult, error)
	// SetCredential stores a pasted credential for a source. For a multi-credential
	// source (e.g. Teller) it adds one; otherwise it replaces the credential.
	SetCredential(ctx context.Context, typ, input string) error
	// RemoveSourceCredential removes one credential (by id) from a multi-credential
	// source, e.g. disconnecting a single bank enrollment.
	RemoveSourceCredential(ctx context.Context, typ, id string) error
	// CredentialConfigured reports whether a source is ready to sync.
	CredentialConfigured(ctx context.Context, typ string) (bool, error)
	// OAuthStart returns the provider consent URL for a source's browser OAuth flow.
	OAuthStart(typ, state string) (string, error)
	// OAuthExchange completes the OAuth flow, storing the credential.
	OAuthExchange(ctx context.Context, typ, code string) error
}

// UpdateChecker reports the running build's status against the latest release
// and provides that release for an apply. Implemented by *selfupdate.Checker.
type UpdateChecker interface {
	Status(ctx context.Context) selfupdate.Status
	LatestRelease(ctx context.Context) (*selfupdate.Release, error)
}

// Server wires the HTTP handlers to their dependencies.
type Server struct {
	store      db.Store
	syncer     Syncer
	sources    SourceManager   // nil when source management is unavailable
	config     *config.Config  // resolved config for the read-only Settings display
	auth       Authenticator   // nil when token auth is unavailable; gates /api/v1 + /mcp
	emitter    *events.Emitter // nil when events are disabled; records + streams events
	logger     *slog.Logger
	version    string
	mcpEnabled bool
	dashboard  http.Handler
	updates    UpdateChecker // nil when update checking is disabled
	allowApply bool
	restart    func()
	pluginMgr  *plugins.Manager // nil when the plugin system is disabled
	oauth      *oauthStates     // pending source OAuth flows (anti-CSRF state)
}

// Options configures a Server.
type Options struct {
	Store      db.Store
	Syncer     Syncer
	Logger     *slog.Logger
	Version    string
	MCPEnabled bool
	// Sources, when non-nil, enables the /api/v1/sources endpoints (list sources,
	// per-source sync, credential, and OAuth), the back-compat
	// PUT /api/v1/simplefin/credential alias, and the connected status in
	// GET /api/v1/config.
	Sources SourceManager
	// Config, when non-nil, is exposed (with secrets redacted) by
	// GET /api/v1/config to power the dashboard's read-only Settings view.
	Config *config.Config
	// Auth, when non-nil and reporting Required(), gates /api/v1 (except the open
	// /api/v1/auth status endpoint) and the MCP-over-HTTP server behind the
	// dashboard token, and powers the token-management endpoints.
	Auth Authenticator
	// Emitter, when non-nil, records events for mutations made through the API
	// (label edits, rule changes) and exposes its bus for the SSE stream. Nil when
	// events are disabled (events.enabled=false).
	Emitter *events.Emitter
	// Dashboard, when non-nil, serves the web UI as the catch-all route.
	Dashboard http.Handler
	// UpdateChecker, when non-nil, enables GET /api/v1/update (status for the
	// dashboard banner). When AllowApply is also set, POST /api/v1/update lets
	// the UI trigger an in-place self-update.
	UpdateChecker UpdateChecker
	AllowApply    bool
	// Restart, when set, is invoked after a successful UI-triggered update to
	// re-exec the new binary. nil leaves the (old) process running.
	Restart func()
	// PluginManager, when non-nil, enables the plugin management endpoints
	// (REST + MCP). Nil when the plugin system is disabled (plugins.enabled=false
	// or events disabled), in which case those routes/tools are not registered.
	PluginManager *plugins.Manager
}

// New constructs a Server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store:      opts.Store,
		syncer:     opts.Syncer,
		sources:    opts.Sources,
		config:     opts.Config,
		auth:       opts.Auth,
		emitter:    opts.Emitter,
		logger:     logger,
		version:    opts.Version,
		mcpEnabled: opts.MCPEnabled,
		dashboard:  opts.Dashboard,
		updates:    opts.UpdateChecker,
		allowApply: opts.AllowApply,
		restart:    opts.Restart,
		pluginMgr:  opts.PluginManager,
		oauth:      newOAuthStates(),
	}
}

// Router returns the fully configured HTTP handler.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	// Gzip JSON/HTML/JS/CSS responses. The WASM is already served pre-gzipped, so
	// it is skipped (it carries Content-Encoding), and text/event-stream is not in
	// chi's compressible set either, so this is safe to keep at the root even for
	// the SSE stream below.
	r.Use(middleware.Compress(5))

	// The SSE event stream is long-lived, so it must NOT be wrapped in the request
	// timeout (which cancels the request context after 60s and would tear the
	// stream down). It is a read surface (the dashboard token or any API key);
	// everything else is registered under the timeout group that follows.
	r.Group(func(r chi.Router) {
		r.Use(s.requireRead)
		r.Get("/api/v1/events/stream", s.handleEventStream)
	})

	r.Group(func(r chi.Router) {
		r.Use(middleware.Timeout(60 * time.Second))

		// Operational endpoints.
		r.Get("/healthz", s.handleHealth)
		r.Get("/readyz", s.handleReady)
		r.Handle("/metrics", promhttp.Handler())

		// REST API. Routes are split into three access tiers: read (the dashboard
		// token or any API key), write (the dashboard token or a read_write key), and
		// admin/provisioning (the dashboard token only — API keys are never accepted,
		// so a key cannot mint another key, manage webhooks, rotate the token, set the
		// SimpleFIN credential, or trigger a self-update). All tiers are open when no
		// token is configured.
		r.Route("/api/v1", func(r chi.Router) {
			// Auth status is intentionally open (no token required) so the dashboard
			// can learn whether to show a login screen before it holds a token.
			r.Get("/auth", s.handleAuthStatus)

			// The OAuth callback is open: the provider redirects the browser here
			// with no Authorization header. It is protected by the unguessable
			// state value issued by the admin-gated /oauth/start, which it verifies.
			if s.sources != nil {
				r.Get("/sources/{type}/oauth/callback", s.handleSourceOAuthCallback)
			}

			// Read tier.
			r.Group(func(r chi.Router) {
				r.Use(s.requireRead)

				r.Get("/organizations", s.handleListOrganizations)

				r.Get("/accounts", s.handleListAccounts)
				r.Get("/accounts/{id}", s.handleGetAccount)
				r.Get("/accounts/{id}/transactions", s.handleListAccountTransactions)

				r.Get("/transactions", s.handleListTransactions)
				// Static /search is registered before /{id} so it isn't captured as a
				// transaction id (chi prefers static segments, but keep it explicit).
				r.Get("/transactions/search", s.handleSearchTransactions)
				r.Get("/transactions/{id}", s.handleGetTransaction)
				r.Get("/transactions/{id}/history", s.handleGetTransactionHistory)
				r.Get("/transactions/{id}/provenance", s.handleGetTransactionProvenance)
				r.Get("/transactions/{id}/relationships", s.handleGetTransactionRelationships)

				r.Get("/labels", s.handleListLabels)
				r.Get("/extensions", s.handleListExtensions)
				r.Get("/relationships", s.handleListRelationships)

				r.Get("/rules", s.handleListRules)
				r.Get("/rules/{id}", s.handleGetRule)

				// Plugin metadata/health is a read; enabling/reloading (which executes
				// code) is admin-only, registered in the admin tier below. These reads
				// are registered even when the plugin system is disabled (pluginMgr is
				// nil) so the dashboard's Plugins page gets a clean "disabled" response
				// instead of a routing 404.
				r.Get("/plugins", s.handleListPlugins)
				r.Get("/plugins/{id}", s.handleGetPlugin)

				// Canonical event stream (poll/cursor). The live SSE tail is the
				// separate /events/stream route above; chi prefers the static
				// "stream" segment over the {sequence} param, but order is explicit.
				r.Get("/events", s.handleListEvents)
				r.Get("/events/{sequence}", s.handleGetEvent)

				r.Get("/sync", s.handleSyncStatus)
				r.Get("/sync/history", s.handleSyncHistory)

				// Ingestion sources: list each source with its readiness and
				// credential shape. Registered even when source management is
				// unavailable so the dashboard gets a clean response, not a 404.
				r.Get("/sources", s.handleListSources)

				// Read-only effective configuration (secrets redacted) for the Settings page.
				r.Get("/config", s.handleGetConfig)

				if s.updates != nil {
					r.Get("/update", s.handleUpdateStatus)
				}
			})

			// Write tier.
			r.Group(func(r chi.Router) {
				r.Use(s.requireWrite)

				// Manual transaction CRUD. Create/edit/delete of core fields is
				// gated to manually-created rows (source="manual"); synced rows are
				// bridge-owned (409). Different methods than the read-tier
				// /transactions and /transactions/{id} routes, so they coexist.
				r.Post("/transactions", s.handleCreateTransaction)
				r.Put("/transactions/{id}", s.handleUpdateTransaction)
				r.Delete("/transactions/{id}", s.handleDeleteTransaction)

				// Manual account CRUD (same manual-only gate). Deleting an account
				// cascades to its transactions.
				r.Post("/accounts", s.handleCreateAccount)
				r.Put("/accounts/{id}", s.handleUpdateAccount)
				r.Delete("/accounts/{id}", s.handleDeleteAccount)

				r.Put("/transactions/{id}/labels", s.handleUpdateTransactionLabels)
				r.Delete("/labels/{key}", s.handleDeleteLabel)

				r.Put("/transactions/{id}/extensions", s.handleUpdateTransactionExtensions)

				r.Post("/transactions/{id}/relationships", s.handleCreateTransactionRelationship)
				r.Delete("/transactions/{id}/relationships", s.handleDeleteTransactionRelationship)

				r.Post("/rules", s.handleCreateRule)
				// Static /rules/run is registered before /rules/{id} so it isn't captured
				// as a rule id (chi prefers static segments, but keep it explicit).
				r.Post("/rules/run", s.handleRunAllRules)
				r.Put("/rules/{id}", s.handleUpdateRule)
				r.Delete("/rules/{id}", s.handleDeleteRule)
				r.Post("/rules/{id}/run", s.handleRunRule)

				r.Post("/sync", s.handleTriggerSync)

				// Per-source sync (the global /sync above syncs every source).
				if s.sources != nil {
					r.Post("/sources/{type}/sync", s.handleSyncSource)
				}
			})

			// Admin / provisioning tier (dashboard token only).
			r.Group(func(r chi.Router) {
				r.Use(s.requireToken)

				// Per-source credential management: set a pasted credential, and
				// begin the browser OAuth flow (which returns the consent URL). The
				// PUT /simplefin/credential alias is kept for back-compat.
				if s.sources != nil {
					r.Put("/sources/{type}/credential", s.handleSetSourceCredential)
					r.Delete("/sources/{type}/credentials/{id}", s.handleRemoveSourceCredential)
					r.Get("/sources/{type}/oauth/start", s.handleSourceOAuthStart)
					r.Put("/simplefin/credential", s.handleSetSimpleFINCredential)
				}

				// Dashboard token management (generate/set, and revoke). Available only
				// when an Authenticator is wired; refused when the token is config-managed.
				if s.auth != nil {
					r.Post("/security/token", s.handleSetToken)
					r.Delete("/security/token", s.handleClearToken)
				}

				// API key provisioning: mint (returns the secret once), list metadata,
				// and revoke per-consumer credentials.
				r.Post("/security/api-keys", s.handleCreateApiKey)
				r.Get("/security/api-keys", s.handleListApiKeys)
				r.Delete("/security/api-keys/{id}", s.handleRevokeApiKey)

				// Webhook management: register endpoints, edit/rotate/test, and delete.
				r.Get("/webhooks", s.handleListWebhooks)
				r.Post("/webhooks", s.handleCreateWebhook)
				// Static /webhooks subpaths are fine alongside /{id} (chi prefers static).
				r.Get("/webhooks/{id}", s.handleGetWebhook)
				r.Put("/webhooks/{id}", s.handleUpdateWebhook)
				r.Delete("/webhooks/{id}", s.handleDeleteWebhook)
				r.Post("/webhooks/{id}/test", s.handleTestWebhook)
				r.Post("/webhooks/{id}/rotate-secret", s.handleRotateWebhookSecret)

				// Plugin lifecycle: enabling/reloading loads and runs third-party code,
				// so it is admin-only (an API key can never enable a plugin).
				if s.pluginMgr != nil {
					r.Post("/plugins/{id}/enable", s.handleEnablePlugin)
					r.Post("/plugins/{id}/disable", s.handleDisablePlugin)
					r.Post("/plugins/{id}/reload", s.handleReloadPlugin)
				}

				// Apply a self-update (status is in the read tier above).
				if s.updates != nil && s.allowApply {
					r.Post("/update", s.handleApplyUpdate)
				}
			})
		})

		// Built-in MCP server over streamable HTTP, gated by the same dashboard token.
		if s.mcpEnabled {
			r.Group(func(r chi.Router) {
				r.Use(s.requireToken)
				r.Mount("/mcp", s.MCPHandler())
			})
		}
	})

	// The web dashboard is the catch-all: it owns "/", client-side routes, and
	// its /web/* assets. The specific routes above (api, ops, mcp) match first.
	if s.dashboard != nil {
		r.NotFound(s.dashboard.ServeHTTP)
	}

	return r
}

// requestLogger logs each request with slog at debug level.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration", time.Since(start).String(),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}
