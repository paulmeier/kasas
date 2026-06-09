package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// jsRuntime is the goja implementation of Runtime. goja is a pure-Go ECMAScript
// engine (no cgo), and esbuild's transform API is pure Go too, so the JS runtime
// preserves the single-static-binary, cross-compilable build. A plugin's entrypoint
// is transpiled once at load (TypeScript types stripped, modern syntax downleveled
// to a goja-safe target), then run in a fresh VM whose only path back into kasas is
// the capability-checked host facade.
type jsRuntime struct{}

// NewJSRuntime constructs the JavaScript/TypeScript runtime.
func NewJSRuntime() Runtime { return jsRuntime{} }

func (jsRuntime) Name() string { return RuntimeJS }

// Load transpiles the entrypoint, creates a sandboxed goja VM, injects the kasas
// host object, executes the script, and resolves each declared hook to its global
// function. A declared hook without a matching function is a load error, keeping the
// manifest's hook list authoritative (identical contract to the Lua runtime).
func (jsRuntime) Load(_ context.Context, m Manifest, dir string, host Host) (Instance, error) {
	src, err := os.ReadFile(filepath.Join(dir, m.Entrypoint))
	if err != nil {
		return nil, fmt.Errorf("read entrypoint %s: %w", m.Entrypoint, err)
	}
	code, err := transpile(m.Entrypoint, src)
	if err != nil {
		return nil, err
	}
	// Compile as a non-strict global script so top-level `function OnX(){}`
	// declarations bind to the global object and resolve via vm.Get below.
	prog, err := goja.Compile(m.Entrypoint, code, false)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", m.Entrypoint, err)
	}

	vm := goja.New()
	ji := &jsInstance{vm: vm, host: host, name: m.Name, hooks: map[Hook]goja.Callable{}}
	ji.inject(m)

	if _, err := vm.RunProgram(prog); err != nil {
		return nil, fmt.Errorf("execute %s: %w", m.Entrypoint, err)
	}
	for _, h := range m.Hooks {
		fn, ok := goja.AssertFunction(vm.Get(string(h)))
		if !ok {
			return nil, fmt.Errorf("hook %q declared but no global function %q is defined", h, h)
		}
		ji.hooks[h] = fn
	}
	return ji, nil
}

// transpile runs the plugin source through esbuild: it strips TypeScript types and
// downlevels modern syntax to a target goja can execute. The loader is chosen by
// file extension, so the same pipeline serves .js and .ts (and the JSX variants).
// This is a single-string transform with no module resolution, so a plugin stays a
// single self-contained file — the VM never reaches the filesystem, matching the Lua
// sandbox stance.
func transpile(entrypoint string, src []byte) (string, error) {
	loader := esbuild.LoaderJS
	switch strings.ToLower(filepath.Ext(entrypoint)) {
	case ".ts":
		loader = esbuild.LoaderTS
	case ".jsx":
		loader = esbuild.LoaderJSX
	case ".tsx":
		loader = esbuild.LoaderTSX
	}
	res := esbuild.Transform(string(src), esbuild.TransformOptions{
		Loader:    loader,
		Format:    esbuild.FormatDefault,
		Target:    esbuild.ES2017,
		Sourcemap: esbuild.SourceMapNone,
	})
	if len(res.Errors) > 0 {
		e := res.Errors[0]
		loc := ""
		if e.Location != nil {
			loc = fmt.Sprintf(" (line %d)", e.Location.Line)
		}
		return "", fmt.Errorf("transpile %s%s: %s", entrypoint, loc, e.Text)
	}
	return string(res.Code), nil
}

// jsInstance is one loaded JS/TS plugin. It is invoked on a single goroutine (the
// plugin's worker), so the non-reentrant *goja.Runtime is reused across invocations.
type jsInstance struct {
	vm    *goja.Runtime
	host  Host
	name  string
	hooks map[Hook]goja.Callable
	// kasas is the injected host object, kept so setConfig can swap the live
	// kasas.config value after a successful save.
	kasas *goja.Object
	// ctx is the current invocation's context, set in Invoke and read by the host
	// closures. Safe because invocations are serialized by the per-plugin worker.
	ctx context.Context
}

