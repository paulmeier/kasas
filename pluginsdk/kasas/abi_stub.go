//go:build !wasip1

package kasas

// Non-wasm stubs so a plugin (and this package) still type-checks, vets, and
// lints on the host platform. The host API only functions inside a kasas WASM
// plugin; at runtime outside one, it panics with build instructions.

const errNotWasm = "kasas plugin SDK: the host API only works inside a kasas plugin — build with: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o main.wasm ."

// The dispatch core's only caller is the wasmexport bridge (abi_wasip1.go),
// which build tags exclude here — anchor it so host-platform linting sees it
// used.
var _ = []any{dispatchDescribe, dispatchTransaction, dispatchSync, dispatchUninstall, dispatchPage, dispatchFetch}

func rawInput(uint32) []byte { panic(errNotWasm) }

func rawOutput([]byte) { panic(errNotWasm) }

func rawHostCall([]byte) []byte { panic(errNotWasm) }
