package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

// errOnUninstall is an Instance whose OnUninstall invocation fails, to prove a
// failing cleanup hook does not block removal.
type errOnUninstall struct{ ran *bool }

func (e errOnUninstall) Invoke(_ context.Context, hook Hook, _ HookEvent) error {
	if hook == HookUninstall {
		*e.ran = true
		return errors.New("cleanup blew up")
	}
	return nil
}
func (e errOnUninstall) Render(_ context.Context, _ Hook, _ PageRequest) (json.RawMessage, error) {
	return nil, ErrHookNotImpl
}
func (e errOnUninstall) Produce(_ context.Context, _ Hook, _ json.RawMessage) (json.RawMessage, error) {
	return nil, ErrHookNotImpl
}
func (e errOnUninstall) Close() error { return nil }

type errRuntime struct{ ran *bool }

func (r errRuntime) Name() string { return RuntimeLua }
func (r errRuntime) Load(_ context.Context, _ Manifest, _ string, _ Host) (Instance, error) {
	return errOnUninstall(r), nil
}

func installedManager(t *testing.T, rt Runtime, manifest, mainLua string) (*Manager, string, int64) {
	t.Helper()
	dir := t.TempDir()
	writePlugin(t, dir, "cleaner", manifest, mainLua)
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: dir,
		Runtimes: map[string]Runtime{RuntimeLua: rt}, Logger: testLogger(),
	})
	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	s, ok := findByName(statuses, "cleaner")
	require.True(t, ok)
	return mgr, dir, s.ID
}

const cleanerManifest = `name="cleaner"` + "\n" + `runtime="lua"` + "\n" +
	`hooks=["OnTransactionCreate","OnUninstall"]` + "\n" + `capabilities=["labels:write"]`

func TestUninstallRunsHookAndRemoves(t *testing.T) {
	stub := &stubInstance{}
	mgr, dir, id := installedManager(t, stubRuntime{inst: stub}, cleanerManifest,
		`function OnTransactionCreate(t) end function OnUninstall() end`)

	res, err := mgr.Uninstall(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "cleaner", res.Name)
	assert.True(t, res.HookRan, "OnUninstall was declared, so it should run")
	assert.Empty(t, res.HookError)
	assert.Contains(t, stub.seen(), HookUninstall, "the uninstall hook was invoked")

	// Files are gone.
	_, statErr := os.Stat(filepath.Join(dir, "cleaner"))
	assert.True(t, os.IsNotExist(statErr), "plugin directory was removed")

	// DB row is gone.
	_, gerr := mgr.Get(context.Background(), id)
	assert.ErrorIs(t, gerr, ErrPluginNotFound)
}

func TestUninstallWithoutHook(t *testing.T) {
	stub := &stubInstance{}
	mgr, dir, id := installedManager(t, stubRuntime{inst: stub},
		`name="cleaner"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`,
		`function OnTransactionCreate(t) end`)

	res, err := mgr.Uninstall(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, res.HookRan, "no OnUninstall declared, so no hook runs")
	assert.NotContains(t, stub.seen(), HookUninstall)
	_, statErr := os.Stat(filepath.Join(dir, "cleaner"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstallHookFailureStillRemoves(t *testing.T) {
	ran := false
	mgr, dir, id := installedManager(t, errRuntime{ran: &ran}, cleanerManifest,
		`function OnTransactionCreate(t) end function OnUninstall() end`)

	res, err := mgr.Uninstall(context.Background(), id)
	require.NoError(t, err, "a hook failure must not fail the uninstall")
	assert.True(t, ran, "the failing hook was invoked")
	assert.True(t, res.HookRan)
	assert.Contains(t, res.HookError, "cleanup blew up")

	// Removed despite the hook error.
	_, statErr := os.Stat(filepath.Join(dir, "cleaner"))
	assert.True(t, os.IsNotExist(statErr))
	_, gerr := mgr.Get(context.Background(), id)
	assert.ErrorIs(t, gerr, ErrPluginNotFound)
}

func TestUninstallRealLuaRuntime(t *testing.T) {
	// With the real Lua runtime, a declared OnUninstall must resolve and run.
	mgr, dir, id := installedManager(t, NewLuaRuntime(), cleanerManifest,
		`function OnTransactionCreate(t) end`+"\n"+
			`function OnUninstall() kasas.log("info", "cleaning up") end`)

	res, err := mgr.Uninstall(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, res.HookRan)
	assert.Empty(t, res.HookError)
	_, statErr := os.Stat(filepath.Join(dir, "cleaner"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestUninstallUnknownPlugin(t *testing.T) {
	mgr := NewManager(Options{
		Store: testutil.NewStore(t), Bus: events.NewBus(), Dir: t.TempDir(),
		Runtimes: map[string]Runtime{RuntimeLua: NewLuaRuntime()}, Logger: testLogger(),
	})
	_, err := mgr.Uninstall(context.Background(), 999)
	assert.ErrorIs(t, err, ErrPluginNotFound)
}

func TestUninstallRemovesUserConfigFile(t *testing.T) {
	stub := &stubInstance{}
	mgr, dir, id := installedManager(t, stubRuntime{inst: stub}, cleanerManifest,
		`function OnTransactionCreate(txn) end`+"\n"+`function OnUninstall() end`)
	require.NoError(t, os.WriteFile(userConfigPath(dir, "cleaner"), []byte("keyword = \"x\"\n"), 0o644))

	_, err := mgr.Uninstall(context.Background(), id)
	require.NoError(t, err)
	_, statErr := os.Stat(userConfigPath(dir, "cleaner"))
	assert.True(t, errors.Is(statErr, fs.ErrNotExist), "the override file belongs to the plugin and is removed with it")
}
