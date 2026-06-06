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
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/selfupdate"
)

// Syncer triggers an on-demand sync.
type Syncer interface {
	Sync(ctx context.Context) (poller.SyncResult, error)
}

// Connector manages the SimpleFIN connection credential at runtime. Implemented
// by *poller.Poller. When provided, the Settings page can set the credential and
// the config endpoint can report whether kasas is connected.
type Connector interface {
	// SetCredential stores a SimpleFIN setup token or access URL for future syncs.
	SetCredential(ctx context.Context, input string) error
	// CredentialConfigured reports whether an access URL is currently stored.
	CredentialConfigured(ctx context.Context) (bool, error)
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
	connector  Connector       // nil when runtime credential management is unavailable
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
}

// Options configures a Server.
type Options struct {
	Store      db.Store
	Syncer     Syncer
	Logger     *slog.Logger
	Version    string
	MCPEnabled bool
	// Connector, when non-nil, enables PUT /api/v1/simplefin/credential (set the
	// SimpleFIN token/access URL) and the connected status in GET /api/v1/config.
	Connector Connector
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
		connector:  opts.Connector,
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

				r.Get("/labels", s.handleListLabels)
				r.Get("/extensions", s.handleListExtensions)

				r.Get("/rules", s.handleListRules)
				r.Get("/rules/{id}", s.handleGetRule)

				// Canonical event stream (poll/cursor). The live SSE tail is the
				// separate /events/stream route above; chi prefers the static
				// "stream" segment over the {sequence} param, but order is explicit.
				r.Get("/events", s.handleListEvents)
				r.Get("/events/{sequence}", s.handleGetEvent)

				r.Get("/sync", s.handleSyncStatus)
				r.Get("/sync/history", s.handleSyncHistory)

				// Read-only effective configuration (secrets redacted) for the Settings page.
				r.Get("/config", s.handleGetConfig)

				if s.updates != nil {
					r.Get("/update", s.handleUpdateStatus)
				}
			})

			// Write tier.
			r.Group(func(r chi.Router) {
				r.Use(s.requireWrite)

				r.Put("/transactions/{id}/labels", s.handleUpdateTransactionLabels)
				r.Delete("/labels/{key}", s.handleDeleteLabel)

				r.Put("/transactions/{id}/extensions", s.handleUpdateTransactionExtensions)

				r.Post("/rules", s.handleCreateRule)
				// Static /rules/run is registered before /rules/{id} so it isn't captured
				// as a rule id (chi prefers static segments, but keep it explicit).
				r.Post("/rules/run", s.handleRunAllRules)
				r.Put("/rules/{id}", s.handleUpdateRule)
				r.Delete("/rules/{id}", s.handleDeleteRule)
				r.Post("/rules/{id}/run", s.handleRunRule)

				r.Post("/sync", s.handleTriggerSync)
			})

			// Admin / provisioning tier (dashboard token only).
			r.Group(func(r chi.Router) {
				r.Use(s.requireToken)

				// Runtime-writable SimpleFIN credential.
				if s.connector != nil {
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
