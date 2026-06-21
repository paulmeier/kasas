package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// replayPage is how many events the gap-replay reads at a time (matches the
// webhook dispatcher and the SSE replay page size).
const replayPage = 500

var (
	// ErrDisabled is returned by the management methods when the plugin system is
	// off (a nil *Manager), so the API can hold a possibly-nil manager and map this
	// to a clear 503.
	ErrDisabled = errors.New("plugins: plugin system is disabled")
	// ErrPluginNotFound is returned for an unknown plugin id.
	ErrPluginNotFound = errors.New("plugins: plugin not found")
)

// Options configures a Manager.
type Options struct {
	Store       db.Store
	Emitter     *events.Emitter
	Bus         *events.Bus
	Dir         string             // directory scanned for plugin subdirectories
	Runtimes    map[string]Runtime // by manifest runtime name ("lua" -> luaRuntime)
	HookTimeout time.Duration      // per-hook invocation timeout
	QueueSize   int                // per-plugin job-queue depth
	SearchLimit int                // max results a plugin search may return
	NetLimits   NetLimits          // net:fetch egress caps (timeout/size/rate/redirects)
	Logger      *slog.Logger
	// Registry, when non-nil, enables the community-plugin marketplace: browsing a
	// published catalog and installing plugins into Dir. Nil leaves the marketplace
	// methods reporting ErrRegistryDisabled.
	Registry RegistrySource
}

// SourceRegistrar is the engine-facing seam a source:provide plugin (ADR 0005) uses
// to join and leave the ingestion engine at runtime: the manager calls RegisterSource
// when such a plugin loads and UnregisterSource when it unloads, and the
// implementation (wired in main) builds a pluginSource adapter + a poller and
// registers it with the engine. It is injected via SetSourceRegistrar; a nil
// registrar disables plugin sources (their hooks still load, but no source appears).
type SourceRegistrar interface {
	RegisterSource(ctx context.Context, name string, m Manifest) error
	UnregisterSource(ctx context.Context, name string) error
}

// providesSource reports whether a loaded plugin should be registered as an
// ingestion source: it declares a [source] block AND was actually granted the
// source:provide capability (a revoked grant leaves the source unregistered).
func providesSource(m Manifest, caps capSet) bool {
	return m.Source != nil && caps.has(CapSourceProvide)
}

// Manager loads plugins from disk, subscribes to the event bus, and routes each
// committed event to the per-plugin workers whose plugins declared the matching
// hook. It generalizes the webhook dispatcher: same subscribe / replay-on-drop /
// head-start lifecycle, but instead of one shared HTTP worker pool it gives each
// plugin its own goroutine + bounded queue, because a language VM is not
// reentrant and per-plugin serialization keeps a slow plugin from starving others.
type Manager struct {
	store       db.Store
	emitter     *events.Emitter
	bus         *events.Bus
	dir         string
	runtimes    map[string]Runtime
	timeout     time.Duration
	queueSize   int
	searchLimit int
	netLimits   NetLimits
	logger      *slog.Logger
	registry    RegistrySource  // nil when the marketplace is disabled
	sourceReg   SourceRegistrar // nil when plugin sources are disabled (ADR 0005)
	// egress is the in-memory, bounded record of every plugin net:fetch attempt,
	// surfaced read-only to the operator (REST/MCP/dashboard). It is process-scoped
	// observability, deliberately not a DB table (ADR 0002 #2 logging).
	egress *egressLog

	mu      sync.RWMutex
	plugins map[string]*plugin
	// baseCtx is the manager's lifetime context, recorded by Run. Per-plugin workers
	// run under it so a plugin enabled later via a short-lived HTTP request context
	// keeps running after that request returns.
	baseCtx context.Context
	wg      sync.WaitGroup
}

// NewManager constructs a Manager, applying defaults for the timing/sizing knobs.
func NewManager(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.HookTimeout <= 0 {
		opts.HookTimeout = 5 * time.Second
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = 256
	}
	if opts.SearchLimit <= 0 {
		opts.SearchLimit = defaultSearchLimit
	}
	if opts.Runtimes == nil {
		opts.Runtimes = map[string]Runtime{}
	}
	return &Manager{
		store:       opts.Store,
		emitter:     opts.Emitter,
		bus:         opts.Bus,
		dir:         opts.Dir,
		runtimes:    opts.Runtimes,
		timeout:     opts.HookTimeout,
		queueSize:   opts.QueueSize,
		searchLimit: opts.SearchLimit,
		netLimits:   opts.NetLimits.withDefaults(),
		logger:      opts.Logger,
		registry:    opts.Registry,
		egress:      newEgressLog(0),
		plugins:     map[string]*plugin{},
	}
}

