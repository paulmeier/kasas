// Package registry is kasas's client for a community plugin registry: the
// machine-readable catalog published by a kasas-plugins repository (see that
// repo's docs/registry.md for the contract). It fetches the index, and downloads a
// plugin's files while verifying their integrity hashes, so the marketplace can
// turn "reviewed in the registry" into "running on this machine" without trusting
// the transport.
//
// This package is deliberately thin and side-effect-light: it speaks HTTP and
// writes verified bytes into a caller-provided staging directory. The lifecycle
// orchestration (swapping a staging dir into plugins.dir, registering the plugin,
// reloading) lives in the plugins.Manager, which owns that state.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SupportedSchemaVersion is the index format this client understands. The client
// refuses an index with a different version rather than guessing at an
// incompatible shape.
const SupportedSchemaVersion = 1

// nameRE constrains a plugin name to the same filesystem- and identity-safe slug
// the host and registry use. A name that fails this is rejected before it is ever
// used to build a path.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Index is the catalog document fetched from the registry.
type Index struct {
	SchemaVersion int       `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Repository    string    `json:"repository"`
	Plugins       []Entry   `json:"plugins"`
}

// Entry is one listed plugin: manifest metadata plus the registry-computed
// integrity data the installer verifies against.
type Entry struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Description    string   `json:"description"`
	Author         string   `json:"author"`
	License        string   `json:"license"`
	Homepage       string   `json:"homepage"`
	Runtime        string   `json:"runtime"`
	Entrypoint     string   `json:"entrypoint"`
	Hooks          []string `json:"hooks"`
	Capabilities   []string `json:"capabilities"`
	CapabilityTier string   `json:"capability_tier"`
	// UI is present when the plugin contributes a dashboard page (a [ui] manifest
	// block), so the marketplace can badge it before install.
	UI          *UIRef    `json:"ui,omitempty"`
	Path        string    `json:"path"`
	Files       []FileRef `json:"files"`
	ContentHash string    `json:"content_hash"`
	SizeBytes   int64     `json:"size_bytes"`
}

// UIRef is the registry's ui metadata: the sidebar title and curated icon name
// of the plugin's dashboard page.
type UIRef struct {
	Title string `json:"title"`
	Icon  string `json:"icon"`
}

// FileRef is one installable file with its integrity hash.
type FileRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Limits bound what the client will accept from a registry, so a malicious or
// broken index cannot exhaust disk. They mirror the registry's own submission
// limits with headroom: the registry caps source files at 256 KiB and a plugin's
// total at 1 MiB, but a wasm plugin's compiled entrypoint has its own 8 MiB
// budget exempt from both, so the largest plugin a registry will list is ~9 MiB
// and the per-file limit here must clear its wasm module. One per-file knob
// (rather than one per file type) is deliberate: the index, not the bytes,
// declares which file is the entrypoint, so a type-specific budget would not
// constrain a malicious index any further than the total cap already does.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxFiles      int
}

// DefaultLimits are applied when a Client is created without explicit limits.
func DefaultLimits() Limits {
	return Limits{
		MaxFileBytes:  16 * 1024 * 1024, // 16 MiB: 2x the registry's 8 MiB wasm-entrypoint budget
		MaxTotalBytes: 20 * 1024 * 1024, // 20 MiB: ~2x the registry's worst case (8 MiB wasm + 1 MiB rest)
		MaxFiles:      64,
	}
}

// Client fetches a registry index and downloads plugin files. The zero value is
// not usable; construct it with New.
type Client struct {
	indexURL string
	ref      string // git ref used to build raw file-download URLs
	http     *http.Client
	limits   Limits
}

// New constructs a Client for the given index URL and git ref (e.g. "main"). A nil
// httpClient gets a sensible default with a timeout.
func New(indexURL, ref string, httpClient *http.Client, limits Limits) *Client {
	if httpClient == nil {
		// The timeout bounds a whole request including its body, and each file is
		// its own request; it is sized so the largest legitimate file (an 8 MiB
		// wasm entrypoint) survives a slow link.
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if ref == "" {
		ref = "main"
	}
	if limits.MaxFileBytes == 0 {
		limits = DefaultLimits()
	}
	return &Client{indexURL: indexURL, ref: ref, http: httpClient, limits: limits}
}

// Catalog fetches and validates the registry index.
func (c *Client) Catalog(ctx context.Context) (*Index, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.indexURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch registry index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch registry index: status %d", resp.StatusCode)
	}

	// Cap the index read; a catalog is small, and an unbounded body is a DoS vector.
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.limits.MaxTotalBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read registry index: %w", err)
	}
	if int64(len(body)) > c.limits.MaxTotalBytes {
		return nil, fmt.Errorf("registry index exceeds %d bytes", c.limits.MaxTotalBytes)
	}

	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("parse registry index: %w", err)
	}
	if idx.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("unsupported registry schema version %d (this kasas understands %d); upgrade kasas", idx.SchemaVersion, SupportedSchemaVersion)
	}
	return &idx, nil
}

// Find returns the entry for name, or false if the catalog has no such plugin.
func (idx *Index) Find(name string) (Entry, bool) {
	for _, e := range idx.Plugins {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Download fetches every file of entry from repository@ref, verifies each file's
// SHA-256 and the aggregate content hash against the index, and writes the verified
// bytes into destDir (which must already exist and should be empty). It never
// writes a file whose hash does not match, and it enforces the size/count limits.
//
// repository is the index's top-level `repository` field; raw file URLs are built
// as `<repository>/raw/<ref>/<entry.Path>/<file.Path>`, matching the registry's
// documented layout.
func (c *Client) Download(ctx context.Context, repository string, entry Entry, destDir string) error {
	if !nameRE.MatchString(entry.Name) {
		return fmt.Errorf("invalid plugin name %q", entry.Name)
	}
	if len(entry.Files) == 0 {
		return fmt.Errorf("plugin %q lists no files", entry.Name)
	}
	if len(entry.Files) > c.limits.MaxFiles {
		return fmt.Errorf("plugin %q lists %d files, over the limit of %d", entry.Name, len(entry.Files), c.limits.MaxFiles)
	}

	var total int64
	for _, f := range entry.Files {
		rel, err := safeRelPath(f.Path)
		if err != nil {
			return fmt.Errorf("plugin %q: %w", entry.Name, err)
		}

		url := c.rawURL(repository, entry.Path, f.Path)
		data, err := c.fetchFile(ctx, url)
		if err != nil {
			return fmt.Errorf("download %s: %w", f.Path, err)
		}
		if int64(len(data)) > c.limits.MaxFileBytes {
			return fmt.Errorf("file %s is %d bytes, over the per-file limit of %d", f.Path, len(data), c.limits.MaxFileBytes)
		}
		total += int64(len(data))
		if total > c.limits.MaxTotalBytes {
			return fmt.Errorf("plugin %q exceeds the total size limit of %d bytes", entry.Name, c.limits.MaxTotalBytes)
		}

		// Integrity: the downloaded bytes must match the hash the registry recorded
		// (and that a maintainer reviewed). A mismatch means the transport or the
		// source was tampered with — refuse it.
		sum := sha256.Sum256(data)
		got := hex.EncodeToString(sum[:])
		if !strings.EqualFold(got, f.SHA256) {
			return fmt.Errorf("integrity check failed for %s: expected %s, got %s", f.Path, f.SHA256, got)
		}

		dst := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}

	// Whole-plugin integrity: recompute the aggregate hash exactly as the registry
	// does and compare, so a dropped or added file is caught too.
	if want := strings.TrimSpace(entry.ContentHash); want != "" {
		if got := aggregateHash(entry.Files); got != want {
			return fmt.Errorf("plugin %q content hash mismatch: expected %s, got %s", entry.Name, want, got)
		}
	}
	return nil
}

func (c *Client) fetchFile(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, c.limits.MaxFileBytes+1))
}

// rawURL builds the raw download URL for a file, matching the registry's documented
// scheme: <repository>/raw/<ref>/<pluginPath>/<filePath>.
func (c *Client) rawURL(repository, pluginPath, filePath string) string {
	base := strings.TrimRight(repository, "/")
	return strings.Join([]string{base, "raw", c.ref, pluginPath, filePath}, "/")
}

// safeRelPath validates a registry-supplied file path and returns it cleaned. It
// rejects absolute paths and any traversal so a malicious index cannot write
// outside the staging directory.
func safeRelPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty file path")
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("absolute file path %q not allowed", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", fmt.Errorf("unsafe file path %q", p)
	}
	return filepath.FromSlash(clean), nil
}

// aggregateHash recomputes the registry's per-plugin content hash: the SHA-256 of
// "<path>\x00<filehash>\n" lines in path order, prefixed "sha256:".
func aggregateHash(files []FileRef) string {
	sorted := make([]FileRef, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%s\x00%s\n", f.Path, strings.ToLower(f.SHA256))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
