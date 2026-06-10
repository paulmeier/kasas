package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
)

// loadJSFixture loads a testdata plugin through the JS runtime (the JS analogue of
// loadFixture in lua_test.go, reusing the shared fakeHost).
func loadJSFixture(t *testing.T, name string, host Host) Instance {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name, manifestFile))
	require.NoError(t, err)
	m, err := ParseManifest(data)
	require.NoError(t, err)
	inst, err := NewJSRuntime().Load(context.Background(), m, filepath.Join("testdata", name), host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func TestJSInvokeWiresHostCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadJSFixture(t, "js-budgeting", host)

	ev := HookEvent{
		Type:        events.TypeTransactionCreated,
		Transaction: &Transaction{ID: "tx-1", Description: "COFFEE SHOP", Amount: "-4.50"},
	}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))

	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"],
		"plugin should have applied the food label via kasas.applyLabels")
	assert.Equal(t, "true", host.extensions["tx-1"]["budgeting.flagged"],
		"plugin should have set the extension via kasas.setExtension")
}

func TestJSInvokeNoMatchNoCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadJSFixture(t, "js-budgeting", host)
	ev := HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "Books"}}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))
	assert.Empty(t, host.applied, "a non-matching transaction triggers no writes")
}

// TestJSTypeScriptStripped loads a .ts fixture full of TypeScript-only syntax
// (interface, type annotations, an `as` cast). It only loads and runs if esbuild
// stripped the types, so a wired host call proves the transpile step.
func TestJSTypeScriptStripped(t *testing.T) {
	host := newFakeHost()
	inst := loadJSFixture(t, "ts-budgeting", host)
	ev := HookEvent{Transaction: &Transaction{ID: "tx-1", Description: "Coffee run", Amount: "-3.00"}}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"],
		"the TypeScript plugin should run once its types are stripped")
}

func TestJSHookNotImplemented(t *testing.T) {
	inst := loadJSFixture(t, "js-budgeting", newFakeHost())
	// js-budgeting does not declare OnSyncComplete.
	err := inst.Invoke(context.Background(), HookSyncComplete, HookEvent{Type: events.TypeSyncCompleted})
	assert.ErrorIs(t, err, ErrHookNotImpl)
}

func TestJSLoadRejectsMissingHandler(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.js"), []byte(`function Nope() {}`), 0o644))
	m, err := ParseManifest([]byte(`name="x"` + "\n" + `runtime="js"` + "\n" + `hooks=["OnTransactionCreate"]`))
	require.NoError(t, err)
	_, err = NewJSRuntime().Load(context.Background(), m, dir, newFakeHost())
	assert.Error(t, err, "a declared hook without a matching function must fail to load")
}

func TestJSThrowRecovered(t *testing.T) {
	inst := loadJSFixture(t, "js-throws", newFakeHost())
	err := inst.Invoke(context.Background(), HookTransactionCreate, HookEvent{Transaction: &Transaction{ID: "tx-1"}})
	require.Error(t, err, "a thrown JS Error becomes a Go error, not a crash")
}

func TestJSTimeoutInterruptsLoop(t *testing.T) {
	inst := loadJSFixture(t, "js-slow", newFakeHost())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := inst.Invoke(ctx, HookTransactionCreate, HookEvent{Transaction: &Transaction{ID: "tx-1"}})
	elapsed := time.Since(start)

	require.Error(t, err, "a runaway loop must be interrupted by the timeout")
	assert.Less(t, elapsed, 2*time.Second, "interruption should be prompt, not run to completion")
}

// TestJSReuseAfterInterrupt confirms the VM is usable after a timeout interrupt is
// cleared — the next invocation runs normally rather than re-tripping a stale interrupt.
func TestJSReuseAfterInterrupt(t *testing.T) {
	host := newFakeHost()
	slow := loadJSFixture(t, "js-slow", host)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	require.Error(t, slow.Invoke(ctx, HookTransactionCreate, HookEvent{Transaction: &Transaction{ID: "tx-1"}}))

	// A separate, well-behaved instance still works (the interrupt was per-VM and cleared).
	ok := loadJSFixture(t, "js-budgeting", host)
	require.NoError(t, ok.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-9", Description: "coffee"}}))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-9"])
}

func TestJSSandboxRemovesDynamicCodegen(t *testing.T) {
	inst := loadJSFixture(t, "js-budgeting", newFakeHost())
	ji := inst.(*jsInstance)
	assert.True(t, goja.IsUndefined(ji.vm.Get("eval")), "eval must be removed in the sandbox")
	assert.True(t, goja.IsUndefined(ji.vm.Get("Function")), "the Function constructor binding must be removed")
	// the kasas host object is present
	assert.False(t, goja.IsUndefined(ji.vm.Get("kasas")), "the kasas host object must be injected")
}

