package plugins

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// wasmRuntime is the wazero implementation of Runtime. wazero is a pure-Go
// WebAssembly runtime (no cgo), so WASM plugins preserve the single-static-binary,
// cross-compilable build exactly like the Lua and JS runtimes. A plugin is a WASI
// preview1 *reactor* — for Go authors, `GOOS=wasip1 GOARCH=wasm go build
// -buildmode=c-shared` against the pluginsdk/kasas package — but the ABI is plain
// wasm imports/exports over JSON, so any language that targets wasip1 can
// implement it.
//
// The sandbox here is stronger than the Lua/JS VMs: the module gets NO preopened
// directories and no sockets, so the WASI filesystem/network surface is dead on
// arrival, and the only way back into kasas is the same capability-checked host
// facade the other runtimes use.
type wasmRuntime struct{}

// NewWasmRuntime constructs the WASM runtime.
func NewWasmRuntime() Runtime { return wasmRuntime{} }

func (wasmRuntime) Name() string { return RuntimeWasm }

// The guest-facing ABI (host module "kasas", version 1). Everything is JSON over
// linear memory, moved by four host functions so the host never has to allocate
// inside the guest (which keeps the ABI independent of the guest's allocator and
// avoids re-entering the module mid-call):
//
//	input(ptr, cap) -> n          copy the current invocation payload into the guest
//	output(ptr, len)              set the invocation's result envelope
//	host_call(ptr, len) -> n      run a host op {"op":...}; returns response length
//	read_response(ptr, cap) -> n  copy (and consume) the pending host_call response
//
// The guest exports `kasas_describe` (writes {"ok":true,"abi":1,"hooks":[...]}
// via output) plus one export per implemented hook, named exactly like the hook,
// with signature (payload_len u32). A hook fetches its payload with input, does
// its work through host_call, and finishes by writing an envelope through output:
// {"ok":true}, {"ok":true,"page":{...}} for page hooks, or {"ok":false,"error":...}.
const (
	wasmABIVersion     = 1
	wasmHostModule     = "kasas"
	wasmExportDescribe = "kasas_describe"
	wasmExportInit     = "_initialize" // WASI reactor initializer (-buildmode=c-shared)

	// wasmMemoryLimitPages caps guest linear memory at 1 GiB (64 KiB pages) so a
	// leaky plugin cannot exhaust the host.
	wasmMemoryLimitPages = 16384

	// wasmLoadTimeout bounds instantiation + the describe handshake, so a module
	// that spins in a package initializer fails the load instead of hanging it.
	wasmLoadTimeout = 30 * time.Second
)

// Load compiles the wasm module, binds the host ABI, instantiates it, and runs the
// kasas_describe handshake to verify the ABI version and that every declared hook
// is actually implemented (keeping the manifest's hook list authoritative, like
// the Lua/JS load-time checks).
func (wasmRuntime) Load(ctx context.Context, m Manifest, dir string, host Host) (Instance, error) {
	bin, err := os.ReadFile(filepath.Join(dir, m.Entrypoint))
	if err != nil {
		return nil, fmt.Errorf("read entrypoint %s: %w", m.Entrypoint, err)
	}

	ctx, cancel := context.WithTimeout(ctx, wasmLoadTimeout)
	defer cancel()

	// CloseOnContextDone makes every guest call honor its context: when the
	// per-hook deadline fires, wazero interrupts execution (even a tight loop) by
	// closing the module. The instance then re-instantiates on the next
	// invocation — see ensureModule.
	rc := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(wasmMemoryLimitPages)
	r := wazero.NewRuntimeWithConfig(ctx, rc)

	wi := &wasmInstance{
		runtime: r,
		host:    host,
		name:    m.Name,
		config:  m.Config,
		stdout:  &wasmLogWriter{host: host, level: "info"},
		stderr:  &wasmLogWriter{host: host, level: "error"},
	}
	ok := false
	defer func() {
		if !ok {
			_ = r.Close(context.Background())
		}
	}()

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return nil, fmt.Errorf("instantiate WASI: %w", err)
	}
	if err := wi.instantiateHostModule(ctx); err != nil {
		return nil, err
	}

	wi.compiled, err = r.CompileModule(ctx, bin)
	if err != nil {
		return nil, fmt.Errorf("compile %s: %w", m.Entrypoint, err)
	}

	// The module config is reused verbatim by every re-instantiation. Real clock,
	// sleep, and entropy are provided (a Go guest's runtime needs them); the
	// filesystem is not — no preopens means every WASI path op fails, which is the
	// sandbox working as intended.
	wi.modConfig = wazero.NewModuleConfig().
		WithName(m.Name).
		WithArgs(m.Name).
		WithStdout(wi.stdout).
		WithStderr(wi.stderr).
		WithSysWalltime().
		WithSysNanotime().
		WithSysNanosleep().
		WithRandSource(crand.Reader).
		WithStartFunctions(wasmExportInit)

	if err := wi.ensureModule(ctx); err != nil {
		return nil, err
	}
	if err := wi.handshake(ctx, m); err != nil {
		return nil, err
	}
	ok = true
	return wi, nil
}

