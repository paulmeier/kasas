//go:build wasip1

package kasas

// This file is the wasm side of the kasas plugin ABI (v1): four host functions
// imported from the "kasas" module move JSON between guest and host, and one
// export per hook (plus kasas_describe) lets the host invoke the plugin. All
// data passing is (pointer, length) into guest linear memory — the host never
// allocates inside the guest.

import (
	"runtime"
	"unsafe"
)

//go:wasmimport kasas input
func abiInput(ptr unsafe.Pointer, capacity uint32) uint32

//go:wasmimport kasas output
func abiOutput(ptr unsafe.Pointer, length uint32)

//go:wasmimport kasas host_call
func abiHostCall(ptr unsafe.Pointer, length uint32) uint32

//go:wasmimport kasas read_response
func abiReadResponse(ptr unsafe.Pointer, capacity uint32) uint32

// rawInput copies the current invocation's payload (whose length arrived as the
// hook argument) out of the host.
func rawInput(payloadLen uint32) []byte {
	if payloadLen == 0 {
		return nil
	}
	buf := make([]byte, payloadLen)
	got := abiInput(unsafe.Pointer(&buf[0]), payloadLen)
	runtime.KeepAlive(buf)
	if got > payloadLen {
		got = payloadLen
	}
	return buf[:got]
}

// rawOutput hands the invocation's result envelope to the host.
func rawOutput(b []byte) {
	if len(b) == 0 {
		return
	}
	abiOutput(unsafe.Pointer(&b[0]), uint32(len(b)))
	runtime.KeepAlive(b)
}

// rawHostCall performs one host op and fetches its response.
func rawHostCall(req []byte) []byte {
	var ptr unsafe.Pointer
	if len(req) > 0 {
		ptr = unsafe.Pointer(&req[0])
	}
	n := abiHostCall(ptr, uint32(len(req)))
	runtime.KeepAlive(req)
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	got := abiReadResponse(unsafe.Pointer(&buf[0]), n)
	runtime.KeepAlive(buf)
	if got > n {
		got = n
	}
	return buf[:got]
}

// --- exports ---
//
// Every hook is exported unconditionally (a wasmexport cannot be conditional);
// the describe handshake reports which hooks actually have registered handlers,
// and the host refuses to load a plugin whose manifest declares a hook that was
// never registered.

//go:wasmexport kasas_describe
func exportDescribe() { dispatchDescribe() }

//go:wasmexport OnTransactionCreate
func exportOnTransactionCreate(payloadLen uint32) {
	dispatchTransaction(hookTransactionCreate, payloadLen)
}

//go:wasmexport OnTransactionUpdate
func exportOnTransactionUpdate(payloadLen uint32) {
	dispatchTransaction(hookTransactionUpdate, payloadLen)
}

//go:wasmexport OnTransactionDelete
func exportOnTransactionDelete(payloadLen uint32) {
	dispatchTransaction(hookTransactionDelete, payloadLen)
}

//go:wasmexport OnSyncComplete
func exportOnSyncComplete(payloadLen uint32) { dispatchSync(payloadLen) }

//go:wasmexport OnUninstall
func exportOnUninstall(_ uint32) { dispatchUninstall() }

//go:wasmexport OnPageRender
func exportOnPageRender(payloadLen uint32) { dispatchPage(hookPageRender, payloadLen) }

//go:wasmexport OnPageAction
func exportOnPageAction(payloadLen uint32) { dispatchPage(hookPageAction, payloadLen) }

//go:wasmexport OnFetch
func exportOnFetch(payloadLen uint32) { dispatchFetch(payloadLen) }
