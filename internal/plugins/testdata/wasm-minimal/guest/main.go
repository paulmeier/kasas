// The smallest possible Go/WASM plugin: one registered hook that does nothing.
// Tests use it to prove that a manifest declaring a hook the guest never
// registered fails to load (the describe handshake is authoritative).
package main

import kasas "github.com/paulmeier/kasas/pluginsdk/kasas"

func init() {
	kasas.OnTransactionCreate(func(*kasas.Transaction) error { return nil })
}

func main() {} // required by -buildmode=c-shared, never runs
