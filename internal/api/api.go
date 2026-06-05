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

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/selfupdate"
)

// Syncer triggers an on-demand sync.
type Syncer interface {
	Sync(ctx context.Context) (poller.SyncResult, error)
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
	r.Use(middleware.Timeout(60 * time.Second))
	// Gzip JSON/HTML/JS/CSS responses. The WASM is already served pre-gzipped,
	// so it is skipped (it already carries Content-Encoding).
	r.Use(middleware.Compress(5))

	// Operational endpoints.
	r.Get("/healthz", s.handleHealth)
	r.Get("/readyz", s.handleReady)
	r.Handle("/metrics", promhttp.Handler())

	// REST API.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/organizations", s.handleListOrganizations)

		r.Get("/accounts", s.handleListAccounts)
		r.Get("/accounts/{id}", s.handleGetAccount)
		r.Get("/accounts/{id}/transactions", s.handleListAccountTransactions)

		r.Get("/transactions", s.handleListTransactions)
		r.Get("/transactions/{id}", s.handleGetTransaction)
		r.Put("/transactions/{id}/tags", s.handleUpdateTransactionTags)

		r.Get("/tags", s.handleListTags)
		r.Delete("/tags/{name}", s.handleDeleteTag)

		r.Get("/sync", s.handleSyncStatus)
		r.Get("/sync/history", s.handleSyncHistory)
		r.Post("/sync", s.handleTriggerSync)

		// Update status for the dashboard banner, and (optionally) apply.
		if s.updates != nil {
			r.Get("/update", s.handleUpdateStatus)
			if s.allowApply {
				r.Post("/update", s.handleApplyUpdate)
			}
		}
	})

	// Built-in MCP server over streamable HTTP.
	if s.mcpEnabled {
		r.Mount("/mcp", s.MCPHandler())
	}

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
