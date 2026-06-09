package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// luaRuntime is the gopher-lua implementation of Runtime. gopher-lua is pure Go
// (no cgo), so it preserves the single-static-binary, cross-compilable build.
type luaRuntime struct{}

// NewLuaRuntime constructs the Lua runtime.
func NewLuaRuntime() Runtime { return luaRuntime{} }

func (luaRuntime) Name() string { return RuntimeLua }

// Load creates a sandboxed Lua VM, injects the kasas host table, executes the
// plugin's entrypoint, and resolves each declared hook to a Lua function. A
// declared hook without a matching function is a load error, keeping the
// manifest's hook list authoritative.
func (luaRuntime) Load(_ context.Context, m Manifest, dir string, host Host) (Instance, error) {
	// SkipOpenLibs: we open only base/table/string/math below — os, io, debug, and
	// the package/require loader are never present, removing the obvious escape
	// hatches. Lua is not a true sandbox (no hard memory cap), so the real isolation
	// guarantees come later with the WASM runtime; v1's model is operator-trusted,
	// opt-in-enabled plugins plus a per-hook timeout.
	L := lua.NewState(lua.Options{SkipOpenLibs: true})
	openSafeLibs(L)

	li := &luaInstance{L: L, host: host, name: m.Name, hooks: map[Hook]*lua.LFunction{}}
	li.inject(m)

	if err := L.DoFile(filepath.Join(dir, m.Entrypoint)); err != nil {
		L.Close()
		return nil, fmt.Errorf("execute %s: %w", m.Entrypoint, err)
	}

	for _, h := range m.Hooks {
		fn, ok := L.GetGlobal(string(h)).(*lua.LFunction)
		if !ok {
			L.Close()
			return nil, fmt.Errorf("hook %q declared but no global function %q is defined", h, h)
		}
		li.hooks[h] = fn
	}
	return li, nil
}

// openSafeLibs opens only the libraries a plugin needs, in dependency order.
func openSafeLibs(L *lua.LState) {
	for _, lib := range []struct {
		name string
		open lua.LGFunction
	}{
		{lua.BaseLibName, lua.OpenBase},
		{lua.TabLibName, lua.OpenTable},
		{lua.StringLibName, lua.OpenString},
		{lua.MathLibName, lua.OpenMath},
	} {
		L.Push(L.NewFunction(lib.open))
		L.Push(lua.LString(lib.name))
		L.Call(1, 0)
	}
}

// luaInstance is one loaded Lua plugin. It is invoked on a single goroutine (the
// plugin's worker), so the non-reentrant *lua.LState is reused across invocations.
type luaInstance struct {
	L     *lua.LState
	host  Host
	name  string
	hooks map[Hook]*lua.LFunction
	// ctx is the current invocation's context, set in Invoke and read by the host
	// closures. Safe because invocations are serialized by the per-plugin worker.
	ctx context.Context
}

var _ Instance = (*luaInstance)(nil)

// inject removes residual escape-hatch globals and installs the kasas host table.
func (li *luaInstance) inject(m Manifest) {
	L := li.L
	// The base library still exposes dynamic-code/file loaders and the module
	// loader; remove them so a plugin can't read the filesystem, eval arbitrary
	// strings, or pull in other modules.
	for _, g := range []string{"dofile", "loadfile", "load", "loadstring", "require", "module", "newproxy"} {
		L.SetGlobal(g, lua.LNil)
	}
	// Route print() to the structured log rather than the server's stdout.
	L.SetGlobal("print", L.NewFunction(li.print))

	mod := L.NewTable()
	L.SetField(mod, "get_transaction", L.NewFunction(li.getTransaction))
	L.SetField(mod, "search", L.NewFunction(li.search))
	L.SetField(mod, "apply_labels", L.NewFunction(li.applyLabels))
	L.SetField(mod, "remove_labels", L.NewFunction(li.removeLabels))
	L.SetField(mod, "set_extension", L.NewFunction(li.setExtension))
	L.SetField(mod, "remove_extension", L.NewFunction(li.removeExtension))
	L.SetField(mod, "log", L.NewFunction(li.log))
	L.SetField(mod, "set_config", L.NewFunction(li.setConfig))
	L.SetField(mod, "config", goToLua(L, m.Config))
	L.SetGlobal("kasas", mod)
}

