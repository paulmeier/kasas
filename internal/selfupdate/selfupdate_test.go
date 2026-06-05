package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in   string
		want semver
		ok   bool
	}{
		{"v0.1.0", semver{0, 1, 0}, true},
		{"0.2.3", semver{0, 2, 3}, true},
		{"v1.2.3-rc1", semver{1, 2, 3}, true},
		{"v0.1.0-2-gabc123", semver{0, 1, 0}, true}, // git describe
		{"  v10.20.30  ", semver{10, 20, 30}, true},
		{"dev", semver{}, false},
		{"abc123", semver{}, false},
		{"v1.2", semver{}, false},
		{"", semver{}, false},
	}
	for _, c := range cases {
		got, ok := parseSemver(c.in)
		assert.Equal(t, c.ok, ok, "ok for %q", c.in)
		if c.ok {
			assert.Equal(t, c.want, got, "value for %q", c.in)
		}
	}
}

func TestIsNewerAndIsRelease(t *testing.T) {
	assert.True(t, isNewer("v0.2.0", "v0.1.0"))
	assert.True(t, isNewer("v1.0.0", "v0.9.9"))
	assert.True(t, isNewer("v0.1.1", "v0.1.0"))
	assert.False(t, isNewer("v0.1.0", "v0.1.0"))
	assert.False(t, isNewer("v0.1.0", "v0.2.0"))
	// Non-release current versions are never "behind", so dev builds aren't nagged.
	assert.False(t, isNewer("v9.9.9", "dev"))
	assert.False(t, isNewer("v9.9.9", "abc123"))
	// A dev build two commits past the latest tag is not "behind" that tag.
	assert.False(t, isNewer("v0.1.0", "v0.1.0-2-gabc123"))

	assert.True(t, IsRelease("v1.2.3"))
	assert.False(t, IsRelease("dev"))
}

func TestAssetFor(t *testing.T) {
	suffix := fmt.Sprintf("_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	rel := &Release{
		Version: "v1.0.0",
		Assets: []asset{
			{Name: "kasas_v1.0.0_other_arch.tar.gz", URL: "https://x/other"},
			{Name: "kasas_v1.0.0" + suffix, URL: "https://x/bin"},
			{Name: "kasas_v1.0.0" + suffix + ".sha256", URL: "https://x/sum"},
		},
	}
	bin, sum, err := rel.assetFor(runtime.GOOS, runtime.GOARCH)
	require.NoError(t, err)
	assert.Equal(t, "https://x/bin", bin.URL)
	assert.Equal(t, "https://x/sum", sum.URL)

	_, _, err = rel.assetFor("plan9", "sparc")
	require.Error(t, err)
}

func TestCheckLatest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","html_url":"https://example.test/r","assets":[]}`))
	})
	mux.HandleFunc("/repos/owner/missing/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	old := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = old }()

	rel, err := CheckLatest(context.Background(), Options{Repo: "owner/repo", HTTPClient: srv.Client()})
	require.NoError(t, err)
	assert.Equal(t, "v1.2.3", rel.Version)
	assert.Equal(t, "https://example.test/r", rel.URL)
	assert.True(t, rel.IsNewerThan("v1.0.0"))
	assert.False(t, rel.IsNewerThan("v1.2.3"))

	_, err = CheckLatest(context.Background(), Options{Repo: "owner/missing", HTTPClient: srv.Client()})
	require.Error(t, err)

	_, err = CheckLatest(context.Background(), Options{Repo: "no-slash"})
	require.ErrorContains(t, err, "owner/name")
}