// SetSourceRegistrar injects the engine-facing registrar used to register/unregister
// plugin ingestion sources (ADR 0005). It is called once during startup wiring,
// before Run, so the reconcile pass registers already-enabled source plugins. A nil
// *Manager is a no-op.
func (m *Manager) SetSourceRegistrar(r SourceRegistrar) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.sourceReg = r
	m.mu.Unlock()
}

// sourceRegistrar returns the injected registrar under the read lock.
func (m *Manager) sourceRegistrar() SourceRegistrar {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sourceReg
}

// Run loads enabled plugins, then consumes the event stream until ctx is
// cancelled, replaying from the durable log and resubscribing whenever the bus
// drops it (the webhook-dispatcher pattern). It blocks; run it in a goroutine. A
// nil *Manager is a no-op so the caller can skip starting it when disabled.
func (m *Manager) Run(ctx context.Context) {
	if m == nil {
		return
	}
	// Record the lifetime context before loading anything, so workers started now
	// (reconcile) or later (SetEnabled/Reload, on a request context that dies when the
	// HTTP handler returns) all run under a context that lives as long as the manager.
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()
	m.reconcile(ctx)
	defer m.shutdown()

	// Start at the current head so a restart does not replay the entire backlog.
	lastSeq := m.headSequence(ctx)
	for ctx.Err() == nil {
		sub, cancel := m.bus.Subscribe()
		m.replay(ctx, &lastSeq)
		m.consumeLive(ctx, sub, &lastSeq)
		cancel()
	}
}

func (m *Manager) headSequence(ctx context.Context) int64 {
	rows, err := m.store.ListRecentEvents(ctx, 1)
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rows[0].ID
}

func (m *Manager) consumeLive(ctx context.Context, sub <-chan events.Event, lastSeq *int64) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return // bus closed or this subscriber was dropped for lagging
			}
			if ev.Sequence <= *lastSeq {
				continue // already delivered during replay
			}
			m.dispatch(ev)
			*lastSeq = ev.Sequence
		}
	}
}

func (m *Manager) replay(ctx context.Context, lastSeq *int64) {
	for ctx.Err() == nil {
		rows, err := m.store.ListEventsAfter(ctx, db.ListEventsAfterParams{After: *lastSeq, RowLimit: replayPage})
		if err != nil {
			m.logger.Error("plugin replay read failed", "after", *lastSeq, "error", err)
			return
		}
		for _, row := range rows {
			m.dispatch(eventFromRow(row))
			*lastSeq = row.ID
		}
		if len(rows) < replayPage {
			return
		}
	}
}

// dispatch fans one event out to the per-plugin queues of every loaded plugin that
// declared the hook this event triggers. It decodes the event once (only if a
// plugin wants it) and enqueues non-blocking: a full queue drops the job (and
// meters it) rather than stalling the bus reader, since the durable log + replay
// backstop any gap.
func (m *Manager) dispatch(ev events.Event) {
	type target struct {
		p    *plugin
		hook Hook
	}
	m.mu.RLock()
	var targets []target
	for _, p := range m.plugins {
		if h, ok := p.triggers[ev.Type]; ok {
			targets = append(targets, target{p, h})
		}
	}
	m.mu.RUnlock()
	if len(targets) == 0 {
		return
	}

	he, ok := decodeHookEvent(ev)
	if !ok {
		return
	}
	for _, t := range targets {
		select {
		case t.p.jobs <- job{hook: t.hook, ev: he}:
		default:
			pluginJobsDropped.WithLabelValues(t.p.name).Inc()
			m.logger.Warn("plugin job dropped: queue full", "plugin", t.p.name, "event", ev.Type, "sequence", ev.Sequence)
		}
	}
}

// --- per-plugin worker ---

