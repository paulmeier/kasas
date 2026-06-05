package dashboard

import (
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"testing/fstest"
)

// gzipBytes returns payload gzip-compressed, the way the embedded app.wasm.gz is
// stored on disk.
func gzipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(payload)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// TestDashboardVersionBustsOnWasmChange guards the service-worker cache-busting:
// go-app caches assets under "app-<Version>" and only refetches when Version
// changes, so Version MUST change when the wasm content changes — otherwise
// browsers keep running a stale dashboard after an update (the bug that a static
// "dev" version caused).
func TestDashboardVersionBustsOnWasmChange(t *testing.T) {
	fsA := fstest.MapFS{"web/app.wasm.gz": {Data: gzipBytes(t, "wasm-A")}}
	fsB := fstest.MapFS{"web/app.wasm.gz": {Data: gzipBytes(t, "wasm-B")}}

	vA := dashboardVersion("v1.2.3", fsA)
	vB := dashboardVersion("v1.2.3", fsB)

	if vA == vB {
		t.Fatalf("version must differ when wasm content differs, both = %q", vA)
	}
	if !strings.HasPrefix(vA, "v1.2.3-") {
		t.Fatalf("version should carry the base for readability, got %q", vA)
	}
	if got := dashboardVersion("v1.2.3", fsA); got != vA {
		t.Fatalf("version must be stable for identical content: %q != %q", got, vA)
	}
}

// TestDashboardVersionFallbacks covers the empty base and the not-yet-built wasm.
func TestDashboardVersionFallbacks(t *testing.T) {
	// Empty base defaults to "dev" (still hashed when the wasm is present).
	withWasm := fstest.MapFS{"web/app.wasm.gz": {Data: gzipBytes(t, "x")}}
	if got := dashboardVersion("", withWasm); !strings.HasPrefix(got, "dev-") {
		t.Fatalf("empty base should default to dev, got %q", got)
	}
	// No wasm built yet -> bare base, no hash suffix (graceful; the route 404s
	// until `make wasm` produces app.wasm.gz).
	if got := dashboardVersion("dev", fstest.MapFS{}); got != "dev" {
		t.Fatalf("missing wasm should fall back to the bare base, got %q", got)
	}
}