var _ Instance = (*jsInstance)(nil)

// inject installs the kasas host object and a console shim, and removes the obvious
// dynamic-codegen globals. goja exposes no require/filesystem/network by default, so
// the VM starts more sandboxed than Lua; like the Lua v1 model this is still not a
// hard sandbox (a script can recover the Function constructor via a function's
// prototype), so the trust model remains operator-installed, opt-in plugins.
func (ji *jsInstance) inject(m Manifest) {
	vm := ji.vm
	_ = vm.Set("eval", goja.Undefined())
	_ = vm.Set("Function", goja.Undefined())

	console := vm.NewObject()
	_ = console.Set("log", ji.consoleLogger("info"))
	_ = console.Set("info", ji.consoleLogger("info"))
	_ = console.Set("warn", ji.consoleLogger("warn"))
	_ = console.Set("error", ji.consoleLogger("error"))
	_ = console.Set("debug", ji.consoleLogger("debug"))
	_ = vm.Set("console", console)

	kasas := vm.NewObject()
	_ = kasas.Set("getTransaction", ji.getTransaction)
	_ = kasas.Set("search", ji.search)
	_ = kasas.Set("applyLabels", ji.applyLabels)
	_ = kasas.Set("removeLabels", ji.removeLabels)
	_ = kasas.Set("setExtension", ji.setExtension)
	_ = kasas.Set("removeExtension", ji.removeExtension)
	_ = kasas.Set("log", ji.log)
	_ = kasas.Set("setConfig", ji.setConfig)
	_ = kasas.Set("config", vm.ToValue(m.Config))
	_ = vm.Set("kasas", kasas)
	ji.kasas = kasas
}

// Invoke runs the plugin's handler for hook. A watcher goroutine interrupts the VM
// if the per-hook deadline fires, so a runaway loop (e.g. while(true){}) is stopped —
// the goja analogue of the Lua runtime attaching ctx to the VM. The deferred recover
// guards against any panic escaping a host closure.
func (ji *jsInstance) Invoke(ctx context.Context, hook Hook, ev HookEvent) error {
	fn, ok := ji.hooks[hook]
	if !ok {
		return ErrHookNotImpl
	}
	_, err := ji.call(ctx, fn, hookArgJS(ji.vm, hook, ev))
	return err
}

// Render runs a value-returning page hook: it calls the JS function with the
// request object and JSON-encodes whatever it returned for ValidatePageDoc.
func (ji *jsInstance) Render(ctx context.Context, hook Hook, req PageRequest) (json.RawMessage, error) {
	fn, ok := ji.hooks[hook]
	if !ok {
		return nil, ErrHookNotImpl
	}
	params := req.Params
	if params == nil {
		params = map[string]string{}
	}
	ret, err := ji.call(ctx, fn, ji.vm.ToValue(map[string]any{
		"plugin": req.Plugin,
		"action": req.Action,
		"params": params,
	}))
	if err != nil {
		return nil, err
	}
	if ret == nil || goja.IsUndefined(ret) || goja.IsNull(ret) {
		return nil, fmt.Errorf("%s returned nothing (expected a page object)", hook)
	}
	raw, err := json.Marshal(ret.Export())
	if err != nil {
		return nil, fmt.Errorf("%s result: %w", hook, err)
	}
	return raw, nil
}

// call invokes one resolved hook function under the shared safety envelope: the
// invocation context is published for the host closures, a watcher goroutine
// interrupts the VM when the deadline fires, and a deferred recover keeps any
// panic from escaping.
func (ji *jsInstance) call(ctx context.Context, fn goja.Callable, arg goja.Value) (ret goja.Value, err error) {
	ji.ctx = ctx

	// vm.Interrupt is explicitly safe to call from another goroutine; it is the only
	// VM method the watcher touches.
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			ji.vm.Interrupt(ctx.Err())
		case <-stop:
		}
	}()

	defer func() {
		close(stop)            // tell the watcher to exit
		<-watcherDone          // wait until it can no longer call Interrupt
		ji.vm.ClearInterrupt() // safe now: clear any interrupt for the next invocation
		ji.ctx = nil
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin panic: %v", r)
		}
	}()

	return fn(goja.Undefined(), arg)
}

