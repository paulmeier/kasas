package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeRegistry serves an index and raw files over httptest, mimicking the layout
// the client expects: <repo>/raw/<ref>/<path>/<file>.
type fakeRegistry struct {
	srv   *httptest.Server
	files map[string]string // raw URL path -> contents
}

func newFakeRegistry(t *testing.T, idx *Index, files map[string]string) *fakeRegistry {
	t.Helper()
	fr := &fakeRegistry{files: files}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(idx)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := fr.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})
	fr.srv = httptest.NewServer(mux)
	t.Cleanup(fr.srv.Close)
	return fr
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func contentHash(files []FileRef) string {
	sorted := make([]FileRef, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%s\x00%s\n", f.Path, strings.ToLower(f.SHA256))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// buildEntry constructs a valid entry plus the raw-file map the server should serve.
func buildEntry(repoBase, name string, contents map[string]string) (Entry, map[string]string) {
	var files []FileRef
	raw := map[string]string{}
	path := "plugins/" + name
	for fname, body := range contents {
		files = append(files, FileRef{Path: fname, SHA256: sha(body), Size: int64(len(body))})
		raw["/raw/main/"+path+"/"+fname] = body
	}
	e := Entry{
		Name:        name,
		Version:     "1.0.0",
		Runtime:     "lua",
		Entrypoint:  "main.lua",
		Path:        path,
		Files:       files,
		ContentHash: contentHash(files),
	}
	return e, raw
}

func TestCatalogAndDownload(t *testing.T) {
	contents := map[string]string{
		"plugin.toml": "name=\"coffee\"\n",
		"main.lua":    "function OnTransactionCreate(t) end\n",
		"README.md":   "# coffee\n",
	}
	idx := &Index{SchemaVersion: 1}
	// Repository will be set to the test server's URL below.
	fr := newFakeRegistry(t, idx, nil)
	entry, raw := buildEntry(fr.srv.URL, "coffee", contents)
	idx.Repository = fr.srv.URL
	idx.Plugins = []Entry{entry}
	fr.files = raw

	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), DefaultLimits())

	got, err := c.Catalog(context.Background())
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "coffee" {
		t.Fatalf("unexpected catalog: %+v", got)
	}

	dest := t.TempDir()
	if err := c.Download(context.Background(), got.Repository, got.Plugins[0], dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	for fname, body := range contents {
		b, err := os.ReadFile(filepath.Join(dest, fname))
		if err != nil {
			t.Fatalf("read %s: %v", fname, err)
		}
		if string(b) != body {
			t.Fatalf("file %s content mismatch", fname)
		}
	}
}

func TestDownloadRejectsTamperedFile(t *testing.T) {
	contents := map[string]string{"plugin.toml": "name=\"x\"\n", "main.lua": "ok\n"}
	idx := &Index{SchemaVersion: 1}
	fr := newFakeRegistry(t, idx, nil)
	entry, raw := buildEntry(fr.srv.URL, "x", contents)
	// Tamper: serve different bytes than the hash promises.
	raw["/raw/main/plugins/x/main.lua"] = "EVIL PAYLOAD\n"
	idx.Repository = fr.srv.URL
	idx.Plugins = []Entry{entry}
	fr.files = raw

	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), DefaultLimits())
	err := c.Download(context.Background(), fr.srv.URL, entry, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
		t.Fatalf("expected integrity failure, got %v", err)
	}
}

func TestDownloadRejectsPathTraversal(t *testing.T) {
	idx := &Index{SchemaVersion: 1}
	fr := newFakeRegistry(t, idx, map[string]string{})
	entry := Entry{
		Name: "evil", Version: "1.0.0", Runtime: "lua", Path: "plugins/evil",
		Files: []FileRef{{Path: "../escape.lua", SHA256: sha("x"), Size: 1}},
	}
	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), DefaultLimits())
	err := c.Download(context.Background(), fr.srv.URL, entry, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unsafe file path") {
		t.Fatalf("expected path-traversal rejection, got %v", err)
	}
}

func TestDownloadRejectsContentHashMismatch(t *testing.T) {
	contents := map[string]string{"plugin.toml": "name=\"x\"\n", "main.lua": "ok\n"}
	idx := &Index{SchemaVersion: 1}
	fr := newFakeRegistry(t, idx, nil)
	entry, raw := buildEntry(fr.srv.URL, "x", contents)
	entry.ContentHash = "sha256:deadbeef" // wrong aggregate
	idx.Repository = fr.srv.URL
	fr.files = raw

	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), DefaultLimits())
	err := c.Download(context.Background(), fr.srv.URL, entry, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "content hash mismatch") {
		t.Fatalf("expected content-hash mismatch, got %v", err)
	}
}

func TestCatalogRejectsUnsupportedSchema(t *testing.T) {
	idx := &Index{SchemaVersion: 99}
	fr := newFakeRegistry(t, idx, nil)
	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), DefaultLimits())
	_, err := c.Catalog(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported registry schema version") {
		t.Fatalf("expected schema rejection, got %v", err)
	}
}

func TestDownloadEnforcesFileSizeLimit(t *testing.T) {
	big := strings.Repeat("a", 200)
	contents := map[string]string{"plugin.toml": "name=\"x\"\n", "main.lua": big}
	idx := &Index{SchemaVersion: 1}
	fr := newFakeRegistry(t, idx, nil)
	entry, raw := buildEntry(fr.srv.URL, "x", contents)
	fr.files = raw

	c := New(fr.srv.URL+"/index.json", "main", fr.srv.Client(), Limits{MaxFileBytes: 50, MaxTotalBytes: 1000, MaxFiles: 10})
	err := c.Download(context.Background(), fr.srv.URL, entry, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "per-file limit") {
		t.Fatalf("expected per-file limit error, got %v", err)
	}
}
