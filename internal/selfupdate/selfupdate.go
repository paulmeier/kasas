// Package selfupdate checks GitHub Releases for a newer kasas build and,
// optionally, replaces the running binary in place.
//
// It is intentionally dependency-free: release discovery is a single GitHub
// API call, integrity is verified against the published SHA-256 checksum, and
// the swap is an atomic rename on the same filesystem. Only the linux/darwin
// targets that the release pipeline publishes are supported.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxBinarySize caps how many bytes we extract from a release tarball, a guard
// against a malicious or corrupt archive. The kasas binary is ~25 MB.
const maxBinarySize = 512 << 20 // 512 MiB

// apiBaseURL is the GitHub REST API root. It is a var so tests can point it at
// a local server.
var apiBaseURL = "https://api.github.com"

// Options configures a release check.
type Options struct {
	// Repo is the "owner/name" GitHub repository to query.
	Repo string
	// CurrentVersion is the running binary's version (used as the User-Agent
	// and reported back to callers).
	CurrentVersion string
	// HTTPClient is optional; a sane default is used when nil.
	HTTPClient *http.Client
}

// Release is the subset of a GitHub release we care about.
type Release struct {
	Version    string  `json:"tag_name"`
	URL        string  `json:"html_url"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// CheckLatest fetches the latest published (non-draft, non-prerelease) release
// for the configured repository. The caller decides whether it is newer via
// Release.IsNewerThan.
func CheckLatest(ctx context.Context, opts Options) (*Release, error) {
	if !strings.Contains(opts.Repo, "/") {
		return nil, fmt.Errorf("invalid repository %q (want owner/name)", opts.Repo)
	}
	url := apiBaseURL + "/repos/" + opts.Repo + "/releases/latest"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "kasas/"+opts.CurrentVersion)

	resp, err := httpClient(opts.HTTPClient).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github releases API: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.Version == "" {
		return nil, fmt.Errorf("release has no tag_name")
	}
	return &rel, nil
}

// IsNewerThan reports whether the release is a strictly higher semantic version
// than current. A current version that is not a parseable release (e.g. "dev"
// or a bare commit hash) yields false, so development builds are never nagged.
func (r *Release) IsNewerThan(current string) bool {
	return isNewer(r.Version, current)
}

// Apply downloads the release asset for the running platform, verifies it
// against the published SHA-256 checksum, and atomically replaces the target
// binary. Dest defaults to the resolved path of the running executable.
func Apply(ctx context.Context, rel *Release, opts ApplyOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	dest := opts.Dest
	if dest == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate running binary: %w", err)
		}
		if dest, err = filepath.EvalSymlinks(exe); err != nil {
			return fmt.Errorf("resolve binary path: %w", err)
		}
	}

	bin, sum, err := rel.assetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if sum.URL == "" {
		return fmt.Errorf("no checksum (.sha256) asset for %s; refusing to update unverified", bin.Name)
	}
	for _, u := range []string{bin.URL, sum.URL} {
		if !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("refusing non-HTTPS asset URL: %s", u)
		}
	}

	client := httpClient(opts.HTTPClient)

	// Download the tarball to a temp file, hashing it as it streams.
	tarball, gotSum, err := download(ctx, client, bin.URL)
	if err != nil {
		return fmt.Errorf("download %s: %w", bin.Name, err)
	}
	defer func() { _ = os.Remove(tarball) }()

	wantSum, err := fetchChecksum(ctx, client, sum.URL)
	if err != nil {
		return fmt.Errorf("download checksum: %w", err)
	}
	if gotSum != wantSum {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", bin.Name, gotSum, wantSum)
	}
	logger.Debug("release checksum verified", "asset", bin.Name, "sha256", gotSum)

	// Extract the kasas binary next to the destination so the final rename is
	// an atomic same-filesystem operation.
	newPath, err := extractBinary(tarball, dest)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(newPath) }() // no-op once renamed

	if err := os.Rename(newPath, dest); err != nil {
		return fmt.Errorf("replace %s: %w (need write access to %s)", dest, err, filepath.Dir(dest))
	}
	logger.Info("binary replaced", "path", dest, "version", rel.Version)
	return nil
}

// ApplyOptions configures Apply.
type ApplyOptions struct {
	// Dest is the binary path to replace; defaults to the running executable.
	Dest string
	// HTTPClient is optional; a sane default is used when nil.
	HTTPClient *http.Client
	// Logger is optional; slog.Default() is used when nil.
	Logger *slog.Logger
}

// assetFor selects the tarball and matching checksum asset for a platform.
func (r *Release) assetFor(goos, goarch string) (bin, sum asset, err error) {
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	for _, a := range r.Assets {
		switch {
		case strings.HasSuffix(a.Name, suffix+".sha256"):
			sum = a
		case strings.HasSuffix(a.Name, suffix):
			bin = a
		}
	}
	if bin.URL == "" {
		return asset{}, asset{}, fmt.Errorf("no release asset for %s/%s in %s", goos, goarch, r.Version)
	}
	return bin, sum, nil
}

// download streams url to a temp file, returning the file path and the hex
// SHA-256 of its contents. The caller is responsible for removing the file.
func download(ctx context.Context, client *http.Client, url string) (path, sum string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("unexpected status %s", resp.Status)
	}

	f, err := os.CreateTemp("", "kasas-update-*.tar.gz")
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBinarySize)); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", "", err
	}
	return f.Name(), hex.EncodeToString(h.Sum(nil)), nil
}

// fetchChecksum downloads a `sha256sum`-format file and returns the hex digest.
func fetchChecksum(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(body))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty checksum file")
	}
	return fields[0], nil
}

// extractBinary pulls the `kasas` executable out of a release tarball, writing
// it to a temp file in the same directory as dest (so the eventual rename is
// atomic). It returns the temp file path.
func extractBinary(tarball, dest string) (string, error) {
	f, err := os.Open(tarball)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()

	mode := os.FileMode(0o755)
	if fi, err := os.Stat(dest); err == nil {
		mode = fi.Mode().Perm() | 0o100 // keep existing perms, ensure +x for owner
	}

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "kasas" {
			continue
		}

		out, err := os.CreateTemp(filepath.Dir(dest), ".kasas-update-*")
		if err != nil {
			return "", fmt.Errorf("create temp binary in %s: %w", filepath.Dir(dest), err)
		}
		if _, err := io.Copy(out, io.LimitReader(tr, maxBinarySize)); err != nil {
			_ = out.Close()
			_ = os.Remove(out.Name())
			return "", fmt.Errorf("extract binary: %w", err)
		}
		if err := out.Chmod(mode); err != nil {
			_ = out.Close()
			_ = os.Remove(out.Name())
			return "", err
		}
		if err := out.Close(); err != nil {
			_ = os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	return "", fmt.Errorf("release tarball did not contain a kasas binary")
}

// Status summarizes a release check. It is JSON-serialisable for the dashboard
// update banner (GET /api/v1/update).
type Status struct {
	Current   string    `json:"current"`
	Latest    string    `json:"latest"`
	Available bool      `json:"update_available"`
	URL       string    `json:"release_url"`
	CheckedAt time.Time `json:"checked_at"`
}

// Checker periodically checks for a newer release, caches the result so cheap
// reads (a dashboard page-load) don't hit GitHub every time, logs a notice when
// the build falls behind, and provides the release for an apply. Safe for
// concurrent use.
type Checker struct {
	opts Options
	ttl  time.Duration

	mu      sync.RWMutex
	status  Status
	release *Release
	checked bool
}

// NewChecker constructs a Checker. Status reads are served from cache for up to
// the TTL (6h) before refreshing.
func NewChecker(opts Options) *Checker {
	return &Checker{
		opts:   opts,
		ttl:    6 * time.Hour,
		status: Status{Current: opts.CurrentVersion},
	}
}

// Refresh queries GitHub and updates the cache. Non-release builds short-circuit
// to "no update" without a network call. On error the previous cache is kept.
func (c *Checker) Refresh(ctx context.Context) (Status, error) {
	if !IsRelease(c.opts.CurrentVersion) {
		st := Status{Current: c.opts.CurrentVersion, CheckedAt: time.Now()}
		c.set(st, nil)
		return st, nil
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rel, err := CheckLatest(cctx, c.opts)
	if err != nil {
		return c.Cached(), err
	}

	st := Status{
		Current:   c.opts.CurrentVersion,
		Latest:    rel.Version,
		Available: rel.IsNewerThan(c.opts.CurrentVersion),
		URL:       rel.URL,
		CheckedAt: time.Now(),
	}
	c.set(st, rel)
	return st, nil
}

// Status returns the cached status, refreshing first if the cache is empty or
// older than the TTL.
func (c *Checker) Status(ctx context.Context) Status {
	c.mu.RLock()
	st, checked := c.status, c.checked
	c.mu.RUnlock()
	if checked && time.Since(st.CheckedAt) < c.ttl {
		return st
	}
	if fresh, err := c.Refresh(ctx); err == nil {
		return fresh
	}
	return st
}

// Cached returns the last known status without a network call.
func (c *Checker) Cached() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// LatestRelease refreshes and returns the latest release, so an apply acts on
// current data.
func (c *Checker) LatestRelease(ctx context.Context) (*Release, error) {
	if _, err := c.Refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.release == nil {
		return nil, fmt.Errorf("no release information available")
	}
	return c.release, nil
}

func (c *Checker) set(st Status, rel *Release) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = st
	if rel != nil {
		c.release = rel
	}
	c.checked = true
}

// Run refreshes on an interval and logs a notice whenever a newer release is
// available; it never self-modifies. Returns when ctx is cancelled. Non-release
// builds are skipped so local builds are not nagged.
func (c *Checker) Run(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	if !IsRelease(c.opts.CurrentVersion) {
		logger.Debug("update check skipped for non-release build", "version", c.opts.CurrentVersion)
		return
	}
	timer := time.NewTimer(15 * time.Second) // first check shortly after startup
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			st, err := c.Refresh(ctx)
			switch {
			case err != nil:
				logger.Debug("update check failed", "error", err)
			case st.Available:
				logger.Warn("a newer version of kasas is available",
					"current", st.Current, "latest", st.Latest, "url", st.URL,
					"hint", "run `kasas self-update` or use the dashboard to upgrade")
			}
			timer.Reset(interval)
		}
	}
}

// IsRelease reports whether v parses as a semantic version, i.e. it is a real
// release build rather than "dev" or a bare commit description.
func IsRelease(v string) bool {
	_, ok := parseSemver(v)
	return ok
}

type semver struct{ major, minor, patch int }

func isNewer(latest, current string) bool {
	l, ok1 := parseSemver(latest)
	c, ok2 := parseSemver(current)
	if !ok1 || !ok2 {
		return false
	}
	switch {
	case l.major != c.major:
		return l.major > c.major
	case l.minor != c.minor:
		return l.minor > c.minor
	default:
		return l.patch > c.patch
	}
}

// parseSemver extracts major.minor.patch from a version string, tolerating a
// leading "v" and any pre-release/build/git-describe suffix (e.g.
// "v0.1.0-2-gabc123" parses as 0.1.0).
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	var err error
	if v.major, err = strconv.Atoi(parts[0]); err != nil {
		return semver{}, false
	}
	if v.minor, err = strconv.Atoi(parts[1]); err != nil {
		return semver{}, false
	}
	if v.patch, err = strconv.Atoi(parts[2]); err != nil {
		return semver{}, false
	}
	return v, true
}
