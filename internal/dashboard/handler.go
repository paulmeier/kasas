package dashboard

import (
	"bytes"
	"compress/gzip"
	"embed"
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
		Version:         opts.Version,
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

	mux := http.NewServeMux()
	mux.HandleFunc("/web/app.wasm", serveWasm)
	mux.Handle("/web/", http.StripPrefix("/web/", http.FileServer(http.FS(static))))
	mux.Handle("/", goapp)
	return mux
}

// serveWasm serves the embedded, pre-gzipped WASM. Browsers always accept gzip;
// the rare client that does not gets it decompressed on the fly.
func serveWasm(w http.ResponseWriter, r *http.Request) {
	gz, err := webFS.ReadFile("web/app.wasm.gz")
	if err != nil {
		http.NotFound(w, r) // WASM not built yet
		return
	}
	w.Header().Set("Content-Type", "application/wasm")
	w.Header().Set("Cache-Control", "public, max-age=86400")

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