// Invoke runs the plugin's handler for hook. ctx (with the per-hook deadline) is
// attached to the VM so a runaway pure-Lua loop is interrupted; Protect=true turns
// a Lua error into a Go error, and the deferred recover guards against any panic
// escaping a host closure.
func (li *luaInstance) Invoke(ctx context.Context, hook Hook, ev HookEvent) (err error) {
	fn, ok := li.hooks[hook]
	if !ok {
		return ErrHookNotImpl
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panic: %v", r)
		}
	}()
	li.ctx = ctx
	defer func() { li.ctx = nil }()
	li.L.SetContext(ctx)
	defer li.L.RemoveContext()

	return li.L.CallByParam(lua.P{Fn: fn, NRet: 0, Protect: true}, hookArg(li.L, hook, ev))
}

// Render runs a value-returning page hook: it calls the Lua function with the
// request table, expects a page-document table back, and encodes it to JSON for
// ValidatePageDoc. Same safety envelope as Invoke (context on the VM, Protect,
// recover).
func (li *luaInstance) Render(ctx context.Context, hook Hook, req PageRequest) (out json.RawMessage, err error) {
	fn, ok := li.hooks[hook]
	if !ok {
		return nil, ErrHookNotImpl
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panic: %v", r)
		}
	}()
	li.ctx = ctx
	defer func() { li.ctx = nil }()
	li.L.SetContext(ctx)
	defer li.L.RemoveContext()

	if err := li.L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}, pageRequestToLua(li.L, req)); err != nil {
		return nil, err
	}
	ret := li.L.Get(-1)
	li.L.Pop(1)
	if ret == lua.LNil {
		return nil, fmt.Errorf("%s returned nil (expected a page table)", hook)
	}
	raw, err := luaValueToJSON(ret)
	if err != nil {
		return nil, fmt.Errorf("%s result: %w", hook, err)
	}
	return raw, nil
}

func pageRequestToLua(L *lua.LState, req PageRequest) *lua.LTable {
	t := L.NewTable()
	L.SetField(t, "plugin", lua.LString(req.Plugin))
	L.SetField(t, "action", lua.LString(req.Action))
	L.SetField(t, "params", goToLua(L, req.Params))
	return t
}

// Close releases the VM.
func (li *luaInstance) Close() error {
	li.L.Close()
	return nil
}

// invCtx returns the current invocation context, defaulting to Background so a
// host closure is never called with a nil context.
func (li *luaInstance) invCtx() context.Context {
	if li.ctx != nil {
		return li.ctx
	}
	return context.Background()
}

// --- kasas.* host closures ---

func (li *luaInstance) getTransaction(L *lua.LState) int {
	t, err := li.host.GetTransaction(li.invCtx(), L.CheckString(1))
	if err != nil {
		if errors.Is(err, ErrTxnNotFound) {
			L.Push(lua.LNil)
			return 1
		}
		L.RaiseError("get_transaction: %v", err)
		return 0
	}
	L.Push(txnToLua(L, *t))
	return 1
}

func (li *luaInstance) search(L *lua.LState) int {
	txns, err := li.host.Search(li.invCtx(), L.CheckString(1), L.OptInt(2, 100))
	if err != nil {
		L.RaiseError("search: %v", err)
		return 0
	}
	res := L.NewTable()
	for _, t := range txns {
		res.Append(txnToLua(L, t))
	}
	L.Push(res)
	return 1
}

func (li *luaInstance) applyLabels(L *lua.LState) int {
	if err := li.host.ApplyLabels(li.invCtx(), L.CheckString(1), luaTableToStringMap(L.CheckTable(2))); err != nil {
		L.RaiseError("apply_labels: %v", err)
	}
	return 0
}