// handshake runs kasas_describe and checks the result against the manifest: the
// ABI version must match and every declared hook must be both exported by the
// module and reported as registered (an SDK guest exports every hook name
// unconditionally, so the describe list is what proves a handler exists).
func (wi *wasmInstance) handshake(ctx context.Context, m Manifest) error {
	if wi.mod.ExportedFunction(wasmExportDescribe) == nil {
		return fmt.Errorf("module exports no %s function — not a kasas plugin (build against the kasas plugin SDK, or implement ABI v%d)", wasmExportDescribe, wasmABIVersion)
	}
	res, err := wi.invokeExport(ctx, wasmExportDescribe, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", wasmExportDescribe, err)
	}
	if !res.OK {
		return fmt.Errorf("%s failed: %s", wasmExportDescribe, res.Error)
	}
	if res.ABI != wasmABIVersion {
		return fmt.Errorf("plugin implements ABI v%d, this kasas supports v%d", res.ABI, wasmABIVersion)
	}
	registered := make(map[string]bool, len(res.Hooks))
	for _, h := range res.Hooks {
		registered[h] = true
	}
	for _, h := range m.Hooks {
		if wi.mod.ExportedFunction(string(h)) == nil || !registered[string(h)] {
			return fmt.Errorf("hook %q declared but the module registers no handler for it", h)
		}
	}
	return nil
}

// wasmInstance is one loaded WASM plugin. The manager invokes it on a single
// goroutine (the plugin's worker), and wazero host functions run on the calling
// goroutine, so the in/out/resp scratch state needs no locking.
type wasmInstance struct {
	runtime   wazero.Runtime
	compiled  wazero.CompiledModule
	modConfig wazero.ModuleConfig
	mod       api.Module
	host      Host
	name      string
	config    map[string]any
	stdout    *wasmLogWriter
	stderr    *wasmLogWriter

	// started tracks whether the module has been instantiated at least once, so a
	// RE-instantiation (after a timeout, trap, or exit) is logged.
	started bool

	// Per-invocation scratch buffers backing the ABI's four data-moving functions.
	in   []byte // payload served to the guest via input
	out  []byte // result envelope the guest set via output
	resp []byte // pending host_call response, consumed by read_response
}

var _ Instance = (*wasmInstance)(nil)

// wasmResult is the guest's result envelope (also the describe payload).
type wasmResult struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Page  json.RawMessage `json:"page,omitempty"`
	ABI   int             `json:"abi,omitempty"`
	SDK   string          `json:"sdk,omitempty"`
	Hooks []string        `json:"hooks,omitempty"`
}

// Invoke runs the plugin's handler for hook with the event payload.
func (wi *wasmInstance) Invoke(ctx context.Context, hook Hook, ev HookEvent) error {
	payload, err := wasmHookPayload(hook, ev)
	if err != nil {
		return err
	}
	res, err := wi.invokeExport(ctx, string(hook), payload)
	if err != nil {
		return err
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = "unknown error"
		}
		return fmt.Errorf("plugin error: %s", res.Error)
	}
	return nil
}