// startWorker runs a plugin's job loop. Hooks are invoked under the manager's
// lifetime context (workerCtx), NOT the context that happened to trigger the load:
// a plugin enabled through an HTTP request must keep running after that request's
// context is cancelled, or every hook would invoke under a dead context and silently
// no-op (the per-hook timeout would fire instantly).
func (m *Manager) startWorker(p *plugin) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer close(p.done)
		ctx := m.workerCtx()
		for j := range p.jobs {
			switch {
			case j.produce != nil:
				m.produce(ctx, p, j)
			case j.reply != nil:
				m.render(ctx, p, j)
			default:
				m.invoke(ctx, p, j)
			}
		}
	}()
}

// workerCtx returns the manager's lifetime context (set by Run), falling back to
// Background for a manager that was never Run (e.g. unit tests that dispatch directly).
func (m *Manager) workerCtx() context.Context {
	m.mu.RLock()
	c := m.baseCtx
	m.mu.RUnlock()
	if c == nil {
		return context.Background()
	}
	return c
}

// invoke runs one hook with the per-hook timeout, records health, and never lets a
// plugin failure propagate. The adapter recovers panics internally; this is the
// outer safety envelope.
func (m *Manager) invoke(ctx context.Context, p *plugin, j job) {
	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	err := p.inst.Invoke(cctx, j.hook, j.ev)
	pluginInvocations.WithLabelValues(p.name, string(j.hook)).Inc()

	if errors.Is(err, ErrHookNotImpl) {
		return // declared-but-unimplemented is rejected at load; ignore defensively
	}
	if err != nil && ctx.Err() != nil {
		return // shutting down; not a real failure
	}
	if err != nil {
		pluginErrors.WithLabelValues(p.name).Inc()
		m.logger.Warn("plugin hook failed", "plugin", p.name, "hook", j.hook, "error", err)
	}
	m.recordStatus(ctx, p, err)
}

// render runs one page job on the worker: it invokes the value-returning hook
// under the per-hook timeout, validates/normalizes the returned document, and
// answers on the job's reply channel. Health and metrics are recorded exactly
// like an event hook, so a page that errors shows up on the Plugins page.
func (m *Manager) render(ctx context.Context, p *plugin, j job) {
	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	raw, err := p.inst.Render(cctx, j.hook, *j.req)
	pluginInvocations.WithLabelValues(p.name, string(j.hook)).Inc()
	if err == nil {
		raw, err = ValidatePageDoc(raw)
	}
	// Mirror invoke's bookkeeping: an unimplemented hook is rejected at load (so
	// this is defensive) and a shutdown-cancelled run is not a plugin failure;
	// neither should flip the plugin's health.
	if err != nil && (errors.Is(err, ErrHookNotImpl) || ctx.Err() != nil) {
		j.reply <- renderReply{err: err}
		return
	}
	if err != nil {
		pluginErrors.WithLabelValues(p.name).Inc()
		m.logger.Warn("plugin page hook failed", "plugin", p.name, "hook", j.hook, "error", err)
	}
	m.recordStatus(ctx, p, err)
	j.reply <- renderReply{doc: raw, err: err} // buffered: never blocks the worker
}