func (li *luaInstance) removeLabels(L *lua.LState) int {
	if err := li.host.RemoveLabels(li.invCtx(), L.CheckString(1), luaTableToStringSlice(L.CheckTable(2))); err != nil {
		L.RaiseError("remove_labels: %v", err)
	}
	return 0
}

func (li *luaInstance) setExtension(L *lua.LState) int {
	raw, err := luaValueToJSON(L.CheckAny(3))
	if err != nil {
		L.RaiseError("set_extension: %v", err)
		return 0
	}
	if err := li.host.SetExtension(li.invCtx(), L.CheckString(1), L.CheckString(2), raw); err != nil {
		L.RaiseError("set_extension: %v", err)
	}
	return 0
}

func (li *luaInstance) removeExtension(L *lua.LState) int {
	if err := li.host.RemoveExtension(li.invCtx(), L.CheckString(1), L.CheckString(2)); err != nil {
		L.RaiseError("remove_extension: %v", err)
	}
	return 0
}

// setConfig persists config overrides: kasas.set_config{ key = value, ... }.
// The host validates each key against the manifest's [config] defaults and
// overwrites the plugin's user config file; on success the live kasas.config
// table is replaced with the new effective config, which is also returned.
func (li *luaInstance) setConfig(L *lua.LState) int {
	raw, err := luaTableToJSON(L.CheckTable(1))
	if err != nil {
		L.RaiseError("set_config: %v", err)
		return 0
	}
	var changes map[string]any
	if err := json.Unmarshal(raw, &changes); err != nil {
		L.RaiseError("set_config: expected a table of key/value pairs")
		return 0
	}
	merged, err := li.host.SetConfig(li.invCtx(), changes)
	if err != nil {
		L.RaiseError("set_config: %v", err)
		return 0
	}
	cfg := goToLua(L, merged)
	if mod, ok := L.GetGlobal("kasas").(*lua.LTable); ok {
		L.SetField(mod, "config", cfg)
	}
	L.Push(cfg)
	return 1
}

func (li *luaInstance) log(L *lua.LState) int {
	var kv map[string]any
	if L.GetTop() >= 3 {
		if tbl, ok := L.Get(3).(*lua.LTable); ok {
			kv = luaTableToAnyMap(tbl)
		}
	}
	li.host.Log(L.CheckString(1), L.CheckString(2), kv)
	return 0
}

func (li *luaInstance) print(L *lua.LState) int {
	n := L.GetTop()
	parts := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		parts = append(parts, lua.LVAsString(L.Get(i)))
	}
	li.host.Log("info", strings.Join(parts, "\t"), nil)
	return 0
}

// --- hook argument + conversions ---

func hookArg(L *lua.LState, hook Hook, ev HookEvent) lua.LValue {
	if hook == HookSyncComplete {
		t := L.NewTable()
		if ev.Sync != nil {
			L.SetField(t, "accounts", lua.LNumber(ev.Sync.Accounts))
			L.SetField(t, "new_transactions", lua.LNumber(ev.Sync.NewTransactions))
			L.SetField(t, "updated_transactions", lua.LNumber(ev.Sync.UpdatedTransactions))
			L.SetField(t, "auto_labeled", lua.LNumber(ev.Sync.AutoLabeled))
			L.SetField(t, "duration", lua.LString(ev.Sync.Duration))
		}
		return t
	}
	if ev.Transaction != nil {
		return txnToLua(L, *ev.Transaction)
	}
	return lua.LNil
}

func txnToLua(L *lua.LState, t Transaction) *lua.LTable {
	tbl := L.NewTable()
	L.SetField(tbl, "id", lua.LString(t.ID))
	L.SetField(tbl, "account_id", lua.LString(t.AccountID))
	L.SetField(tbl, "amount", lua.LString(t.Amount))
	L.SetField(tbl, "pending", lua.LBool(t.Pending))
	L.SetField(tbl, "date", lua.LNumber(float64(t.Date.Unix())))
	L.SetField(tbl, "description", lua.LString(t.Description))
	L.SetField(tbl, "payee", lua.LString(t.Payee))
	L.SetField(tbl, "memo", lua.LString(t.Memo))
	L.SetField(tbl, "labels", goToLua(L, t.Labels))
	L.SetField(tbl, "extensions", goToLua(L, t.Extensions))
	return tbl
}

