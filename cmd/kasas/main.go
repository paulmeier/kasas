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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/selfupdate"
	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/sources/bitcoin"
	"github.com/paulmeier/kasas/internal/sources/csv"
	"github.com/paulmeier/kasas/internal/sources/ethereum"
	"github.com/paulmeier/kasas/internal/sources/plaid"
	"github.com/paulmeier/kasas/internal/sources/simplefin"
	"github.com/paulmeier/kasas/internal/sources/teller"
	"github.com/paulmeier/kasas/internal/vault"
	"github.com/paulmeier/kasas/internal/webhooks"
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

	// Event stream: a bus fans live events out to SSE subscribers, and an emitter
	// records each change transactionally and publishes it after commit. Both stay
	// nil when events are disabled, which makes the poller and API emit nothing.
	var (
		eventBus *events.Bus
		emitter  *events.Emitter
	)
	if cfg.Events.Enabled {
		eventBus = events.NewBus()
		emitter = events.NewEmitter(eventBus)
	}

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

	// Build the configured ingestion sources. Each source registers itself (via its
	// package import) and is constructed by type through the registry; the engine
	// then drives one poller per source. SimpleFIN is always built (it surfaces its
	// own "not configured" error on sync); CSV is built only when folders are
	// configured. Additional sources plug in here by registering under their type.
	engine, err := buildEngine(cfg, store, secrets, emitter, logger)
	if err != nil {
		return err
	}

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

	// Plugin manager: loads plugins from disk and runs them in a sandboxed VM,
	// reacting to committed events off the bus (so it needs events enabled, like the
	// webhook dispatcher). A nil manager is a no-op; the API server holds it to
	// expose plugin management across REST/MCP/dashboard.
	var pluginManager *plugins.Manager
	if cfg.Plugins.Enabled && eventBus != nil {
		pluginManager = plugins.NewManager(plugins.Options{
			Store:   store,
			Emitter: emitter,
			Bus:     eventBus,
			Dir:     cfg.Plugins.Dir,
			Runtimes: map[string]plugins.Runtime{
				plugins.RuntimeLua: plugins.NewLuaRuntime(),
				plugins.RuntimeJS:  plugins.NewJSRuntime(),
			},
			HookTimeout: cfg.Plugins.HookTimeout,
			QueueSize:   cfg.Plugins.QueueSize,
			Logger:      logger,
		})
	}

	apiOpts := api.Options{
		Store:      store,
		Syncer:     engine,
		Sources:    engine,
		Config:     cfg,
		Auth:       guard,
		Emitter:    emitter,
		Logger:     logger,
		Version:    version,
		MCPEnabled: cfg.MCP.Enabled,
		Dashboard:  dashboardHandler,
		// nil when the plugin system is disabled; gates the plugin REST/MCP surface.
		PluginManager: pluginManager,
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
		return serve(cfg, logger, engine, srv, updateChecker, guard, store, eventBus, pluginManager)
	case "migrate":
		logger.Info("migrations applied")
		return nil
	case "sync":
		_, err := engine.Sync(context.Background())
		return err
	case "mcp":
		return srv.RunMCPStdio(context.Background())
	default:
		return fmt.Errorf("unknown command %q (want one of: serve, sync, migrate, mcp, healthcheck, self-update, version)", command)
	}
}