// recordStatus persists the outcome of the latest run on the plugin row. It uses a
// cancellation-detached context so the health write still lands during shutdown,
// and advances last_success_at only on success (mirrors the webhook dispatcher).
func (m *Manager) recordStatus(ctx context.Context, p *plugin, runErr error) {
	now := time.Now().Unix()
	status := statusOK
	errMsg := ""
	if runErr != nil {
		status = statusError
		errMsg = truncate(runErr.Error(), maxErrorLen)
	} else {
		p.lastSuccessAt = now
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := m.store.UpdatePluginRunStatus(wctx, db.UpdatePluginRunStatusParams{
		ID:            p.id,
		LastStatus:    status,
		LastError:     errMsg,
		LastRunAt:     now,
		LastSuccessAt: p.lastSuccessAt,
	}); err != nil {
		m.logger.Warn("record plugin run status failed", "plugin", p.name, "error", err)
	}
}

// markError records a load failure on a plugin row that is not in the live set,
// preserving its last_success_at.
func (m *Manager) markError(ctx context.Context, id int64, loadErr error) {
	now := time.Now().Unix()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var success int64
	if row, err := m.store.GetPlugin(wctx, id); err == nil {
		success = row.LastSuccessAt
	}
	if err := m.store.UpdatePluginRunStatus(wctx, db.UpdatePluginRunStatusParams{
		ID:            id,
		LastStatus:    statusError,
		LastError:     truncate(loadErr.Error(), maxErrorLen),
		LastRunAt:     now,
		LastSuccessAt: success,
	}); err != nil {
		m.logger.Warn("record plugin load error failed", "plugin_id", id, "error", err)
	}
}

// --- discovery / reconcile / load ---

// reconcile registers every on-disk plugin in the DB and loads the enabled ones.
// It runs once at startup (and is safe to call again).
func (m *Manager) reconcile(ctx context.Context) {
	byName, rows, err := m.register(ctx)
	if err != nil {
		m.logger.Error("plugin reconcile failed", "error", err)
		return
	}
	for _, row := range rows {
		if row.Enabled != 1 {
			continue
		}
		d, ok := byName[row.Name]
		if !ok || !d.Valid() {
			m.logger.Warn("enabled plugin is not loadable", "plugin", row.Name)
			continue
		}
		if err := m.load(ctx, row, d); err != nil {
			m.logger.Error("load plugin failed", "plugin", row.Name, "error", err)
			m.markError(ctx, row.ID, err)
		}
	}
}

// register discovers on-disk plugins, inserts newly-found ones (disabled, with
// granted capabilities seeded from the manifest), refreshes manifest-derived
// fields on existing ones, and returns the discovered set by name plus the fresh
// DB rows.
func (m *Manager) register(ctx context.Context) (map[string]Discovered, []db.Plugin, error) {
	discovered := m.discover()
	byName := make(map[string]Discovered, len(discovered))
	for _, d := range discovered {
		byName[d.Name] = d
	}

	rows, err := m.store.ListPlugins(ctx)
	if err != nil {
		return nil, nil, err
	}
	existing := make(map[string]db.Plugin, len(rows))
	for _, r := range rows {
		existing[r.Name] = r
	}

	changed := false
	for _, d := range discovered {
		if !d.Valid() {
			continue // can't register a plugin whose manifest didn't parse
		}
		row, ok := existing[d.Name]
		if !ok {
			if _, err := m.insertDiscovered(ctx, d); err != nil {
				m.logger.Error("register plugin failed", "plugin", d.Name, "error", err)
				continue
			}
			changed = true
			continue
		}
		if m.refreshManifest(ctx, row, d) {
			changed = true
		}
	}
	if changed {
		if rows, err = m.store.ListPlugins(ctx); err != nil {
			return nil, nil, err
		}
	}
	return byName, rows, nil
}

func (m *Manager) discover() []Discovered {
	ds, err := Discover(m.dir)
	if err != nil {
		m.logger.Error("plugin discovery failed", "dir", m.dir, "error", err)
		return nil
	}
	for _, d := range ds {
		if d.Err != nil {
			m.logger.Warn("plugin has an invalid manifest", "plugin", d.Name, "error", d.Err)
		}
	}
	return ds
}

func (m *Manager) discoverByName(name string) (Discovered, bool) {
	for _, d := range m.discover() {
		if d.Name == name {
			return d, true
		}
	}
	return Discovered{}, false
}

func (m *Manager) insertDiscovered(ctx context.Context, d Discovered) (db.Plugin, error) {
	now := time.Now().Unix()
	return m.store.InsertPlugin(ctx, db.InsertPluginParams{
		Name:                d.Name,
		Runtime:             d.Manifest.Runtime,
		Version:             d.Manifest.Version,
		Enabled:             0,
		GrantedCapabilities: encodeCapList(d.Manifest.Capabilities),
		Config:              "{}",
		CreatedAt:           now,
		UpdatedAt:           now,
	})
}

// refreshManifest syncs the DB's manifest-derived fields with disk on
// re-discovery. v1 auto-grants the manifest's requested capabilities (the model is
// operator-trusted local plugins); the marketplace approval flow will replace this
// with an explicit grant. It returns whether anything changed.
func (m *Manager) refreshManifest(ctx context.Context, row db.Plugin, d Discovered) bool {
	now := time.Now().Unix()
	changed := false
	if row.Runtime != d.Manifest.Runtime || row.Version != d.Manifest.Version {
		if _, err := m.store.UpdatePluginManifest(ctx, db.UpdatePluginManifestParams{
			ID: row.ID, Runtime: d.Manifest.Runtime, Version: d.Manifest.Version, UpdatedAt: now,
		}); err != nil {
			m.logger.Error("refresh plugin manifest failed", "plugin", d.Name, "error", err)
		} else {
			changed = true
		}
	}
	if want := encodeCapList(d.Manifest.Capabilities); row.GrantedCapabilities != want {
		if _, err := m.store.UpdatePluginGrantedCapabilities(ctx, db.UpdatePluginGrantedCapabilitiesParams{
			ID: row.ID, GrantedCapabilities: want, UpdatedAt: now,
		}); err != nil {
			m.logger.Error("refresh plugin grants failed", "plugin", d.Name, "error", err)
		} else {
			changed = true
		}
	}
	return changed
}

// load instantiates a plugin and starts its worker, replacing any prior instance
// of the same name. The slow rt.Load runs without holding the lock; only the map
// mutation is locked.
func (m *Manager) load(ctx context.Context, row db.Plugin, d Discovered) error {
	rt, ok := m.runtimes[d.Manifest.Runtime]
	if !ok {
		return fmt.Errorf("no runtime registered for %q", d.Manifest.Runtime)
	}
	caps := intersectCaps(d.Manifest.Capabilities, decodeCapList(row.GrantedCapabilities))
	host := newHost(m.store, m.emitter, caps, row.Name, m.searchLimit, m.logger,
		newConfigStore(m.dir, row.Name, d.Manifest.Config), m.netGateFor(caps, row, d.Manifest))

	// The instance sees the EFFECTIVE config: manifest defaults overlaid with the
	// operator's <name>.config.toml overrides. A broken or mistyped override file
	// is a load error (surfaced as plugin health), like a broken manifest.
	man := d.Manifest
	eff, err := effectiveConfig(m.dir, row.Name, d.Manifest.Config)
	if err != nil {
		return fmt.Errorf("plugin config: %w", err)
	}
	man.Config = eff

	inst, err := rt.Load(ctx, man, d.Dir, host)
	if err != nil {
		return err
	}

	triggers := make(map[string]Hook, len(d.Manifest.Hooks))
	for _, h := range d.Manifest.Hooks {
		if et, ok := hookTrigger[h]; ok {
			triggers[et] = h
		}
	}
	p := &plugin{
		id:            row.ID,
		name:          row.Name,
		manifest:      d.Manifest,
		caps:          caps,
		inst:          inst,
		triggers:      triggers,
		jobs:          make(chan job, m.queueSize),
		done:          make(chan struct{}),
		lastSuccessAt: row.LastSuccessAt,
	}

	m.unload(row.Name) // stop any prior instance (reload), unregistering its source
	m.mu.Lock()
	m.plugins[row.Name] = p
	m.mu.Unlock()
	m.startWorker(p)
	m.logger.Info("plugin loaded", "plugin", row.Name, "runtime", d.Manifest.Runtime, "hooks", len(triggers), "capabilities", len(caps))

	// A source:provide plugin joins the ingestion engine as a first-class source
	// (ADR 0005): it then appears on the Sources page and syncs on the schedule. A
	// registration failure is logged but does not fail the load — the plugin's other
	// hooks still run.
	if reg := m.sourceRegistrar(); reg != nil && providesSource(p.manifest, p.caps) {
		if err := reg.RegisterSource(ctx, p.name, p.manifest); err != nil {
			m.logger.Error("register plugin source failed", "plugin", p.name, "error", err)
		}
	}
	return nil
}

// unload removes a plugin from the live set and stops it. Safe to call for a
// plugin that is not loaded.
func (m *Manager) unload(name string) {
	m.mu.Lock()
	p := m.plugins[name]
	delete(m.plugins, name)
	m.mu.Unlock()
	if p == nil {
		return
	}
	// Remove its ingestion source from the engine first (stops scheduling, so no sync
	// races the VM teardown), then stop the worker and close the VM.
	if reg := m.sourceRegistrar(); reg != nil && providesSource(p.manifest, p.caps) {
		if err := reg.UnregisterSource(context.Background(), p.name); err != nil {
			m.logger.Warn("unregister plugin source failed", "plugin", p.name, "error", err)
		}
	}
	m.stop(p)
}

// stop drains a plugin's queue, waits for its worker to finish, and closes the VM.
func (m *Manager) stop(p *plugin) {
	close(p.jobs)
	<-p.done
	if err := p.inst.Close(); err != nil {
		m.logger.Warn("plugin close error", "plugin", p.name, "error", err)
	}
}

// shutdown stops every loaded plugin: close all queues, wait for the workers, then
// close the VMs. Called from Run's defer after the bus loop exits, so no dispatch
// races it.
func (m *Manager) shutdown() {
	m.mu.Lock()
	ps := make([]*plugin, 0, len(m.plugins))
	for _, p := range m.plugins {
		ps = append(ps, p)
	}
	m.plugins = map[string]*plugin{}
	m.mu.Unlock()

	for _, p := range ps {
		close(p.jobs)
	}
	m.wg.Wait()
	for _, p := range ps {
		if err := p.inst.Close(); err != nil {
			m.logger.Warn("plugin close error", "plugin", p.name, "error", err)
		}
	}
}

// --- management API (used by REST + MCP) ---

// Status is the merged view of a plugin: its persistent DB state, its on-disk
// manifest, and whether it is currently loaded and running.
type Status struct {
	ID            int64
	Name          string
	Runtime       string
	Version       string
	Description   string
	Enabled       bool
	Loaded        bool
	OnDisk        bool
	State         string // loaded | disabled | error | missing
	Hooks         []Hook
	Requested     []Capability // capabilities the manifest requests
	Granted       []Capability // capabilities the operator/DB granted
	NetAllow      []string     // manifest-declared egress hosts ([net].allow)
	NetGrants     []string     // operator-granted private hosts (net:fetch)
	LastStatus    int64
	LastError     string
	LastRunAt     int64
	LastSuccessAt int64
}

// List returns every known plugin (on disk and/or in the DB), registering any
// newly-discovered ones so the list always reflects the plugins directory.
func (m *Manager) List(ctx context.Context) ([]Status, error) {
	if m == nil {
		return nil, ErrDisabled
	}
	byName, rows, err := m.register(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	loaded := make(map[string]bool, len(m.plugins))
	for name := range m.plugins {
		loaded[name] = true
	}
	m.mu.RUnlock()

	out := make([]Status, 0, len(rows))
	for _, row := range rows {
		out = append(out, statusOf(row, byName[row.Name], loaded[row.Name]))
	}
	return out, nil
}

// Get returns one plugin's merged status by id.
func (m *Manager) Get(ctx context.Context, id int64) (Status, error) {
	if m == nil {
		return Status{}, ErrDisabled
	}
	row, err := m.store.GetPlugin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrPluginNotFound
	}
	if err != nil {
		return Status{}, err
	}
	d, _ := m.discoverByName(row.Name)
	m.mu.RLock()
	_, loaded := m.plugins[row.Name]
	m.mu.RUnlock()
	return statusOf(row, d, loaded), nil
}

// SetEnabled enables or disables a plugin, loading or stopping it accordingly.
// Enabling executes the plugin's code, so the API gates this on the admin tier.
// netGrants, when non-nil, replaces the plugin's net:fetch private-host grants
// before it loads (each must be a host the manifest declares in [net].allow); a
// nil slice leaves the stored grants untouched. Grants are ignored when disabling.
func (m *Manager) SetEnabled(ctx context.Context, id int64, enabled bool, netGrants []string) (Status, error) {
	if m == nil {
		return Status{}, ErrDisabled
	}
	row, err := m.store.GetPlugin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrPluginNotFound
	}
	if err != nil {
		return Status{}, err
	}

	if enabled {
		d, ok := m.discoverByName(row.Name)
		if !ok || !d.Valid() {
			return Status{}, fmt.Errorf("plugin %q has no loadable manifest on disk", row.Name)
		}
		// Persist operator-supplied private-host grants BEFORE load, so the egress
		// gate built in load() reads the fresh row. A grant must name a host the
		// manifest declares; an unknown host is a clear error, not a silent no-op.
		if netGrants != nil {
			updated, gerr := m.persistNetGrants(ctx, row, d.Manifest, netGrants)
			if gerr != nil {
				return Status{}, gerr
			}
			row = updated
		}
		if _, err := m.store.SetPluginEnabled(ctx, db.SetPluginEnabledParams{ID: id, Enabled: 1, UpdatedAt: time.Now().Unix()}); err != nil {
			return Status{}, err
		}
		row.Enabled = 1
		if err := m.load(ctx, row, d); err != nil {
			m.markError(ctx, id, err)
			return Status{}, err
		}
	} else {
		if _, err := m.store.SetPluginEnabled(ctx, db.SetPluginEnabledParams{ID: id, Enabled: 0, UpdatedAt: time.Now().Unix()}); err != nil {
			return Status{}, err
		}
		m.unload(row.Name)
	}
	return m.Get(ctx, id)
}