// goToLua converts a decoded Go value (from JSON or a config map) into a Lua value.
func goToLua(L *lua.LState, v any) lua.LValue {
	switch val := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(val)
	case float64:
		return lua.LNumber(val)
	case int:
		return lua.LNumber(val)
	case int64:
		return lua.LNumber(val)
	case json.Number:
		f, _ := val.Float64()
		return lua.LNumber(f)
	case string:
		return lua.LString(val)
	case map[string]any:
		t := L.NewTable()
		for k, vv := range val {
			L.SetField(t, k, goToLua(L, vv))
		}
		return t
	case map[string]string:
		t := L.NewTable()
		for k, vv := range val {
			L.SetField(t, k, lua.LString(vv))
		}
		return t
	case []any:
		t := L.NewTable()
		for _, vv := range val {
			t.Append(goToLua(L, vv))
		}
		return t
	default:
		return lua.LString(fmt.Sprintf("%v", val))
	}
}

func luaTableToStringMap(t *lua.LTable) map[string]string {
	out := map[string]string{}
	t.ForEach(func(k, v lua.LValue) {
		if ks := lua.LVAsString(k); ks != "" {
			out[ks] = lua.LVAsString(v)
		}
	})
	return out
}

func luaTableToStringSlice(t *lua.LTable) []string {
	var out []string
	t.ForEach(func(_, v lua.LValue) {
		if s := lua.LVAsString(v); s != "" {
			out = append(out, s)
		}
	})
	return out
}

func luaTableToAnyMap(t *lua.LTable) map[string]any {
	out := map[string]any{}
	t.ForEach(func(k, v lua.LValue) {
		if ks := lua.LVAsString(k); ks != "" {
			out[ks] = luaToAny(v)
		}
	})
	return out
}

func luaToAny(v lua.LValue) any {
	switch val := v.(type) {
	case lua.LBool:
		return bool(val)
	case lua.LNumber:
		return float64(val)
	case lua.LString:
		return string(val)
	default:
		return v.String()
	}
}

// luaValueToJSON converts a Lua value (an extension value) into a raw JSON message.
func luaValueToJSON(v lua.LValue) (json.RawMessage, error) {
	switch val := v.(type) {
	case *lua.LNilType:
		return json.RawMessage("null"), nil
	case lua.LBool:
		if bool(val) {
			return json.RawMessage("true"), nil
		}
		return json.RawMessage("false"), nil
	case lua.LNumber:
		return json.RawMessage(strconv.FormatFloat(float64(val), 'g', -1, 64)), nil
	case lua.LString:
		b, err := json.Marshal(string(val))
		return b, err
	case *lua.LTable:
		return luaTableToJSON(val)
	default:
		return nil, fmt.Errorf("unsupported value type %s", v.Type())
	}
}

func luaTableToJSON(t *lua.LTable) (json.RawMessage, error) {
	n := t.Len()
	keyCount := 0
	t.ForEach(func(_, _ lua.LValue) { keyCount++ })

	// A table with sequential integer keys 1..n (and no others) is a JSON array.
	if n > 0 && keyCount == n {
		arr := make([]json.RawMessage, 0, n)
		for i := 1; i <= n; i++ {
			elem, err := luaValueToJSON(t.RawGetInt(i))
			if err != nil {
				return nil, err
			}
			arr = append(arr, elem)
		}
		return json.Marshal(arr)
	}

	obj := make(map[string]json.RawMessage, keyCount)
	var ferr error
	t.ForEach(func(k, v lua.LValue) {
		if ferr != nil {
			return
		}
		ks, ok := k.(lua.LString)
		if !ok {
			ferr = fmt.Errorf("table keys must be strings to encode as JSON")
			return
		}
		elem, err := luaValueToJSON(v)
		if err != nil {
			ferr = err
			return
		}
		obj[string(ks)] = elem
	})
	if ferr != nil {
		return nil, ferr
	}
	return json.Marshal(obj)
}
