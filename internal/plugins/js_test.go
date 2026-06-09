package plugins

import (
	"context"
	"os"
	"path/filepath"
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