// Render runs a value-returning page hook and returns the page document the
// handler produced, still untrusted until ValidatePageDoc accepts it.
func (wi *wasmInstance) Render(ctx context.Context, hook Hook, req PageRequest) (json.RawMessage, error) {
	if req.Params == nil {
		req.Params = map[string]string{}
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	res, err := wi.invokeExport(ctx, string(hook), payload)
	if err != nil {
		return nil, err
	}
	if !res.OK {
		if res.Error == "" {
			res.Error = "unknown error"
		}
		return nil, fmt.Errorf("plugin error: %s", res.Error)
	}
	if len(res.Page) == 0 {
		return nil, fmt.Errorf("%s returned nothing (expected a page object)", hook)
	}
	return res.Page, nil
}

// Close releases the wazero runtime, which closes the module, the host modules,
// and the compiled code.
func (wi *wasmInstance) Close() error {
	wi.stdout.flush()
	wi.stderr.flush()
	return wi.runtime.Close(context.Background())
}

// invokeExport calls one guest export under the shared safety envelope: the
// module is (re)instantiated if a previous failure killed it, the scratch
// buffers are armed, and any call failure tears the module down so the next
// invocation starts from a clean instance.
func (wi *wasmInstance) invokeExport(ctx context.Context, name string, payload []byte) (*wasmResult, error) {
	if err := wi.ensureModule(ctx); err != nil {
		return nil, err
	}
	fn := wi.mod.ExportedFunction(name)
	if fn == nil {
		return nil, ErrHookNotImpl
	}

	wi.in, wi.out, wi.resp = payload, nil, nil
	var args []uint64
	if name != wasmExportDescribe {
		args = []uint64{uint64(len(payload))}
	}
	_, err := fn.Call(ctx, args...)
	wi.in, wi.resp = nil, nil
	wi.stdout.flush()
	wi.stderr.flush()
	if err != nil {
		// The instance is unusable (wazero closed it on a context interrupt, or a
		// trap/exit left the guest runtime in an unknown state). Drop it; the next
		// invocation re-instantiates from the compiled module.
		wi.teardownModule()
		return nil, wi.callError(ctx, name, err)
	}
	if wi.out == nil {
		return nil, fmt.Errorf("%s returned no result (the module must write an envelope via the output host function)", name)
	}
	var res wasmResult
	if uerr := json.Unmarshal(wi.out, &res); uerr != nil {
		return nil, fmt.Errorf("%s result: %w", name, uerr)
	}
	return &res, nil
}

// ensureModule instantiates the compiled module if there is no live instance —
// at load, and again after a failed invocation killed the previous instance.
func (wi *wasmInstance) ensureModule(ctx context.Context) error {
	if wi.mod != nil && !wi.mod.IsClosed() {
		return nil
	}
	wi.teardownModule()
	mod, err := wi.runtime.InstantiateModule(ctx, wi.compiled, wi.modConfig)
	wi.stdout.flush()
	wi.stderr.flush()
	if err != nil {
		return fmt.Errorf("instantiate module: %w", err)
	}
	wi.mod = mod
	if wi.started {
		wi.host.Log("warn", "plugin module re-instantiated after a failed invocation (in-memory state was reset)", nil)
	}
	wi.started = true
	return nil
}

func (wi *wasmInstance) teardownModule() {
	if wi.mod != nil {
		_ = wi.mod.Close(context.Background())
		wi.mod = nil
	}
}

// callError translates a wazero call failure: a fired deadline surfaces as the
// context error (matching the Lua/JS timeout behavior), a guest exit (a Go panic
// exits the module with code 2, after printing the panic to stderr — which lands
// in the plugin log) is reported with its code, and anything else (a trap) is
// passed through.
func (wi *wasmInstance) callError(ctx context.Context, name string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%s interrupted: %w", name, ctxErr)
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s: module exited with code %d (a Go guest panic exits with code 2; the panic trace is in the plugin log)", name, exit.ExitCode())
	}
	return fmt.Errorf("%s: %w", name, err)
}

// wasmHookPayload builds the JSON payload for an event hook: the transaction for
// transaction hooks, the sync summary for OnSyncComplete (mirroring the argument
// the Lua/JS runtimes pass), and null for payload-less lifecycle hooks.
func wasmHookPayload(hook Hook, ev HookEvent) ([]byte, error) {
	if hook == HookSyncComplete {
		s := ev.Sync
		if s == nil {
			s = &SyncSummary{}
		}
		return json.Marshal(s)
	}
	if ev.Transaction != nil {
		return json.Marshal(ev.Transaction)
	}
	return []byte("null"), nil
}

// --- host module (the guest's only way back into kasas) ---

// instantiateHostModule binds the four ABI functions. They close over the
// instance's scratch buffers and route every operation through the
// capability-checked Host facade, so a WASM plugin gets exactly the enforcement
// the Lua/JS plugins get.
func (wi *wasmInstance) instantiateHostModule(ctx context.Context) error {
	_, err := wi.runtime.NewHostModuleBuilder(wasmHostModule).
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, capacity uint32) uint32 {
			return wasmCopyOut(mod, ptr, capacity, wi.in)
		}).
		Export("input").
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) {
			if b, ok := mod.Memory().Read(ptr, length); ok {
				wi.out = append([]byte(nil), b...)
			}
		}).
		Export("output").
		NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, ptr, length uint32) uint32 {
			raw, ok := mod.Memory().Read(ptr, length)
			if !ok {
				wi.resp = wasmErrEnvelope(fmt.Errorf("host_call request out of bounds"))
			} else {
				wi.resp = wi.execHostCall(ctx, raw)
			}
			return uint32(len(wi.resp))
		}).
		Export("host_call").
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, capacity uint32) uint32 {
			n := wasmCopyOut(mod, ptr, capacity, wi.resp)
			wi.resp = nil // one-shot: a response is consumed by exactly one read
			return n
		}).
		Export("read_response").
		Instantiate(ctx)
	if err != nil {
		return fmt.Errorf("instantiate host module: %w", err)
	}
	return nil
}

