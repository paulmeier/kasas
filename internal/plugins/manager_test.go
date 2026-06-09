package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/testutil"
)

// --- stub runtime for routing tests (registered under "lua" so manifests validate) ---

type stubInstance struct {
	mu    sync.Mutex
	calls []Hook
}

func (s *stubInstance) Invoke(_ context.Context, hook Hook, _ HookEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, hook)
	return nil
}
func (s *stubInstance) Render(_ context.Context, _ Hook, _ PageRequest) (json.RawMessage, error) {
	return nil, ErrHookNotImpl
}
func (s *stubInstance) Close() error { return nil }
func (s *stubInstance) seen() []Hook {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Hook(nil), s.calls...)
}

type stubRuntime struct{ inst *stubInstance }

func (r stubRuntime) Name() string { return RuntimeLua }
func (r stubRuntime) Load(_ context.Context, _ Manifest, _ string, _ Host) (Instance, error) {
	return r.inst, nil
}

// writePlugin creates a minimal plugin directory under root.
func writePlugin(t *testing.T, root, name, manifest, mainLua string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(manifest), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.lua"), []byte(mainLua), 0o644))
}

func findByName(statuses []Status, name string) (Status, bool) {
	for _, s := range statuses {
		if s.Name == name {
			return s, true
		}
	}
	return Status{}, false
}

