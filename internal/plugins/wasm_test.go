package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
)

// The wasm fixtures are real Go programs (testdata/<name>/guest) compiled to
// wasip1 reactors on first use, exactly the way a plugin author builds one. The
// guest packages live inside the root module (testdata is invisible to ./...
// patterns but buildable when named explicitly), so no nested go.mod is needed.
var (
	wasmFixtureOnce sync.Once
	wasmFixtureErr  error
)

func buildWasmFixtures(t *testing.T) {
	t.Helper()
	wasmFixtureOnce.Do(func() {
		for _, name := range []string{"wasm-budgeting", "wasm-minimal", "wasm-source"} {
			out := filepath.Join("testdata", name, "main.wasm")
			cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./"+filepath.Join("testdata", name, "guest"))
			cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				wasmFixtureErr = fmt.Errorf("build %s: %v\n%s", name, err, b)
				return
			}
		}
	})
	if wasmFixtureErr != nil {
		t.Fatalf("compile wasm fixtures: %v", wasmFixtureErr)
	}
}

// loadWasmFixture loads a testdata plugin through the WASM runtime (the wasm
// analogue of loadFixture / loadJSFixture, reusing the shared fakeHost).
func loadWasmFixture(t *testing.T, name string, host Host) Instance {
	t.Helper()
	buildWasmFixtures(t)
	data, err := os.ReadFile(filepath.Join("testdata", name, manifestFile))
	require.NoError(t, err)
	m, err := ParseManifest(data)
	require.NoError(t, err)
	inst, err := NewWasmRuntime().Load(context.Background(), m, filepath.Join("testdata", name), host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func TestWasmInvokeWiresHostCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)

	ev := HookEvent{
		Type:        events.TypeTransactionCreated,
		Transaction: &Transaction{ID: "tx-1", Description: "COFFEE SHOP", Amount: "-4.50"},
	}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))

	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"],
		"plugin should have applied the food label via kasas.ApplyLabels")
	assert.Equal(t, "true", host.extensions["tx-1"]["budgeting.flagged"],
		"plugin should have set the extension via kasas.SetExtension")
}

func TestWasmInvokeNoMatchNoCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)
	ev := HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "Books"}}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))
	assert.Empty(t, host.applied, "a non-matching transaction triggers no writes")
}

func TestWasmSyncAndUninstallHooks(t *testing.T) {
	inst := loadWasmFixture(t, "wasm-budgeting", newFakeHost())
	require.NoError(t, inst.Invoke(context.Background(), HookSyncComplete,
		HookEvent{Type: events.TypeSyncCompleted, Sync: &SyncSummary{NewTransactions: 3}}))
	require.NoError(t, inst.Invoke(context.Background(), HookUninstall,
		HookEvent{Type: "plugin.uninstall", OccurredAt: time.Now()}))
}

// TestWasmConfigOverrideDelivered proves the effective config (not the manifest
// default) reaches the guest: with keyword overridden to "tea", a TEA
// transaction is tagged and a COFFEE one is not.
func TestWasmConfigOverrideDelivered(t *testing.T) {
	buildWasmFixtures(t)
	host := newFakeHost()
	m := Manifest{Name: "wasm-budgeting", Runtime: RuntimeWasm, Entrypoint: "main.wasm",
		Hooks:  []Hook{HookTransactionCreate},
		Config: map[string]any{"keyword": "tea"}}
	inst, err := NewWasmRuntime().Load(context.Background(), m, filepath.Join("testdata", "wasm-budgeting"), host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })

	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1", Description: "TEA TIME"}}))
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "COFFEE"}}))

	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"])
	assert.NotContains(t, host.applied, "tx-2", "the manifest default must be overridden")
}

func TestWasmLoadRejectsUnregisteredHook(t *testing.T) {
	buildWasmFixtures(t)
	// wasm-minimal only registers OnTransactionCreate; declaring OnSyncComplete
	// must fail the load (the describe handshake is authoritative).
	m := Manifest{Name: "wasm-minimal", Runtime: RuntimeWasm, Entrypoint: "main.wasm",
		Hooks: []Hook{HookTransactionCreate, HookSyncComplete}}
	_, err := NewWasmRuntime().Load(context.Background(), m, filepath.Join("testdata", "wasm-minimal"), newFakeHost())
	require.Error(t, err)
	assert.Contains(t, err.Error(), string(HookSyncComplete))
}

func TestWasmUnregisteredHookInvokeErrors(t *testing.T) {
	// The SDK exports every hook name unconditionally, so invoking one without a
	// registered handler is an error envelope, not a crash. (The manager never
	// dispatches undeclared hooks; this is the defensive path.)
	inst := loadWasmFixture(t, "wasm-minimal", newFakeHost())
	err := inst.Invoke(context.Background(), HookSyncComplete, HookEvent{Type: events.TypeSyncCompleted})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no handler registered")
}

func TestWasmLoadRejectsGarbageModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.wasm"), []byte("not wasm at all"), 0o644))
	m := Manifest{Name: "garbage", Runtime: RuntimeWasm, Entrypoint: "main.wasm", Hooks: []Hook{HookTransactionCreate}}
	_, err := NewWasmRuntime().Load(context.Background(), m, dir, newFakeHost())
	assert.Error(t, err, "a file that is not a wasm module must fail to load")
}

// TestWasmPanicRecoveredBySDK: a handler panic is recovered inside the guest and
// surfaces as an invocation error; the module instance stays alive and the next
// invocation works without a re-instantiation.
func TestWasmPanicRecoveredBySDK(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)

	err := inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1", Description: "panic!"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic: guest panic requested")

	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "coffee"}}))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-2"],
		"the instance must keep working after a recovered handler panic")
}

