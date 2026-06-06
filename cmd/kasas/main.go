// Command kasas syncs SimpleFIN financial data into a local SQLite database
// and serves it over a REST API and a built-in MCP server.
//
// Usage:
//
//	kasas [-config path] [command]
//
// Commands:
//
//	serve        (default) run the HTTP server and background sync scheduler
//	sync         run a single sync and exit
//	migrate      apply database migrations and exit
//	mcp          serve the MCP server over stdio (for desktop MCP clients)
//	healthcheck  probe the local /healthz endpoint (used by Docker HEALTHCHECK)
//	self-update  download and install the latest release (use -check to dry-run)
//	version      print the version and exit
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/auth"
	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/dashboard"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/selfupdate"
	"github.com/paulmeier/kasas/internal/vault"
	"github.com/paulmeier/kasas/migrations"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", os.Getenv("KASAS_CONFIG"), "path to TOML config file")
	flag.Parse()

	if err := run(flag.Arg(0), configPath); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(command, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log)
	slog.SetDefault(logger)

	if command == "version" {
		fmt.Printf("kasas %s\n", version)
		return nil
	}

	if command == "healthcheck" {
		return healthcheck(cfg.Server.Addr)
	}

	if command == "self-update" {
		return selfUpdate(flag.Args()[1:], cfg.Update, logger)
	}

	database, err := openDB(cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()

	if err := runMigrations(database, cfg.Database.Driver); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	store := newStore(cfg.Database.Driver, database)

	secrets, err := vault.New(vault.Config{
		Enabled:      cfg.Vault.Enabled,
		Address:      cfg.Vault.Address,
		Token:        cfg.Vault.Token,
		Mount:        cfg.Vault.Mount,
		Path:         cfg.Vault.Path,
		AccessURLKey: cfg.Vault.AccessURLKey,
	}, cfg.Secrets.File)
	if err != nil {
		return fmt.Errorf("init secret store: %w", err)
	}

	// Dashboard token guard: a config/env token is authoritative; otherwise a
	// token generated from the dashboard (stored alongside the SimpleFIN access
	// URL) applies. When none is set, the API/dashboard/MCP are unauthenticated.
	guard, err := auth.New(cfg.Dashboard.Token, secrets)
	if err != nil {
		return fmt.Errorf("init dashboard auth: %w", err)
	}

	p := poller.New(poller.Options{
		Store:           store,
		Secrets:         secrets,
		Logger:          logger,
		Interval:        cfg.Sync.Interval,
		LookbackDays:    cfg.Sync.LookbackDays,
		ConfigAccessURL: cfg.SimpleFIN.AccessURL,
		SetupToken:      cfg.SimpleFIN.SetupToken,
	})

	var dashboardHandler http.Handler
	if cfg.Dashboard.Enabled {
		dashboardHandler = dashboard.Handler(dashboard.Options{Version: version})
	}

	var updateChecker *selfupdate.Checker
	if cfg.Update.Check {
		updateChecker = selfupdate.NewChecker(selfupdate.Options{
			Repo:           cfg.Update.Repository,
			CurrentVersion: version,
		})
	}

	apiOpts := api.Options{
		Store:      store,
		Syncer:     p,
		Connector:  p,
		Config:     cfg,
		Auth:       guard,
		Logger:     logger,
		Version:    version,
		MCPEnabled: cfg.MCP.Enabled,
		Dashboard:  dashboardHandler,
	}
	// Assign the interface field only when non-nil to avoid a typed-nil that
	// would read as non-nil inside the Server.
	if updateChecker != nil {
		apiOpts.UpdateChecker = updateChecker
		apiOpts.AllowApply = cfg.Update.AllowApply
		apiOpts.Restart = restartIntoNewBinary(logger)
	}
	srv := api.New(apiOpts)

	switch command {
	case "", "serve":
		return serve(cfg, logger, p, srv, updateChecker, guard)
	case "migrate":
		logger.Info("migrations applied")
		return nil
	case "sync":
		_, err := p.Sync(context.Background())
		return err
	case "mcp":
		return srv.RunMCPStdio(context.Background())
	default:
		return fmt.Errorf("unknown command %q (want one of: serve, sync, migrate, mcp, healthcheck, self-update, version)", command)
	}
}