// serve runs the HTTP server plus the background sync scheduler until an
// interrupt or SIGTERM is received, then shuts down gracefully.
func serve(cfg *config.Config, logger *slog.Logger, engine *poller.Engine, srv *api.Server, updateChecker *selfupdate.Checker, guard *auth.Guard, store db.Store, eventBus *events.Bus, pluginManager *plugins.Manager) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !guard.Required() {
		logger.Warn("dashboard is NOT secured: no dashboard token is set — anyone who can reach kasas can read your financial data and change settings; set dashboard.token / KASAS_DASHBOARD_TOKEN, or generate a token from the dashboard Settings page")
	}

	if updateChecker != nil {
		go updateChecker.Run(ctx, logger, 24*time.Hour)
	}

	// Prune old events on a schedule when a finite retention is configured.
	if eventBus != nil && cfg.Events.RetentionDays > 0 {
		go pruneEvents(ctx, logger, store, cfg.Events.RetentionDays)
	}

	// Prune old transaction-history snapshots on a schedule when a finite history
	// retention is configured. History recording rides on the event emitter (created
	// alongside the bus when events are enabled), so the bus being non-nil gates it.
	if eventBus != nil && cfg.Events.HistoryRetentionDays > 0 {
		go pruneTransactionVersions(ctx, logger, store, cfg.Events.HistoryRetentionDays)
	}

	// Webhook dispatcher: push committed events to registered endpoints, HMAC-signed.
	// It rides the event bus (so it needs events enabled) and is best-effort —
	// consumers reconcile any gap via the /events cursor. ctx cancellation stops it,
	// and closing the bus on shutdown unblocks its subscription.
	if eventBus != nil && cfg.Webhooks.Enabled {
		dispatcher := webhooks.NewDispatcher(store, eventBus, webhooks.Options{
			Timeout:     cfg.Webhooks.Timeout,
			MaxAttempts: cfg.Webhooks.MaxAttempts,
			UserAgent:   "kasas/" + version,
		}, logger)
		go dispatcher.Run(ctx)
		logger.Info("webhook dispatcher started", "timeout", cfg.Webhooks.Timeout.String(), "max_attempts", cfg.Webhooks.MaxAttempts)
	}

	// Plugin manager: discovers plugins, loads the enabled ones, and runs their
	// hooks against committed events. Like the dispatcher it rides the event bus and
	// stops when ctx is cancelled / the bus closes on shutdown.
	if pluginManager != nil {
		go pluginManager.Run(ctx)
		logger.Info("plugin manager started", "dir", cfg.Plugins.Dir, "hook_timeout", cfg.Plugins.HookTimeout.String())
	}

	if cfg.Sync.Enabled {
		if err := engine.Start(ctx); err != nil {
			return fmt.Errorf("start ingestion engine: %w", err)
		}
		defer func() {
			if err := engine.Stop(context.Background()); err != nil {
				logger.Error("ingestion engine shutdown error", "error", err)
			}
		}()

		if cfg.Sync.RunOnStart {
			go func() {
				if _, err := engine.Sync(ctx); err != nil {
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

	// Close the event bus first so live SSE subscribers unblock and their handlers
	// return, letting the HTTP server drain instead of waiting out the grace period.
	if eventBus != nil {
		eventBus.Close()
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}

// pruneEvents periodically deletes events older than retentionDays, running once
// at startup and then on a fixed interval until ctx is cancelled. It is only
// started when a finite retention is configured (events.retention_days > 0);
// the default of 0 keeps the stream fully replayable forever.
func pruneEvents(ctx context.Context, logger *slog.Logger, store db.Store, retentionDays int) {
	const interval = 6 * time.Hour
	prune := func() {
		cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
		n, err := store.DeleteEventsBefore(ctx, cutoff)
		if err != nil {
			logger.Error("event retention prune failed", "error", err)
			return
		}
		if n > 0 {
			logger.Info("pruned old events", "removed", n, "older_than_days", retentionDays)
		}
	}

	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// pruneTransactionVersions periodically deletes transaction-history snapshots older
// than retentionDays, mirroring pruneEvents. It is only started when a finite
// history retention is configured (events.history_retention_days > 0); the default
// of 0 keeps every transaction's full history forever. It prunes the oldest
// versions first, so a truncated timeline begins at the oldest surviving snapshot.
func pruneTransactionVersions(ctx context.Context, logger *slog.Logger, store db.Store, retentionDays int) {
	const interval = 6 * time.Hour
	prune := func() {
		cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
		n, err := store.DeleteTransactionVersionsBefore(ctx, cutoff)
		if err != nil {
			logger.Error("transaction history retention prune failed", "error", err)
			return
		}
		if n > 0 {
			logger.Info("pruned old transaction versions", "removed", n, "older_than_days", retentionDays)
		}
	}

	prune()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
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

// buildEngine constructs the ingestion engine: one poller per configured source.
// SimpleFIN is always built (it reports "not connected" and is skipped on sync
// until a credential is set, so a setup that uses only another source is quiet); the
// CSV file-import source is built only when folders are configured, Teller when an
// access token or certificate is set, Plaid when its app credentials are set, Bitcoin
// when an address or custom api_url is set, and Ethereum when its Etherscan API key is
// set. Each source is constructed by type through the registry, resolving its
// credentials/config from the secret store and config via the env.
func buildEngine(cfg *config.Config, store db.Store, secrets vault.SecretStore, emitter *events.Emitter, logger *slog.Logger) (*poller.Engine, error) {
	newPoller := func(typ string, opts map[string]string) (*poller.Poller, error) {
		src, err := source.New(typ, source.Env{Logger: logger, Secrets: secrets, Options: opts})
		if err != nil {
			return nil, fmt.Errorf("init %s source: %w", typ, err)
		}
		return poller.New(poller.Options{
			Store:        store,
			Source:       src,
			Logger:       logger,
			Emitter:      emitter,
			Interval:     cfg.Sync.Interval,
			LookbackDays: cfg.Sync.LookbackDays,
		}), nil
	}

	sfin, err := newPoller(simplefin.SourceType, map[string]string{
		"access_url":  cfg.SimpleFIN.AccessURL,
		"setup_token": cfg.SimpleFIN.SetupToken,
	})
	if err != nil {
		return nil, err
	}
	pollers := []*poller.Poller{sfin}

	// The CSV source carries a structured config (folder profiles), so it is passed
	// as JSON through the registry's flat options map.
	if len(cfg.CSV.Folders) > 0 {
		raw, err := json.Marshal(cfg.CSV)
		if err != nil {
			return nil, fmt.Errorf("encode csv config: %w", err)
		}
		csvPoller, err := newPoller(csv.SourceType, map[string]string{"config": string(raw)})
		if err != nil {
			return nil, err
		}
		pollers = append(pollers, csvPoller)
		logger.Info("csv file-import source enabled", "folders", len(cfg.CSV.Folders))
	}

	// Teller is a pull source like SimpleFIN, but each access token is one bank
	// enrollment, so it fans out over a list. Merge the singular access_token with
	// the access_tokens array (the source deduplicates) and pass them newline-joined.
	// Build it when any token or a client certificate is configured (the certificate
	// is its development/production mTLS credential); more tokens can then be added at
	// runtime from the Sources page. Like SimpleFIN, it is skipped until a token exists.
	tellerTokens := cfg.Teller.AccessTokens
	if cfg.Teller.AccessToken != "" {
		tellerTokens = append([]string{cfg.Teller.AccessToken}, tellerTokens...)
	}
	if len(tellerTokens) > 0 || cfg.Teller.Certificate != "" {
		tellerPoller, err := newPoller(teller.SourceType, map[string]string{
			"access_tokens": strings.Join(tellerTokens, "\n"),
			"certificate":   cfg.Teller.Certificate,
			"private_key":   cfg.Teller.PrivateKey,
		})
		if err != nil {
			return nil, err
		}
		pollers = append(pollers, tellerPoller)
		logger.Info("teller source enabled", "enrollments", len(tellerTokens))
	}

	// Plaid is a pull source like Teller: each access token is one linked Item (bank),
	// so it fans out over a list. The app-level client_id/secret/environment are set in
	// config; per-bank access tokens come from config and/or the Sources page at
	// runtime. Build it only when the app credentials are present (without them it can
	// do nothing); like the others it is then skipped until at least one token exists.
	if cfg.Plaid.ClientID != "" && cfg.Plaid.Secret != "" {
		plaidTokens := cfg.Plaid.AccessTokens
		if cfg.Plaid.AccessToken != "" {
			plaidTokens = append([]string{cfg.Plaid.AccessToken}, plaidTokens...)
		}
		plaidPoller, err := newPoller(plaid.SourceType, map[string]string{
			"client_id":     cfg.Plaid.ClientID,
			"secret":        cfg.Plaid.Secret,
			"environment":   cfg.Plaid.Environment,
			"country_codes": strings.Join(cfg.Plaid.CountryCodes, ","),
			"access_tokens": strings.Join(plaidTokens, "\n"),
		})
		if err != nil {
			return nil, err
		}
		pollers = append(pollers, plaidPoller)
		logger.Info("plaid source enabled", "environment", cfg.Plaid.Environment, "items", len(plaidTokens))
	}

	// Bitcoin watches public addresses with no API key (mempool.space). Each address is
	// one watched account, so it fans out over a list. Build it when any address or a
	// custom api_url is set (the api_url override is the "manage addresses from the
	// dashboard only" enable path); more addresses can be added at runtime. Like the
	// others, it is skipped until at least one address exists.
	btcAddrs := cfg.Bitcoin.Addresses
	if cfg.Bitcoin.Address != "" {
		btcAddrs = append([]string{cfg.Bitcoin.Address}, btcAddrs...)
	}
	if len(btcAddrs) > 0 || cfg.Bitcoin.APIURL != "" {
		btcPoller, err := newPoller(bitcoin.SourceType, map[string]string{
			"addresses": strings.Join(btcAddrs, "\n"),
			"api_url":   cfg.Bitcoin.APIURL,
		})
		if err != nil {
			return nil, err
		}
		pollers = append(pollers, btcPoller)
		logger.Info("bitcoin source enabled", "addresses", len(btcAddrs))
	}

	// Ethereum watches addresses via Etherscan, which requires a (free) API key shared
	// across addresses; build it only when the key is present (without it the source can
	// do nothing). Addresses come from config and/or the dashboard, and it fans out over
	// them; like the others it is then skipped until at least one address exists.
	if cfg.Ethereum.APIKey != "" {
		ethAddrs := cfg.Ethereum.Addresses
		if cfg.Ethereum.Address != "" {
			ethAddrs = append([]string{cfg.Ethereum.Address}, ethAddrs...)
		}
		ethPoller, err := newPoller(ethereum.SourceType, map[string]string{
			"api_key":   cfg.Ethereum.APIKey,
			"api_url":   cfg.Ethereum.APIURL,
			"chain_id":  strconv.Itoa(cfg.Ethereum.ChainID),
			"addresses": strings.Join(ethAddrs, "\n"),
		})
		if err != nil {
			return nil, err
		}
		pollers = append(pollers, ethPoller)
		logger.Info("ethereum source enabled", "chain_id", cfg.Ethereum.ChainID, "addresses", len(ethAddrs))
	}

	return poller.NewEngine(pollers...), nil
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