// TestWasmExitReinstantiates: os.Exit in the guest kills the module instance;
// the next invocation must transparently re-instantiate and succeed.
func TestWasmExitReinstantiates(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)

	err := inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1", Description: "exit!"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 3")

	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "coffee"}}))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-2"],
		"a fresh module instance must serve the next invocation")
}

// TestWasmTimeoutInterruptsSpin: a runaway loop is interrupted when the per-hook
// deadline fires (wazero closes the module), and the instance recovers on the
// next invocation.
func TestWasmTimeoutInterruptsSpin(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := inst.Invoke(ctx, HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-1", Description: "spin!"}})
	elapsed := time.Since(start)

	require.Error(t, err, "a runaway loop must be interrupted by the timeout")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 5*time.Second, "interruption should be prompt, not run to completion")

	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate,
		HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "coffee"}}))
	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-2"],
		"the instance must recover after a timeout killed the module")
}

func TestWasmPageRenderProducesValidDoc(t *testing.T) {
	inst := loadWasmFixture(t, "wasm-budgeting", newFakeHost())
	raw, err := inst.Render(context.Background(), HookPageRender, PageRequest{Plugin: "wasm-budgeting"})
	require.NoError(t, err)

	normalized, err := ValidatePageDoc(raw)
	require.NoError(t, err, "the SDK page builders must produce a document the server accepts")
	var doc PageDoc
	require.NoError(t, json.Unmarshal(normalized, &doc))
	assert.Equal(t, "WASM Budgeting", doc.Title)
	require.NotEmpty(t, doc.Blocks)
	assert.Equal(t, "stat", doc.Blocks[0].Type)
}

// TestWasmPageActionSetsConfig drives the form-submit path end to end: the
// action persists the keyword via kasas.SetConfig and the refreshed page
// reflects the new value (the SDK's config cache was updated by the response).
func TestWasmPageActionSetsConfig(t *testing.T) {
	host := newFakeHost()
	inst := loadWasmFixture(t, "wasm-budgeting", host)

	raw, err := inst.Render(context.Background(), HookPageAction, PageRequest{
		Plugin: "wasm-budgeting",
		Action: "set-keyword",
		Params: map[string]string{"keyword": "tea"},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"keyword": "tea"}, host.config, "the change reached the host")

	normalized, err := ValidatePageDoc(raw)
	require.NoError(t, err)
	var doc PageDoc
	require.NoError(t, json.Unmarshal(normalized, &doc))
	require.Len(t, doc.Blocks, 2)
	require.Len(t, doc.Blocks[1].Fields, 1)
	assert.Equal(t, "tea", string(doc.Blocks[1].Fields[0].Value),
		"the refreshed page must render the just-saved keyword")
}

func TestWasmSetConfigPersistsViaRealHost(t *testing.T) {
	buildWasmFixtures(t)
	pluginsDir := t.TempDir()
	defaults := map[string]any{"keyword": "coffee"}
	// No capabilities granted: the page's search degrades gracefully (the fixture
	// renders "n/a"), while SetConfig is always allowed, like the JS analogue.
	host := newHost(nil, nil, capSet{}, "wasm-budgeting", 0, testLogger(),
		newConfigStore(pluginsDir, "wasm-budgeting", defaults), nil)

	data, err := os.ReadFile(filepath.Join("testdata", "wasm-budgeting", manifestFile))
	require.NoError(t, err)
	m, err := ParseManifest(data)
	require.NoError(t, err)
	inst, err := NewWasmRuntime().Load(context.Background(), m, filepath.Join("testdata", "wasm-budgeting"), host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })

	_, err = inst.Render(context.Background(), HookPageAction, PageRequest{
		Plugin: "wasm-budgeting",
		Action: "set-keyword",
		Params: map[string]string{"keyword": "tea"},
	})
	require.NoError(t, err)

	eff, err := effectiveConfig(pluginsDir, "wasm-budgeting", defaults)
	require.NoError(t, err)
	assert.Equal(t, "tea", eff["keyword"], "the override file is the durable source of truth")
}

// TestWasmProduceReturnsBatch verifies the source:provide producer path through
// the WASM runtime + guest SDK (ADR 0005): OnFetch returns a Batch the host reads
// from the result envelope's batch field.
func TestWasmProduceReturnsBatch(t *testing.T) {
	inst := loadWasmFixture(t, "wasm-source", newFakeHost())
	raw, err := inst.Produce(context.Background(), HookFetch, json.RawMessage(`{"since":0,"cursor":""}`))
	require.NoError(t, err)

	var batch struct {
		Accounts []struct {
			ExternalID   string `json:"external_id"`
			Transactions []struct {
				ExternalID string `json:"external_id"`
				Amount     string `json:"amount"`
				Date       int64  `json:"date"`
			} `json:"transactions"`
		} `json:"accounts"`
	}
	require.NoError(t, json.Unmarshal(raw, &batch))
	require.Len(t, batch.Accounts, 1)
	require.Len(t, batch.Accounts[0].Transactions, 1)
	assert.Equal(t, "acct-1", batch.Accounts[0].ExternalID)
	assert.Equal(t, "tx-1", batch.Accounts[0].Transactions[0].ExternalID)
	assert.Equal(t, "-12.50", batch.Accounts[0].Transactions[0].Amount)
	assert.Equal(t, int64(1700000000), batch.Accounts[0].Transactions[0].Date, "Go marshals an int64 date plainly (no exponent)")
}