// serve runs the HTTP server plus the background sync scheduler until an
// interrupt or SIGTERM is received, then shuts down gracefully.
func serve(cfg *config.Config, logger *slog.Logger, p *poller.Poller, srv *api.Server, updateChecker *selfupdate.Checker, guard *auth.Guard) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !guard.Required() {
		logger.Warn("dashboard is NOT secured: no dashboard token is set — anyone who can reach kasas can read your financial data and change settings; set dashboard.token / KASAS_DASHBOARD_TOKEN, or generate a token from the dashboard Settings page")
	}

	if updateChecker != nil {
		go updateChecker.Run(ctx, logger, 24*time.Hour)
	}

	if cfg.Sync.Enabled {
		if err := p.Start(ctx); err != nil {
			return fmt.Errorf("start poller: %w", err)
		}
		defer func() {
			if err := p.Stop(context.Background()); err != nil {
				logger.Error("poller shutdown error", "error", err)
			}
		}()

		if cfg.Sync.RunOnStart {
			go func() {
				if _, err := p.Sync(ctx); err != nil {
					logger.Error("initial sync failed", "error", err)
				}
			}()
		}
	}

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("http server listening", "addr", cfg.Server.Addr, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server error", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, draining connections")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// selfUpdate checks GitHub for a newer release and, unless -check is passed,
// downloads and installs it over the running binary.
func selfUpdate(args []string, cfg config.Update, logger *slog.Logger) error {
	fs := flag.NewFlagSet("self-update", flag.ContinueOnError)
	checkOnly := fs.Bool("check", false, "report whether an update is available without installing it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rel, err := selfupdate.CheckLatest(ctx, selfupdate.Options{Repo: cfg.Repository, CurrentVersion: version})
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}

	// self-update moves between published releases; a dev/source build can't be
	// version-compared, so report rather than guess (and never clobber it).
	if !selfupdate.IsRelease(version) {
		fmt.Printf("current build %q is not a released version; latest release is %s\n%s\n", version, rel.Version, rel.URL)
		return nil
	}

	if !rel.IsNewerThan(version) {
		fmt.Printf("kasas %s is up to date (latest release: %s)\n", version, rel.Version)
		return nil
	}
	if *checkOnly {
		fmt.Printf("update available: %s -> %s\n%s\n", version, rel.Version, rel.URL)
		return nil
	}

	fmt.Printf("updating kasas %s -> %s ...\n", version, rel.Version)
	if err := selfupdate.Apply(ctx, rel, selfupdate.ApplyOptions{Logger: logger}); err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	fmt.Printf("updated to %s; restart kasas to run the new version\n", rel.Version)
	return nil
}

// restartIntoNewBinary returns a hook the API calls after a dashboard-triggered
// self-update: it re-execs the (now replaced) binary so the new version runs
// without an external supervisor. The re-exec is delayed briefly so the HTTP
// response reaches the browser first; on failure the old process keeps serving.
func restartIntoNewBinary(logger *slog.Logger) func() {
	return func() {
		go func() {
			time.Sleep(750 * time.Millisecond) // let the apply response flush
			exe, err := os.Executable()
			if err != nil {
				logger.Error("auto-restart failed: locate executable", "error", err)
				return
			}
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}
			logger.Info("restarting into updated binary", "path", exe)
			if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
				logger.Error("auto-restart failed; restart kasas manually to run the new version", "error", err)
			}
		}()
	}
}

// healthcheck performs a GET against the local /healthz endpoint. It backs the
// container HEALTHCHECK, which cannot shell out in a scratch image.
func healthcheck(addr string) error {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck failed: status %d", resp.StatusCode)
	}
	return nil
}

// openDB opens the configured database backend.
func openDB(cfg config.Database) (*sql.DB, error) {
	switch cfg.Driver {
	case "postgres":
		return openPostgres(cfg.DSN)
	default:
		return openSQLite(cfg.Path)
	}
}

// openSQLite opens the SQLite database with WAL mode and sensible pragmas.
func openSQLite(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir %q: %w", dir, err)
		}
	}

	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(on)" +
		"&_pragma=synchronous(NORMAL)"

	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Writes are serialized at the application layer (the poller), and WAL mode
	// lets readers proceed concurrently, so a small pool is plenty.
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(4)
	database.SetConnMaxIdleTime(5 * time.Minute)

	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

// openPostgres opens a Postgres connection pool via the pgx stdlib driver.
func openPostgres(dsn string) (*sql.DB, error) {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(5)
	database.SetConnMaxIdleTime(5 * time.Minute)

	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	return database, nil
}

// newStore wraps the open database in the matching Store implementation.
func newStore(driver string, database *sql.DB) db.Store {
	if driver == "postgres" {
		return db.NewPostgresStore(database)
	}
	return db.NewSQLiteStore(database)
}

// runMigrations applies the embedded goose migrations for the active dialect.
func runMigrations(database *sql.DB, driver string) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())

	dialect, dir := "sqlite3", "sqlite"
	if driver == "postgres" {
		dialect, dir = "postgres", "postgres"
	}
	if err := goose.SetDialect(dialect); err != nil {
		return err
	}
	return goose.UpContext(context.Background(), database, dir)
}

func newLogger(cfg config.Log) *slog.Logger {
	var level slog.Level
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(cfg.Format) == "text" {
		handler = slog.NewTextHandler(os.Stderr, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}