func TestChecker(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0","html_url":"https://example.test/r2","assets":[]}`))
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	old := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = old }()

	t.Run("refresh detects newer and caches", func(t *testing.T) {
		atomic.StoreInt32(&hits, 0)
		c := NewChecker(Options{Repo: "owner/repo", CurrentVersion: "v1.0.0", HTTPClient: srv.Client()})

		st, err := c.Refresh(context.Background())
		require.NoError(t, err)
		assert.True(t, st.Available)
		assert.Equal(t, "v2.0.0", st.Latest)
		assert.Equal(t, "https://example.test/r2", st.URL)
		assert.Equal(t, int32(1), atomic.LoadInt32(&hits))

		// Status within the TTL is served from cache (no extra GitHub call).
		st2 := c.Status(context.Background())
		assert.True(t, st2.Available)
		assert.Equal(t, int32(1), atomic.LoadInt32(&hits))

		rel, err := c.LatestRelease(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "v2.0.0", rel.Version)
	})

	t.Run("dev build skips the network", func(t *testing.T) {
		atomic.StoreInt32(&hits, 0)
		c := NewChecker(Options{Repo: "owner/repo", CurrentVersion: "dev", HTTPClient: srv.Client()})
		st, err := c.Refresh(context.Background())
		require.NoError(t, err)
		assert.False(t, st.Available)
		assert.Equal(t, int32(0), atomic.LoadInt32(&hits), "dev build must not call GitHub")
	})
}

// makeTarball builds a release-style .tar.gz containing the kasas binary
// alongside a sibling file (to confirm extraction picks the right entry).
func makeTarball(t *testing.T, version, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := fmt.Sprintf("kasas_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
	for _, f := range []struct{ name, body string }{
		{dir + "/README.md", "readme"},
		{dir + "/kasas", content},
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(f.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

func TestApply(t *testing.T) {
	const version = "v9.9.9"
	const content = "new-kasas-binary-bytes"
	tarball := makeTarball(t, version, content)
	digest := sha256.Sum256(tarball)
	sumHex := hex.EncodeToString(digest[:])
	assetName := fmt.Sprintf("kasas_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tarball) })
	mux.HandleFunc("/sum", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", sumHex, assetName)
	})
	mux.HandleFunc("/badsum", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Repeat("0", 64), assetName)
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()

	newDest := func(t *testing.T) string {
		t.Helper()
		dest := filepath.Join(t.TempDir(), "kasas")
		require.NoError(t, os.WriteFile(dest, []byte("old-binary"), 0o755))
		return dest
	}

	t.Run("happy path replaces the binary", func(t *testing.T) {
		dest := newDest(t)
		rel := &Release{Version: version, Assets: []asset{
			{Name: assetName, URL: srv.URL + "/bin"},
			{Name: assetName + ".sha256", URL: srv.URL + "/sum"},
		}}
		require.NoError(t, Apply(context.Background(), rel, ApplyOptions{Dest: dest, HTTPClient: srv.Client()}))

		got, err := os.ReadFile(dest)
		require.NoError(t, err)
		assert.Equal(t, content, string(got))

		fi, err := os.Stat(dest)
		require.NoError(t, err)
		assert.NotZero(t, fi.Mode()&0o100, "updated binary should stay executable")

		// No stray temp files left behind in the install dir.
		entries, err := os.ReadDir(filepath.Dir(dest))
		require.NoError(t, err)
		assert.Len(t, entries, 1)
	})

	t.Run("checksum mismatch aborts", func(t *testing.T) {
		dest := newDest(t)
		rel := &Release{Version: version, Assets: []asset{
			{Name: assetName, URL: srv.URL + "/bin"},
			{Name: assetName + ".sha256", URL: srv.URL + "/badsum"},
		}}
		err := Apply(context.Background(), rel, ApplyOptions{Dest: dest, HTTPClient: srv.Client()})
		require.ErrorContains(t, err, "checksum mismatch")

		got, _ := os.ReadFile(dest)
		assert.Equal(t, "old-binary", string(got), "binary must be untouched on failure")
	})

	t.Run("missing checksum asset refuses", func(t *testing.T) {
		dest := newDest(t)
		rel := &Release{Version: version, Assets: []asset{
			{Name: assetName, URL: srv.URL + "/bin"},
		}}
		err := Apply(context.Background(), rel, ApplyOptions{Dest: dest, HTTPClient: srv.Client()})
		require.ErrorContains(t, err, "checksum")
	})

	t.Run("non-https asset refused", func(t *testing.T) {
		dest := newDest(t)
		rel := &Release{Version: version, Assets: []asset{
			{Name: assetName, URL: "http://insecure/bin"},
			{Name: assetName + ".sha256", URL: "http://insecure/sum"},
		}}
		err := Apply(context.Background(), rel, ApplyOptions{Dest: dest, HTTPClient: srv.Client()})
		require.ErrorContains(t, err, "HTTPS")
	})
}