// Close releases the instance. goja has no explicit teardown; dropping the VM is
// enough, so this is a no-op kept for the Instance contract.
func (ji *jsInstance) Close() error { return nil }

// invCtx returns the current invocation context, defaulting to Background so a host
// closure is never called with a nil context.
func (ji *jsInstance) invCtx() context.Context {
	if ji.ctx != nil {
		return ji.ctx
	}
	return context.Background()
}

// throw builds a JS Error to panic with from a host closure; goja recovers the panic
// and surfaces it as a thrown exception, which becomes this Invoke's returned error.
func (ji *jsInstance) throw(format string, args ...any) *goja.Object {
	return ji.vm.NewGoError(fmt.Errorf(format, args...))
}

// --- kasas.* host closures ---

func (ji *jsInstance) getTransaction(call goja.FunctionCall) goja.Value {
	t, err := ji.host.GetTransaction(ji.invCtx(), call.Argument(0).String())
	if errors.Is(err, ErrTxnNotFound) {
		return goja.Null()
	}
	if err != nil {
		panic(ji.throw("getTransaction: %v", err))
	}
	return txnToJS(ji.vm, *t)
}

func (ji *jsInstance) search(call goja.FunctionCall) goja.Value {
	limit := 0
	if a := call.Argument(1); !goja.IsUndefined(a) && !goja.IsNull(a) {
		limit = int(a.ToInteger())
	}
	txns, err := ji.host.Search(ji.invCtx(), call.Argument(0).String(), limit)
	if err != nil {
		panic(ji.throw("search: %v", err))
	}
	out := make([]any, len(txns))
	for i, t := range txns {
		out[i] = txnToJS(ji.vm, t)
	}
	return ji.vm.ToValue(out)
}

func (ji *jsInstance) applyLabels(call goja.FunctionCall) goja.Value {
	if err := ji.host.ApplyLabels(ji.invCtx(), call.Argument(0).String(), exportStringMap(call.Argument(1))); err != nil {
		panic(ji.throw("applyLabels: %v", err))
	}
	return goja.Undefined()
}

func (ji *jsInstance) removeLabels(call goja.FunctionCall) goja.Value {
	if err := ji.host.RemoveLabels(ji.invCtx(), call.Argument(0).String(), exportStringSlice(call.Argument(1))); err != nil {
		panic(ji.throw("removeLabels: %v", err))
	}
	return goja.Undefined()
}

func (ji *jsInstance) setExtension(call goja.FunctionCall) goja.Value {
	raw, err := json.Marshal(call.Argument(2).Export())
	if err != nil {
		panic(ji.throw("setExtension: %v", err))
	}
	if err := ji.host.SetExtension(ji.invCtx(), call.Argument(0).String(), call.Argument(1).String(), raw); err != nil {
		panic(ji.throw("setExtension: %v", err))
	}
	return goja.Undefined()
}

func (ji *jsInstance) removeExtension(call goja.FunctionCall) goja.Value {
	if err := ji.host.RemoveExtension(ji.invCtx(), call.Argument(0).String(), call.Argument(1).String()); err != nil {
		panic(ji.throw("removeExtension: %v", err))
	}
	return goja.Undefined()
}

// setConfig persists config overrides: kasas.setConfig({ key: value, ... }).
// The host validates each key against the manifest's [config] defaults and
// overwrites the plugin's user config file; on success the live kasas.config
// object is replaced with the new effective config, which is also returned.
func (ji *jsInstance) setConfig(call goja.FunctionCall) goja.Value {
	changes, ok := call.Argument(0).Export().(map[string]any)
	if !ok {
		panic(ji.throw("setConfig: expected an object of key/value pairs"))
	}
	merged, err := ji.host.SetConfig(ji.invCtx(), changes)
	if err != nil {
		panic(ji.throw("setConfig: %v", err))
	}
	cfg := ji.vm.ToValue(merged)
	if ji.kasas != nil {
		_ = ji.kasas.Set("config", cfg)
	}
	return cfg
}

