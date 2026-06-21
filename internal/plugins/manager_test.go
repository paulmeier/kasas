package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	mu      sync.Mutex
	calls   []Hook
	produce json.RawMessage // batch returned by Produce; nil => ErrHookNotImpl
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
func (s *stubInstance) Produce(_ context.Context, hook Hook, _ json.RawMessage) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, hook)
	if s.produce == nil {
		return nil, ErrHookNotImpl
	}
	return s.produce, nil
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

func TestManagerNetFetchGrantsAndStatus(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "importer",
		`name="importer"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+
			`capabilities=["net:fetch"]`+"\n"+`[net]`+"\n"+`allow=["paperless.lan","api.example.com"]`,
		`function OnTransactionCreate(txn) end`)

	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()}, Logger: testLogger(),
	})
	t.Cleanup(mgr.shutdown)

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "importer")
	require.True(t, ok)
	assert.ElementsMatch(t, []string{"paperless.lan", "api.example.com"}, s.NetAllow, "declared allowlist is surfaced")
	assert.Empty(t, s.NetGrants)

	// Granting a host that the manifest does NOT declare is refused.
	_, err = mgr.SetEnabled(context.Background(), s.ID, true, []string{"evil.example.net"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in the plugin's declared")

	// Granting a declared host persists it and surfaces it on the status.
	st, err := mgr.SetEnabled(context.Background(), s.ID, true, []string{"paperless.lan"})
	require.NoError(t, err)
	assert.Equal(t, []string{"paperless.lan"}, st.NetGrants)
	assert.True(t, st.Enabled)

	// The grant survives a re-read (it is durable, not just in-memory).
	again, err := mgr.Get(context.Background(), s.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{"paperless.lan"}, again.NetGrants)
}

// TestManagerNetFetchEndToEnd loads a real Lua plugin from disk, enables it with a
// loopback grant, and dispatches a real transaction.created event — the plugin's
// hook calls kasas.fetch, which goes through the REAL gate (real DNS + real dial,
// not injected) to a local server. It proves the whole stack: manifest [net]
// parse, enable-with-grant persistence, the SSRF rule's private-host grant path,
// and the egress log. No external network: the target is a loopback test server,
// reachable only because the operator granted "127.0.0.1".
func TestManagerNetFetchEndToEnd(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte("pong"))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	dir := t.TempDir()
	writePlugin(t, dir, "pinger",
		`name="pinger"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`+"\n"+
			`capabilities=["net:fetch"]`+"\n"+`[net]`+"\n"+`allow=["127.0.0.1"]`,
		`function OnTransactionCreate(txn) kasas.fetch{ url = "`+srv.URL+`/ping" } end`)

	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()}, Logger: testLogger(),
	})
	t.Cleanup(mgr.shutdown)

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "pinger")
	require.True(t, ok)

	// Loopback is a private address, so it is reachable only because we grant it.
	st, err := mgr.SetEnabled(context.Background(), s.ID, true, []string{"127.0.0.1"})
	require.NoError(t, err)
	require.Equal(t, []string{"127.0.0.1"}, st.NetGrants)

	mgr.dispatch(events.Event{Type: events.TypeTransactionCreated, Data: []byte(`{"id":"tx-1"}`)})

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&hits) == 1
	}, 3*time.Second, 10*time.Millisecond, "the plugin's kasas.fetch should reach the granted loopback host")

	// The egress log recorded the successful request for the operator.
	entries, err := mgr.EgressLog(context.Background(), s.ID, 0)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, 200, entries[0].Status)
	assert.Equal(t, u.Hostname(), entries[0].Host)
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
	_, err = mgr.SetEnabled(context.Background(), s.ID, true, nil)
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

// TestManagerRoutesTransactionDelete confirms a transaction.deleted event reaches a
// plugin that declared OnTransactionDelete (a manual/account-cascade delete fires the
// hook, closing the gap that creates and edits reached plugins but deletes did not).
func TestManagerRoutesTransactionDelete(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "reaper",
		`name="reaper"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionDelete"]`,
		`function OnTransactionDelete(txn) end`)

	stub := &stubInstance{}
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: stubRuntime{inst: stub}}, Logger: testLogger(),
	})
	t.Cleanup(mgr.shutdown)

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "reaper")
	require.True(t, ok)
	_, err = mgr.SetEnabled(context.Background(), s.ID, true, nil)
	require.NoError(t, err)

	// The delete event is delivered; the create event (not declared) is not.
	mgr.dispatch(events.Event{Type: events.TypeTransactionDeleted, Data: []byte(`{"id":"tx-1"}`)})
	mgr.dispatch(events.Event{Type: events.TypeTransactionCreated, Data: []byte(`{"id":"tx-2"}`)})

	require.Eventually(t, func() bool {
		return len(stub.seen()) == 1
	}, time.Second, 5*time.Millisecond)
	assert.Equal(t, []Hook{HookTransactionDelete}, stub.seen())
}

