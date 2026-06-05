// Command kasas-wasm is the WebAssembly entrypoint for the kasas dashboard.
// It is built with GOOS=js GOARCH=wasm into internal/dashboard/web/app.wasm and
// runs in the browser. Built for the host it is a harmless no-op
// (app.RunWhenOnBrowser does nothing off-browser), which keeps `go build ./...`
// and the test suite green without a separate WASM toolchain.
package main

import (
	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/paulmeier/kasas/internal/dashboard"
)

func main() {
	dashboard.Routes()
	app.RunWhenOnBrowser()
}
