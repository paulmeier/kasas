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
)

// Syncer triggers an on-demand sync.
type Syncer interface {
	Sync(ctx context.Context) (poller.SyncResult, error)
}

// Server wires the HTTP handlers to their dependencies.
type Server struct {
	store      db.Store
	syncer     Syncer
	logger     *slog.Logger
	version    string
	mcpEnabled bool
}

// Options configures a Server.
type Options struct {
	Store      db.Store
	Syncer     Syncer
	Logger     *slog.Logger
	Version    string
	MCPEnabled bool
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
	}
}

// Router returns the fully configured HTTP handler.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(s.requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

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

		r.Get("/sync", s.handleSyncStatus)
		r.Get("/sync/history", s.handleSyncHistory)
		r.Post("/sync", s.handleTriggerSync)
	})

	// Built-in MCP server over streamable HTTP.
	if s.mcpEnabled {
		r.Mount("/mcp", s.MCPHandler())
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