func TestJSSetConfigUpdatesLiveConfig(t *testing.T) {
	dir := t.TempDir()
	src := `
function OnTransactionCreate(txn) {
  kasas.setConfig({ keyword: "tea" });
  kasas.applyLabels(txn.id, { k: kasas.config.keyword });
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.js"), []byte(src), 0o644))
	m := Manifest{Name: "cfg", Runtime: RuntimeJS, Entrypoint: "main.js",
		Hooks: []Hook{HookTransactionCreate}, Config: map[string]any{"keyword": "coffee"}}

	host := newFakeHost()
	inst, err := NewJSRuntime().Load(context.Background(), m, dir, host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })

	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1"}}))
	assert.Equal(t, map[string]any{"keyword": "tea"}, host.config, "the change reached the host")
	assert.Equal(t, "tea", host.applied["tx-1"]["k"], "kasas.config reflects the new value immediately")
}

func TestJSSetConfigPersistsViaRealHost(t *testing.T) {
	pluginsDir := t.TempDir()
	codeDir := t.TempDir()
	src := `
function OnTransactionCreate(txn) {
  kasas.setConfig({ keyword: "tea", limit: 25 });
}`
	require.NoError(t, os.WriteFile(filepath.Join(codeDir, "main.js"), []byte(src), 0o644))
	defaults := map[string]any{"keyword": "coffee", "limit": int64(10)}
	m := Manifest{Name: "cfg", Runtime: RuntimeJS, Entrypoint: "main.js",
		Hooks: []Hook{HookTransactionCreate}, Config: defaults}
	host := newHost(nil, nil, capSet{}, "cfg", 0, testLogger(), newConfigStore(pluginsDir, "cfg", defaults))

	inst, err := NewJSRuntime().Load(context.Background(), m, codeDir, host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1"}}))

	eff, err := effectiveConfig(pluginsDir, "cfg", defaults)
	require.NoError(t, err)
	assert.Equal(t, "tea", eff["keyword"])
	assert.EqualValues(t, 25, eff["limit"])
}

// TestJSTransactionDateIsRealJSDate is a regression test: goja does not convert
// time.Time to a JS Date on its own, so without jsDate a plugin calling
// txn.date.getTime() threw "Object has no member 'getTime'" — breaking the
// documented contract that `date` is a JavaScript Date.
func TestJSTransactionDateIsRealJSDate(t *testing.T) {
	dir := t.TempDir()
	src := `
function OnTransactionCreate(txn) {
  kasas.applyLabels(txn.id, {
    iso: txn.date.toISOString(),
    ms: String(txn.date.getTime()),
  });
}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.js"), []byte(src), 0o644))
	m := Manifest{Name: "dater", Runtime: RuntimeJS, Entrypoint: "main.js",
		Hooks: []Hook{HookTransactionCreate}, Config: map[string]any{}}

	host := newFakeHost()
	inst, err := NewJSRuntime().Load(context.Background(), m, dir, host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })

	when := time.Date(2026, 6, 9, 12, 30, 0, 0, time.UTC)
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1", Date: when}}))
	assert.Equal(t, "2026-06-09T12:30:00.000Z", host.applied["tx-1"]["iso"])
	assert.Equal(t, strconv.FormatInt(when.UnixMilli(), 10), host.applied["tx-1"]["ms"])
}

// TestJSLoadsBundledEntrypoint proves the host can load a plugin whose entrypoint
// is a dependency bundle (ADR 0001). The canonical bundle wraps everything in an
// IIFE that assigns the entry's exported hooks to a namespace, then a footer copies
// them onto the global object — so the host resolves each declared hook as a global
// function exactly as it does for a hand-written entrypoint. This is the shape
// kasas-plugins' `bundle`/verifier produces, so loading it here keeps the host and
// the gate's contract in lockstep.
func TestJSLoadsBundledEntrypoint(t *testing.T) {
	dir := t.TempDir()
	bundle := `var __kasasExports = (() => {
  // a stand-in for a bundled dependency: pure computation, inlined
  function toCents(a) { return Math.round(parseFloat(a) * 100); }
  function OnTransactionCreate(txn) {
    if (toCents(txn.amount) < 0) { kasas.applyLabels(txn.id, { category: "food" }); }
  }
  function OnUninstall() {}
  return { OnTransactionCreate, OnUninstall };
})();
Object.assign(globalThis, __kasasExports);
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.js"), []byte(bundle), 0o644))
	m, err := ParseManifest([]byte("name=\"bundled\"\nruntime=\"js\"\nhooks=[\"OnTransactionCreate\",\"OnUninstall\"]\ncapabilities=[\"labels:write\"]\n"))
	require.NoError(t, err)

	host := newFakeHost()
	inst, err := NewJSRuntime().Load(context.Background(), m, dir, host)
	require.NoError(t, err, "a bundled entrypoint must load like any other")
	t.Cleanup(func() { _ = inst.Close() })

	ev := HookEvent{Transaction: &Transaction{ID: "tx-1", Amount: "-9.99"}}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"],
		"the bundled plugin's hook should run and reach the host")
}