// wasmCopyOut writes up to cap bytes of src into guest memory at ptr, returning
// how many were written. A guest passing a too-small buffer gets a truncated
// copy, which the SDK never does (it allocates exactly the announced length).
func wasmCopyOut(mod api.Module, ptr, capacity uint32, src []byte) uint32 {
	n := uint32(len(src))
	if n > capacity {
		n = capacity
	}
	if n == 0 {
		return 0
	}
	if !mod.Memory().Write(ptr, src[:n]) {
		return 0
	}
	return n
}

// wasmHostReq is the host_call request envelope. One flat shape keeps the ABI to
// a single op-dispatched function, so new host methods never change the wasm
// surface — they are just new op values.
type wasmHostReq struct {
	Op      string            `json:"op"`
	ID      string            `json:"id,omitempty"`
	Query   string            `json:"query,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Keys    []string          `json:"keys,omitempty"`
	Key     string            `json:"key,omitempty"`
	Value   json.RawMessage   `json:"value,omitempty"`
	Level   string            `json:"level,omitempty"`
	Msg     string            `json:"msg,omitempty"`
	KV      map[string]any    `json:"kv,omitempty"`
	Changes map[string]any    `json:"changes,omitempty"`
}

type wasmHostResp struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

func wasmOKEnvelope(data any) []byte {
	b, err := json.Marshal(wasmHostResp{OK: true, Data: data})
	if err != nil {
		return wasmErrEnvelope(fmt.Errorf("encode host response: %w", err))
	}
	return b
}

func wasmErrEnvelope(err error) []byte {
	b, merr := json.Marshal(wasmHostResp{Error: err.Error()})
	if merr != nil {
		return []byte(`{"ok":false,"error":"host response encoding failed"}`)
	}
	return b
}

// execHostCall dispatches one host op. ctx is the invocation context wazero
// hands to host functions, so host work shares the per-hook deadline (the same
// property the Lua/JS instances get by stashing the invocation context).
func (wi *wasmInstance) execHostCall(ctx context.Context, raw []byte) []byte {
	var req wasmHostReq
	if err := json.Unmarshal(raw, &req); err != nil {
		return wasmErrEnvelope(fmt.Errorf("decode host_call request: %w", err))
	}
	switch req.Op {
	case "get_transaction":
		t, err := wi.host.GetTransaction(ctx, req.ID)
		if errors.Is(err, ErrTxnNotFound) {
			return wasmOKEnvelope(nil) // null, like Lua's nil / JS's null
		}
		if err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(t)
	case "search":
		txns, err := wi.host.Search(ctx, req.Query, req.Limit)
		if err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(txns)
	case "apply_labels":
		if err := wi.host.ApplyLabels(ctx, req.ID, req.Labels); err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(nil)
	case "remove_labels":
		if err := wi.host.RemoveLabels(ctx, req.ID, req.Keys); err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(nil)
	case "set_extension":
		value := req.Value
		if len(value) == 0 {
			value = json.RawMessage("null")
		}
		if err := wi.host.SetExtension(ctx, req.ID, req.Key, value); err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(nil)
	case "remove_extension":
		if err := wi.host.RemoveExtension(ctx, req.ID, req.Key); err != nil {
			return wasmErrEnvelope(err)
		}
		return wasmOKEnvelope(nil)
	case "log":
		wi.host.Log(req.Level, req.Msg, req.KV)
		return wasmOKEnvelope(nil)
	case "get_config":
		return wasmOKEnvelope(wi.config)
	case "set_config":
		merged, err := wi.host.SetConfig(ctx, req.Changes)
		if err != nil {
			return wasmErrEnvelope(err)
		}
		wi.config = merged // keep get_config coherent after a save
		return wasmOKEnvelope(merged)
	default:
		return wasmErrEnvelope(fmt.Errorf("unknown host op %q", req.Op))
	}
}

// --- guest stdout/stderr -> plugin log ---

// wasmLogWriter forwards the guest's stdout/stderr to the plugin log, one line
// per entry (the WASM analogue of the JS console shim). A Go guest's
// fmt.Println lands at info level via stdout; a panic trace lands at error
// level via stderr, right before the module exits.
type wasmLogWriter struct {
	host  Host
	level string
	buf   []byte
}

// maxWasmLogLine bounds a buffered partial line so a guest printing endlessly
// without newlines cannot grow the buffer unbounded.
const maxWasmLogLine = 8 << 10

func (w *wasmLogWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxWasmLogLine {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// flush logs any buffered partial line; called after each invocation and at
// close so trailing output (like a panic's last line) is never lost.
func (w *wasmLogWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
}

func (w *wasmLogWriter) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.host.Log(w.level, line, nil)
}
