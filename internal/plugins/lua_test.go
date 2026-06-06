package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	lua "github.com/yuin/gopher-lua"

	"github.com/paulmeier/kasas/internal/events"
)

// fakeHost records the calls a plugin makes, so a test can assert the Lua adapter
// wired arguments through correctly without a real store.
type fakeHost struct {
	applied    map[string]map[string]string
	removed    map[string][]string
	extensions map[string]map[string]string
	searchRes  []Transaction
	getRes     *Transaction
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		applied:    map[string]map[string]string{},
		removed:    map[string][]string{},
		extensions: map[string]map[string]string{},
	}
}

func (f *fakeHost) GetTransaction(_ context.Context, _ string) (*Transaction, error) {
	return f.getRes, nil
}
func (f *fakeHost) Search(_ context.Context, _ string, _ int) ([]Transaction, error) {
	return f.searchRes, nil
}
func (f *fakeHost) ApplyLabels(_ context.Context, id string, l map[string]string) error {
	f.applied[id] = l
	return nil
}
func (f *fakeHost) RemoveLabels(_ context.Context, id string, keys []string) error {
	f.removed[id] = keys
	return nil
}
func (f *fakeHost) SetExtension(_ context.Context, id, key string, v json.RawMessage) error {
	if f.extensions[id] == nil {
		f.extensions[id] = map[string]string{}
	}
	f.extensions[id][key] = string(v)
	return nil
}
func (f *fakeHost) RemoveExtension(_ context.Context, _, _ string) error { return nil }
func (f *fakeHost) Log(_, _ string, _ map[string]any)                    {}

func loadFixture(t *testing.T, name string, host Host) Instance {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name, manifestFile))
	require.NoError(t, err)
	m, err := ParseManifest(data)
	require.NoError(t, err)
	inst, err := NewLuaRuntime().Load(context.Background(), m, filepath.Join("testdata", name), host)
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func TestLuaInvokeWiresHostCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadFixture(t, "budgeting", host)

	ev := HookEvent{
		Type:        events.TypeTransactionCreated,
		Transaction: &Transaction{ID: "tx-1", Description: "COFFEE SHOP", Amount: "-4.50"},
	}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))

	assert.Equal(t, map[string]string{"category": "food"}, host.applied["tx-1"],
		"plugin should have applied the food label via kasas.apply_labels")
	assert.Equal(t, "true", host.extensions["tx-1"]["budgeting.flagged"],
		"plugin should have set the extension via kasas.set_extension")
}

func TestLuaInvokeNoMatchNoCalls(t *testing.T) {
	host := newFakeHost()
	inst := loadFixture(t, "budgeting", host)
	ev := HookEvent{Transaction: &Transaction{ID: "tx-2", Description: "Books"}}
	require.NoError(t, inst.Invoke(context.Background(), HookTransactionCreate, ev))
	assert.Empty(t, host.applied, "a non-matching transaction triggers no writes")
}

func TestLuaHookNotImplemented(t *testing.T) {
	inst := loadFixture(t, "budgeting", newFakeHost())
	// budgeting does not declare OnSyncComplete.
	err := inst.Invoke(context.Background(), HookSyncComplete, HookEvent{Type: events.TypeSyncCompleted})
	assert.ErrorIs(t, err, ErrHookNotImpl)
}

func TestLuaLoadRejectsMissingHandler(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.toml"), []byte(
		`name="x"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`), 0o644))
	// main.lua defines a different function, so the declared hook is missing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.lua"), []byte(`function Nope() end`), 0o644))
	m, err := ParseManifest([]byte(`name="x"` + "\n" + `runtime="lua"` + "\n" + `hooks=["OnTransactionCreate"]`))
	require.NoError(t, err)
	_, err = NewLuaRuntime().Load(context.Background(), m, dir, newFakeHost())
	assert.Error(t, err, "a declared hook without a matching function must fail to load")
}

func TestLuaPanicRecovered(t *testing.T) {
	inst := loadFixture(t, "panics", newFakeHost())
	err := inst.Invoke(context.Background(), HookTransactionCreate, HookEvent{Transaction: &Transaction{ID: "tx-1"}})
	require.Error(t, err, "a Lua error becomes a Go error, not a crash")
}

func TestLuaTimeoutInterruptsLoop(t *testing.T) {
	inst := loadFixture(t, "slow", newFakeHost())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := inst.Invoke(ctx, HookTransactionCreate, HookEvent{Transaction: &Transaction{ID: "tx-1"}})
	elapsed := time.Since(start)

	require.Error(t, err, "a runaway loop must be interrupted by the timeout")
	assert.Less(t, elapsed, 2*time.Second, "interruption should be prompt, not run to completion")
}

func TestLuaSandboxRemovesDangerousGlobals(t *testing.T) {
	inst := loadFixture(t, "budgeting", newFakeHost())
	li := inst.(*luaInstance)
	for _, g := range []string{"os", "io", "debug", "require", "load", "loadstring", "dofile", "loadfile"} {
		assert.Equal(t, lua.LTNil, li.L.GetGlobal(g).Type(), "global %q must be nil in the sandbox", g)
	}
	// the kasas host table is present
	assert.Equal(t, lua.LTTable, li.L.GetGlobal("kasas").Type())
}