// Reload re-reads a plugin from disk and reloads it if enabled (picking up code
// and manifest changes without a restart).
func (m *Manager) Reload(ctx context.Context, id int64) (Status, error) {
	if m == nil {
		return Status{}, ErrDisabled
	}
	row, err := m.store.GetPlugin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrPluginNotFound
	}
	if err != nil {
		return Status{}, err
	}

	d, ok := m.discoverByName(row.Name)
	if !ok || !d.Valid() {
		m.unload(row.Name)
		if ok && d.Err != nil {
			return Status{}, d.Err
		}
		return Status{}, fmt.Errorf("plugin %q not found on disk", row.Name)
	}
	m.refreshManifest(ctx, row, d)
	if row.Enabled != 1 {
		return m.Get(ctx, id) // disabled: metadata refreshed, nothing to load
	}
	if updated, gerr := m.store.GetPlugin(ctx, id); gerr == nil {
		row = updated // pick up refreshed grants before loading
	}
	if err := m.load(ctx, row, d); err != nil {
		m.markError(ctx, id, err)
		return Status{}, err
	}
	return m.Get(ctx, id)
}

// statusOf merges a DB row, its on-disk discovery, and its loaded state into a
// Status, deriving the coarse State string for the UI.
func statusOf(row db.Plugin, d Discovered, loaded bool) Status {
	s := Status{
		ID:            row.ID,
		Name:          row.Name,
		Runtime:       row.Runtime,
		Version:       row.Version,
		Enabled:       row.Enabled == 1,
		Loaded:        loaded,
		OnDisk:        d.Valid(),
		Granted:       decodeCapList(row.GrantedCapabilities),
		NetGrants:     decodeStringList(row.NetGrants),
		LastStatus:    row.LastStatus,
		LastError:     row.LastError,
		LastRunAt:     row.LastRunAt,
		LastSuccessAt: row.LastSuccessAt,
	}
	if d.Dir != "" && d.Err == nil {
		s.Description = d.Manifest.Description
		s.Hooks = d.Manifest.Hooks
		s.Requested = d.Manifest.Capabilities
		if d.Manifest.Net != nil {
			s.NetAllow = d.Manifest.Net.Allow
		}
		if d.Manifest.Runtime != "" {
			s.Runtime = d.Manifest.Runtime
		}
		if d.Manifest.Version != "" {
			s.Version = d.Manifest.Version
		}
	}
	switch {
	case d.Dir == "":
		s.State = "missing"
	case d.Err != nil:
		s.State = "error"
		if s.LastError == "" {
			s.LastError = d.Err.Error()
		}
	case loaded:
		s.State = "loaded"
	case row.Enabled == 1 && row.LastStatus == statusError:
		s.State = "error"
	default:
		s.State = "disabled"
	}
	return s
}

