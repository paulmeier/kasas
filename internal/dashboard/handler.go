package dashboard

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// webFS holds the dashboard's static assets. The directory (not the individual
// files) is embedded so the package still builds when the WASM has not been
// compiled yet — app.wasm.gz is simply absent until `make wasm` produces
// internal/dashboard/web/app.wasm.gz.
//
// The WASM is embedded gzip-compressed (~3-4 MB vs ~12 MB raw): it keeps the
// binary/image small and is served with Content-Encoding: gzip.
//
//go:embed web
var webFS embed.FS

// Options configures the dashboard handler.
type Options struct {
	Version string // app version, used for cache-busting across releases
}

// Handler returns an http.Handler that serves the go-app dashboard: the
// generated bootstrap HTML and runtime (app.js, wasm_exec.js, manifest, service
// worker) plus the embedded app.wasm and stylesheet under /web/.
func Handler(opts Options) http.Handler {
	// Register the client routes on the server too: go-app's Handler serves the
	// SPA index only for known routes (the WASM client registers them in its own
	// process; the server must as well, or page paths like "/" 404).
	Routes()

	version := dashboardVersion(opts.Version, webFS)

	goapp := &app.Handler{
		Name:            "kasas",
		ShortName:       "kasas",
		Title:           "kasas — transactions",
		Description:     "Browse your synced SimpleFIN accounts and transactions.",
		Author:          "kasas",
		Lang:            "en",
		BackgroundColor: "#0f1115",
		ThemeColor:      "#0f1115",
		LoadingLabel:    "loading kasas… {progress}%",
		Version:         version,
		Styles:          []string{"/web/dashboard.css"},
		Icon: app.Icon{
			Default: "/web/logo.png", // favicon + PWA icon
			Large:   "/web/logo.png",
		},
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err) // only fails on a malformed embed path (programmer error)
	}

	// ETag for the wasm = a hash of its content, so the browser revalidates
	// cheaply (304) instead of trusting a long max-age. "" when not built yet.
	wasmETag := ""
	if h := wasmHash(webFS); h != "" {
		wasmETag = `"` + h + `"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/web/app.wasm", serveWasm(webFS, wasmETag))
	// version is the same string go-app uses as its service-worker cache key
	// (binary version + wasm content hash). The dashboard reads it for the
	// build badge; app.Getenv("GOAPP_VERSION") is unreliable in the WASM build,
	// so it is exposed over HTTP instead. Never cached, so the badge is current.
	mux.HandleFunc("/web/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(version))
	})
	mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.FS(static))))
	mux.Handle("/", goapp)
	return mux
}

// dashboardVersion derives go-app's cache-busting Version. go-app names its
// service-worker cache "app-<Version>" and only refetches assets when Version
// changes. Releases bump the base version, but local builds report a static
// "dev", so a bare version string would leave the service worker serving a
// stale app.wasm (and thus an old UI) after every rebuild. Mixing in a short
// hash of the embedded wasm's *content* makes the cache key change exactly when
// the UI bytes change — busting the cache on every meaningful rebuild while
// staying stable across identical builds. Falls back to the bare base when the
// wasm has not been built yet (the gz is absent until `make wasm`).
func dashboardVersion(base string, fsys fs.FS) string {
	if base == "" {
		base = "dev"
	}
	if h := wasmHash(fsys); h != "" {
		return base + "-" + h
	}
	return base
}

// wasmHash returns a short hex hash of the *decompressed* app.wasm content, or
// "" when the wasm has not been built yet (the gz is absent until `make wasm`).
// Hashing the decompressed bytes — not the .gz, whose gzip header carries an
// mtime that changes every build — keeps the value stable across identical
// builds and changing only when the UI bytes do. Reused for both the cache-bust
// version and the wasm ETag.
func wasmHash(fsys fs.FS) string {
	gz, err := fs.ReadFile(fsys, "web/app.wasm.gz")
	if err != nil {
		return ""
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return ""
	}
	defer zr.Close()
	h := sha256.New()
	if _, err := io.Copy(h, zr); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// serveWasm serves the embedded, pre-gzipped WASM. Browsers always accept gzip;
// the rare client that does not gets it decompressed on the fly.
//
// The URL is unversioned (/web/app.wasm), so it must not be cached without
// revalidation: go-app's service worker repopulates its per-version cache with
// cache.addAll(), which honours the HTTP cache, so a long max-age would let a
// stale wasm survive a rebuild (the new version key would cache the old bytes,
// i.e. an old UI). An ETag plus "no-cache" makes the browser revalidate on
// every load and get a cheap 304 when the wasm is unchanged.
func serveWasm(fsys fs.FS, etag string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gz, err := fs.ReadFile(fsys, "web/app.wasm.gz")
		if err != nil {
			http.NotFound(w, r) // WASM not built yet
			return
		}
		w.Header().Set("Content-Type", "application/wasm")
		w.Header().Set("Cache-Control", "no-cache")
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Length", strconv.Itoa(len(gz)))
			_, _ = w.Write(gz)
			return
		}

		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			http.Error(w, "wasm decode error", http.StatusInternalServerError)
			return
		}
		defer zr.Close()
		_, _ = io.Copy(w, zr)
	}
}