func (ji *jsInstance) log(call goja.FunctionCall) goja.Value {
	var kv map[string]any
	if a := call.Argument(2); !goja.IsUndefined(a) && !goja.IsNull(a) {
		if m, ok := a.Export().(map[string]any); ok {
			kv = m
		}
	}
	ji.host.Log(call.Argument(0).String(), call.Argument(1).String(), kv)
	return goja.Undefined()
}

func (ji *jsInstance) consoleLogger(level string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.String())
		}
		ji.host.Log(level, strings.Join(parts, " "), nil)
		return goja.Undefined()
	}
}

// --- hook argument + conversions ---

func hookArgJS(vm *goja.Runtime, hook Hook, ev HookEvent) goja.Value {
	if hook == HookSyncComplete {
		if ev.Sync == nil {
			return vm.ToValue(map[string]any{})
		}
		return vm.ToValue(map[string]any{
			"accounts":             ev.Sync.Accounts,
			"new_transactions":     ev.Sync.NewTransactions,
			"updated_transactions": ev.Sync.UpdatedTransactions,
			"auto_labeled":         ev.Sync.AutoLabeled,
			"duration":             ev.Sync.Duration,
		})
	}
	if ev.Transaction != nil {
		return txnToJS(vm, *ev.Transaction)
	}
	return goja.Null()
}

// txnToJS builds the plugin-facing transaction object. Field names match kasas's
// canonical snake_case JSON wire format (REST + events), so a JS author sees the same
// shape they would from the API; `date` is a real JS Date (see jsDate).
// labels/extensions are always objects (never null) so a plugin can iterate them.
func txnToJS(vm *goja.Runtime, t Transaction) goja.Value {
	lbls := t.Labels
	if lbls == nil {
		lbls = map[string]string{}
	}
	exts := t.Extensions
	if exts == nil {
		exts = map[string]any{}
	}
	return vm.ToValue(map[string]any{
		"id":          t.ID,
		"account_id":  t.AccountID,
		"amount":      t.Amount,
		"pending":     t.Pending,
		"date":        jsDate(vm, t.Date),
		"description": t.Description,
		"payee":       t.Payee,
		"memo":        t.Memo,
		"labels":      lbls,
		"extensions":  exts,
	})
}

// jsDate converts a Go time into a REAL JS Date through the VM's Date
// constructor. goja does not do this conversion itself — vm.ToValue(time.Time)
// wraps the Go value, whose methods (getTime, toISOString, …) don't exist —
// which would break the documented contract that `date` is a JS Date.
func jsDate(vm *goja.Runtime, t time.Time) goja.Value {
	ctor, ok := goja.AssertConstructor(vm.Get("Date"))
	if !ok {
		return vm.ToValue(t) // unreachable in practice: Date is a built-in
	}
	d, err := ctor(nil, vm.ToValue(t.UnixMilli()))
	if err != nil {
		return vm.ToValue(t)
	}
	return d
}

// exportStringMap reads a JS object argument into a Go string map (the labels shape),
// stringifying non-string values so a plugin passing a number or bool still works.
func exportStringMap(v goja.Value) map[string]string {
	out := map[string]string{}
	m, ok := v.Export().(map[string]any)
	if !ok {
		return out
	}
	for k, vv := range m {
		out[k] = stringifyJS(vv)
	}
	return out
}

// exportStringSlice reads a JS array argument into a Go string slice (the label-keys
// shape), skipping empty entries.
func exportStringSlice(v goja.Value) []string {
	arr, ok := v.Export().([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, e := range arr {
		if s := stringifyJS(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func stringifyJS(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