// --- net:fetch egress wiring (ADR 0002) ---

// netGateFor builds the egress gate for a plugin, or nil when the plugin was not
// granted net:fetch or declares no [net].allow list (so the host facade refuses
// kasas.fetch by construction). The gate binds the manifest allowlist, the
// operator's stored private-host grants, the host's egress caps, and the shared
// egress log.
func (m *Manager) netGateFor(caps capSet, row db.Plugin, man Manifest) *netGate {
	if !caps.has(CapNetFetch) || man.Net == nil || len(man.Net.Allow) == 0 {
		return nil
	}
	return newNetGate(row.Name, man.Net.Allow, decodeStringList(row.NetGrants), m.netLimits, m.egress, m.logger)
}

// persistNetGrants validates operator-supplied private-host grants against the
// manifest's declared allowlist and writes them to the plugin row, returning the
// updated row. A grant that names a host the manifest does not declare is rejected.
func (m *Manager) persistNetGrants(ctx context.Context, row db.Plugin, man Manifest, grants []string) (db.Plugin, error) {
	clean, err := validateNetGrants(man, grants)
	if err != nil {
		return db.Plugin{}, err
	}
	encoded := encodeStringList(clean)
	if encoded == row.NetGrants {
		return row, nil
	}
	if _, err := m.store.UpdatePluginNetGrants(ctx, db.UpdatePluginNetGrantsParams{
		ID: row.ID, NetGrants: encoded, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		return db.Plugin{}, err
	}
	row.NetGrants = encoded
	return row, nil
}

// validateNetGrants normalizes a grant list and checks every host is one the
// manifest declares in [net].allow. An empty list clears all grants.
func validateNetGrants(man Manifest, grants []string) ([]string, error) {
	if len(grants) == 0 {
		return nil, nil
	}
	if man.Net == nil || len(man.Net.Allow) == 0 {
		return nil, fmt.Errorf("plugin declares no [net].allow list; there are no hosts to grant")
	}
	allow := make(map[string]bool, len(man.Net.Allow))
	for _, h := range man.Net.Allow {
		allow[h] = true
	}
	seen := make(map[string]bool, len(grants))
	out := make([]string, 0, len(grants))
	for _, g := range grants {
		h, err := normalizeNetHost(g)
		if err != nil {
			return nil, err
		}
		if !allow[h] {
			return nil, fmt.Errorf("cannot grant host %q: it is not in the plugin's declared [net].allow list", g)
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out, nil
}

// EgressLog returns the most recent net:fetch egress entries for one plugin
// (newest first, up to limit). A disabled plugin system returns ErrDisabled; an
// unknown id returns ErrPluginNotFound.
func (m *Manager) EgressLog(ctx context.Context, id int64, limit int) ([]EgressEntry, error) {
	if m == nil {
		return nil, ErrDisabled
	}
	row, err := m.store.GetPlugin(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPluginNotFound
	}
	if err != nil {
		return nil, err
	}
	return m.egress.list(row.Name, limit), nil
}

// --- event decoding ---

func decodeHookEvent(ev events.Event) (HookEvent, bool) {
	he := HookEvent{Type: ev.Type, Sequence: ev.Sequence, EntityID: ev.EntityID, OccurredAt: ev.OccurredAt}
	switch ev.Type {
	case events.TypeTransactionCreated, events.TypeTransactionUpdated:
		var p events.TransactionPayload
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return HookEvent{}, false
		}
		t := transactionFromPayload(p)
		he.Transaction = &t
	case events.TypeSyncCompleted:
		var p events.SyncCompletedPayload
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return HookEvent{}, false
		}
		he.Sync = &SyncSummary{
			Accounts:            p.Accounts,
			NewTransactions:     p.NewTransactions,
			UpdatedTransactions: p.UpdatedTransactions,
			AutoLabeled:         p.AutoLabeled,
			Duration:            p.Duration,
		}
	default:
		return HookEvent{}, false
	}
	return he, true
}

func transactionFromPayload(p events.TransactionPayload) Transaction {
	return Transaction{
		ID:          p.ID,
		AccountID:   p.AccountID,
		Amount:      p.Amount,
		Pending:     p.Pending,
		Date:        p.Date,
		Description: p.Description,
		Payee:       p.Payee,
		Memo:        p.Memo,
		Labels:      p.Labels,
		Extensions:  p.Extensions,
	}
}

// eventFromRow adapts a stored event row into the in-memory event the dispatch
// path consumes (mirrors the webhook dispatcher's helper).
func eventFromRow(row db.Event) events.Event {
	return events.Event{
		Sequence:   row.ID,
		EventID:    row.EventID,
		Type:       row.EventType,
		EntityType: row.EntityType,
		EntityID:   row.EntityID,
		OccurredAt: time.Unix(row.OccurredAt, 0).UTC(),
		Data:       []byte(row.Data),
	}
}