func TestManagerReconcileRegistersDisabled(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "budgeting",
		`name="budgeting"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+`capabilities=["labels:write"]`,
		`function OnTransactionCreate(txn) end`)

	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()}, Logger: testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "budgeting")
	require.True(t, ok, "discovered plugin is registered and listed")
	assert.False(t, s.Enabled, "newly discovered plugins are disabled")
	assert.Equal(t, "disabled", s.State)
	assert.False(t, s.Loaded)
	assert.ElementsMatch(t, []Capability{CapLabelsWrite}, s.Granted, "grant is seeded from the manifest")
}

func TestManagerRoutesOnlyDeclaredHooks(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "router",
		`name="router"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`,
		`function OnTransactionCreate(txn) end`)

	stub := &stubInstance{}
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: stubRuntime{inst: stub}}, Logger: testLogger(),
	})
	t.Cleanup(mgr.shutdown)

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "router")
	require.True(t, ok)
	_, err = mgr.SetEnabled(context.Background(), s.ID, true)
	require.NoError(t, err)

	// A declared-hook event is delivered.
	mgr.dispatch(events.Event{Type: events.TypeTransactionCreated, Data: []byte(`{"id":"tx-1"}`)})
	// An event with no matching hook is not.
	mgr.dispatch(events.Event{Type: events.TypeSyncCompleted, Data: []byte(`{}`)})

	require.Eventually(t, func() bool {
		return len(stub.seen()) == 1
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, []Hook{HookTransactionCreate}, stub.seen())
}

// TestManagerEndToEnd exercises the whole spine: an emitted transaction.created
// event travels bus -> manager -> per-plugin worker -> Lua -> host -> emitter ->
// DB, leaving the configured label on the transaction.
func TestManagerEndToEnd(t *testing.T) {
	store := testutil.NewStore(t)
	fx := testutil.Seed(t, store)
	bus := events.NewBus()
	emitter := events.NewEmitter(bus)

	mgr := NewManager(Options{
		Store: store, Emitter: emitter, Bus: bus, Dir: "testdata",
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})

	// Register, then enable the budgeting plugin.
	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	bud, ok := findByName(statuses, "budgeting")
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let Run subscribe to the bus

	_, err = mgr.SetEnabled(ctx, bud.ID, true)
	require.NoError(t, err)

	// Insert a matching transaction and emit transaction.created for it.
	require.NoError(t, emitter.Record(ctx, store, func(q db.Querier, rec *events.Recorder) error {
		if _, err := q.InsertTransaction(ctx, db.InsertTransactionParams{
			ID: "tx-coffee", AccountID: fx.CheckingID, Amount: "-5.00",
			Date: testutil.Date2024Jun, Description: "Coffee Bar", Payee: "Cafe", SyncedAt: 1,
		}); err != nil {
			return err
		}
		row, err := q.GetTransaction(ctx, "tx-coffee")
		if err != nil {
			return err
		}
		return rec.Emit(ctx, q, events.TypeTransactionCreated, events.EntityTransaction, "tx-coffee", events.TransactionSnapshot(row))
	}))

	require.Eventually(t, func() bool {
		row, err := store.GetTransaction(context.Background(), "tx-coffee")
		if err != nil {
			return false
		}
		return labels.Decode(row.Labels)["category"] == "food"
	}, 3*time.Second, 20*time.Millisecond, "the plugin should have labeled the coffee transaction")
}

// TestManagerEnableContextCancelStillRuns is a regression test: a plugin enabled via
// SetEnabled on a short-lived context (an HTTP request, whose context is cancelled
// when the handler returns) must keep running afterwards. Before the fix, the worker
// captured that request context, so every subsequent hook invoked under an
// already-cancelled context, was interrupted before doing anything, and silently
// no-oped.
func TestManagerEnableContextCancelStillRuns(t *testing.T) {
	store := testutil.NewStore(t)
	fx := testutil.Seed(t, store)
	bus := events.NewBus()
	emitter := events.NewEmitter(bus)

	mgr := NewManager(Options{
		Store: store, Emitter: emitter, Bus: bus, Dir: "testdata",
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	go mgr.Run(runCtx)
	time.Sleep(100 * time.Millisecond) // let Run record baseCtx and subscribe

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	bud, ok := findByName(statuses, "budgeting")
	require.True(t, ok)

	// Enable on a request-scoped context, then cancel it as if the HTTP request ended.
	reqCtx, cancelReq := context.WithCancel(context.Background())
	_, err = mgr.SetEnabled(reqCtx, bud.ID, true)
	require.NoError(t, err)
	cancelReq()

	require.NoError(t, emitter.Record(context.Background(), store, func(q db.Querier, rec *events.Recorder) error {
		if _, err := q.InsertTransaction(context.Background(), db.InsertTransactionParams{
			ID: "tx-coffee2", AccountID: fx.CheckingID, Amount: "-5.00",
			Date: testutil.Date2024Jun, Description: "Coffee Bar", Payee: "Cafe", SyncedAt: 1,
		}); err != nil {
			return err
		}
		row, err := q.GetTransaction(context.Background(), "tx-coffee2")
		if err != nil {
			return err
		}
		return rec.Emit(context.Background(), q, events.TypeTransactionCreated, events.EntityTransaction, "tx-coffee2", events.TransactionSnapshot(row))
	}))

	require.Eventually(t, func() bool {
		row, err := store.GetTransaction(context.Background(), "tx-coffee2")
		if err != nil {
			return false
		}
		return labels.Decode(row.Labels)["category"] == "food"
	}, 3*time.Second, 20*time.Millisecond, "hook must still run after the enable request context is cancelled")
}

// captureRuntime records the manifest handed to Load, so a test can assert the
// effective config the instance sees.
type captureRuntime struct {
	inst *stubInstance
	got  *Manifest
}

func (r *captureRuntime) Name() string { return RuntimeLua }
func (r *captureRuntime) Load(_ context.Context, m Manifest, _ string, _ Host) (Instance, error) {
	*r.got = m
	return r.inst, nil
}

func TestManagerLoadMergesUserConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "budgeting",
		`name="budgeting"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+
			"[config]\nkeyword=\"coffee\"\nlimit=10",
		`function OnTransactionCreate(txn) end`)
	require.NoError(t, os.WriteFile(userConfigPath(dir, "budgeting"), []byte("keyword = \"tea\"\n"), 0o644))

	rt := &captureRuntime{inst: &stubInstance{}, got: &Manifest{}}
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: rt}, Logger: testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	bud, ok := findByName(statuses, "budgeting")
	require.True(t, ok)

	_, err = mgr.SetEnabled(context.Background(), bud.ID, true)
	require.NoError(t, err)
	assert.Equal(t, "tea", rt.got.Config["keyword"], "the override file wins over the manifest default")
	assert.Equal(t, int64(10), rt.got.Config["limit"], "untouched keys keep their manifest defaults")
}

func TestManagerLoadRejectsBrokenUserConfig(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "budgeting",
		`name="budgeting"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+
			"[config]\nkeyword=\"coffee\"",
		`function OnTransactionCreate(txn) end`)
	require.NoError(t, os.WriteFile(userConfigPath(dir, "budgeting"), []byte("nope = \"x\"\n"), 0o644))

	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: &captureRuntime{inst: &stubInstance{}, got: &Manifest{}}},
		Logger:   testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	bud, ok := findByName(statuses, "budgeting")
	require.True(t, ok)

	_, err = mgr.SetEnabled(context.Background(), bud.ID, true)
	require.Error(t, err, "a mistyped override key must fail the load so the operator notices")
	assert.Contains(t, err.Error(), "unknown config key")
}