// TestDecodeHookEventTransactionDelete asserts the deleted transaction's snapshot is
// decoded onto the hook event, so OnTransactionDelete receives the row's last known
// state (id + labels) rather than a bare id.
func TestDecodeHookEventTransactionDelete(t *testing.T) {
	data, err := json.Marshal(events.TransactionPayload{
		ID:     "tx-gone",
		Amount: "-5.00",
		Labels: map[string]string{"category": "food"},
	})
	require.NoError(t, err)

	he, ok := decodeHookEvent(events.Event{Type: events.TypeTransactionDeleted, Data: data})
	require.True(t, ok)
	require.NotNil(t, he.Transaction)
	assert.Equal(t, "tx-gone", he.Transaction.ID)
	assert.Equal(t, "food", he.Transaction.Labels["category"])
	assert.Nil(t, he.Sync)
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

	_, err = mgr.SetEnabled(ctx, bud.ID, true, nil)
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

// TestManagerEndToEndTransactionDelete exercises the delete spine end to end through a
// real Lua VM: a transaction.deleted event reaches the OnTransactionDelete handler,
// which reacts from the deleted row's snapshot (its id) by labeling a SURVIVING audit
// transaction — proving the hook fires, the snapshot is delivered, and host writes
// against other rows still work even though the deleted row is gone.
func TestManagerEndToEndTransactionDelete(t *testing.T) {
	store := testutil.NewStore(t)
	fx := testutil.Seed(t, store)
	bus := events.NewBus()
	emitter := events.NewEmitter(bus)

	dir := t.TempDir()
	writePlugin(t, dir, "reaper",
		`name="reaper"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionDelete"]`+"\n"+
			`capabilities=["labels:write"]`,
		`function OnTransactionDelete(txn)`+"\n"+
			`  kasas.apply_labels("tx-audit", { last_deleted = txn.id })`+"\n"+
			`end`)

	mgr := NewManager(Options{
		Store: store, Emitter: emitter, Bus: bus, Dir: dir,
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	rp, ok := findByName(statuses, "reaper")
	require.True(t, ok)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let Run subscribe to the bus

	// A surviving "audit" transaction the delete handler writes to.
	require.NoError(t, emitter.Record(ctx, store, func(q db.Querier, _ *events.Recorder) error {
		_, err := q.InsertTransaction(ctx, db.InsertTransactionParams{
			ID: "tx-audit", AccountID: fx.CheckingID, Amount: "0.00",
			Date: testutil.Date2024Jun, Description: "Audit", SyncedAt: 1,
		})
		return err
	}))

	_, err = mgr.SetEnabled(ctx, rp.ID, true, nil)
	require.NoError(t, err)

	// Emit transaction.deleted carrying the gone row's last-known snapshot.
	require.NoError(t, emitter.Record(ctx, store, func(q db.Querier, rec *events.Recorder) error {
		return rec.Emit(ctx, q, events.TypeTransactionDeleted, events.EntityTransaction, "tx-victim",
			events.TransactionPayload{ID: "tx-victim", AccountID: fx.CheckingID, Amount: "-9.99"})
	}))

	require.Eventually(t, func() bool {
		row, err := store.GetTransaction(context.Background(), "tx-audit")
		if err != nil {
			return false
		}
		return labels.Decode(row.Labels)["last_deleted"] == "tx-victim"
	}, 3*time.Second, 20*time.Millisecond, "OnTransactionDelete should have recorded the deleted id on the audit transaction")
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
	_, err = mgr.SetEnabled(reqCtx, bud.ID, true, nil)
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

	_, err = mgr.SetEnabled(context.Background(), bud.ID, true, nil)
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

	_, err = mgr.SetEnabled(context.Background(), bud.ID, true, nil)
	require.Error(t, err, "a mistyped override key must fail the load so the operator notices")
	assert.Contains(t, err.Error(), "unknown config key")
}
