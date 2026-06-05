package dashboard

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
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

// TestServeWasmRevalidation guards the cache-control fix: the wasm URL is
// unversioned, and go-app's service worker repopulates its cache via fetch()
// (which honours the HTTP cache), so a long max-age would let a stale wasm
// survive a rebuild. The wasm must therefore be served with "no-cache" + an
// ETag, and a matching If-None-Match must revalidate to 304.
func TestServeWasmRevalidation(t *testing.T) {
	gz := gzipBytes(t, "wasm-A")
	fsys := fstest.MapFS{"web/app.wasm.gz": {Data: gz}}
	const etag = `"abc123"`
	h := serveWasm(fsys, etag)

	// Full response: revalidatable headers and the gzipped body.
	req := httptest.NewRequest(http.MethodGet, "/web/app.wasm", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want %q (a long max-age lets a stale wasm survive a rebuild)", cc, "no-cache")
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("ETag = %q, want %q", got, etag)
	}
	if !bytes.Equal(rec.Body.Bytes(), gz) {
		t.Fatalf("body should be the gzipped wasm bytes")
	}

	// Matching If-None-Match revalidates cheaply to 304 with no body.
	req2 := httptest.NewRequest(http.MethodGet, "/web/app.wasm", nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	h(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("If-None-Match status = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Fatalf("304 must have an empty body, got %d bytes", rec2.Body.Len())
	}
}

// TestServeWasmNotBuilt: the route 404s gracefully when the wasm is absent.
func TestServeWasmNotBuilt(t *testing.T) {
	h := serveWasm(fstest.MapFS{}, "")
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/web/app.wasm", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when wasm is not built", rec.Code)
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
