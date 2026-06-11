package plugins

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestGate wires a gate to a test server: resolve maps a host to a chosen IP
// (so the SSRF rule can be exercised deterministically), while dial ignores the
// validated address and connects to srv — separating the egress POLICY (which IP,
// allowed or not) from the TRANSPORT (where bytes actually go), so a test can drive
// the rule without a real private network.
func newTestGate(srv *httptest.Server, allow, grants []string, limits NetLimits, ipFor map[string]net.IP) (*netGate, *egressLog) {
	log := newEgressLog(0)
	g := newNetGate("tester", allow, grants, limits, log, testLogger())
	g.resolve = func(_ context.Context, host string) ([]net.IP, error) {
		if ip, ok := ipFor[strings.ToLower(host)]; ok {
			return []net.IP{ip}, nil
		}
		return []net.IP{net.ParseIP("93.184.216.34")}, nil // a public default
	}
	srvAddr := srv.Listener.Addr().String()
	g.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, srvAddr)
	}
	return g, log
}

func TestNetFetchAllowlistedPublicHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "api.example.com", r.Host)
		assert.Equal(t, "1", r.Header.Get("X-Test"))
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	g, log := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{}, nil)
	resp, err := g.do(context.Background(), FetchRequest{
		URL: "http://api.example.com/x", Method: "GET", Headers: map[string]string{"X-Test": "1"},
	})
	require.NoError(t, err)
	assert.Equal(t, 200, resp.Status)
	assert.Equal(t, "hello", resp.Body)
	assert.False(t, resp.Truncated)
	require.Len(t, log.list("tester", 0), 1)
	assert.Equal(t, 200, log.list("tester", 0)[0].Status)
}

func TestNetFetchUndeclaredHostRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should never be reached for an undeclared host")
	}))
	defer srv.Close()

	g, log := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{}, nil)
	_, err := g.do(context.Background(), FetchRequest{URL: "http://evil.example.net/x"})
	assert.ErrorIs(t, err, ErrNetDenied)
	// The refusal is logged so the operator can see the attempt.
	require.Len(t, log.list("tester", 0), 1)
	assert.NotEmpty(t, log.list("tester", 0)[0].Error)
}

func TestNetFetchPrivateHostBlockedWithoutGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should never be reached for an ungranted private host")
	}))
	defer srv.Close()

	ipFor := map[string]net.IP{"paperless.lan": net.ParseIP("192.168.1.10")}
	g, _ := newTestGate(srv, []string{"paperless.lan"}, nil, NetLimits{}, ipFor)
	_, err := g.do(context.Background(), FetchRequest{URL: "http://paperless.lan/api"})
	assert.ErrorIs(t, err, ErrNetBlocked)
}

func TestNetFetchPrivateHostAllowedWithGrant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("doc"))
	}))
	defer srv.Close()

	ipFor := map[string]net.IP{"paperless.lan": net.ParseIP("192.168.1.10")}
	g, _ := newTestGate(srv, []string{"paperless.lan"}, []string{"paperless.lan"}, NetLimits{}, ipFor)
	resp, err := g.do(context.Background(), FetchRequest{URL: "http://paperless.lan/api"})
	require.NoError(t, err)
	assert.Equal(t, "doc", resp.Body)
}

func TestNetFetchMetadataAddressBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	// A declared host that resolves to the cloud metadata IP is still blocked
	// (link-local), unless the operator granted it — the classic SSRF target.
	ipFor := map[string]net.IP{"metadata.example.com": net.ParseIP("169.254.169.254")}
	g, _ := newTestGate(srv, []string{"metadata.example.com"}, nil, NetLimits{}, ipFor)
	_, err := g.do(context.Background(), FetchRequest{URL: "http://metadata.example.com/latest/meta-data/"})
	assert.ErrorIs(t, err, ErrNetBlocked)
}

func TestNetFetchRedirectToUndeclaredHostRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect onto a host the manifest never declared.
		http.Redirect(w, r, "http://evil.example.net/stolen", http.StatusFound)
	}))
	defer srv.Close()

	g, _ := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{}, nil)
	_, err := g.do(context.Background(), FetchRequest{URL: "http://api.example.com/start"})
	assert.ErrorIs(t, err, ErrNetDenied, "a 302 onto an undeclared host must be refused")
}

func TestNetFetchRedirectToDeclaredHostFollowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://b.example.com/dest", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("arrived"))
	}))
	defer srv.Close()

	g, _ := newTestGate(srv, []string{"a.example.com", "b.example.com"}, nil, NetLimits{}, nil)
	resp, err := g.do(context.Background(), FetchRequest{URL: "http://a.example.com/start"})
	require.NoError(t, err)
	assert.Equal(t, "arrived", resp.Body)
}

func TestNetFetchResponseSizeCapTruncates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()

	g, _ := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{MaxResponseBytes: 100}, nil)
	resp, err := g.do(context.Background(), FetchRequest{URL: "http://api.example.com/big"})
	require.NoError(t, err)
	assert.Len(t, resp.Body, 100)
	assert.True(t, resp.Truncated)
}

func TestNetFetchRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	g, _ := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{RatePerMinute: 2}, nil)
	req := FetchRequest{URL: "http://api.example.com/x"}
	_, err1 := g.do(context.Background(), req)
	_, err2 := g.do(context.Background(), req)
	_, err3 := g.do(context.Background(), req)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.ErrorIs(t, err3, ErrNetRateLimited, "the 3rd immediate request exceeds the 2/min budget")
}

func TestNetFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()

	g, _ := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{Timeout: 50 * time.Millisecond}, nil)
	_, err := g.do(context.Background(), FetchRequest{URL: "http://api.example.com/slow"})
	require.Error(t, err)
}

func TestNetFetchRejectsSchemeAndMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	g, _ := newTestGate(srv, []string{"api.example.com"}, nil, NetLimits{}, nil)

	_, err := g.do(context.Background(), FetchRequest{URL: "ftp://api.example.com/x"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")

	_, err = g.do(context.Background(), FetchRequest{URL: "http://api.example.com/x", Method: "CONNECT"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "method")
}

func TestIsPublicIP(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"}
	private := []string{
		"127.0.0.1", "::1", // loopback
		"10.0.0.1", "172.16.5.4", "192.168.1.1", // RFC1918
		"169.254.169.254", // link-local / cloud metadata
		"100.64.0.1",      // CGNAT (Tailscale)
		"fd00::1",         // ULA
		"0.0.0.0",         // unspecified
		"fe80::1",         // link-local v6
	}
	for _, s := range public {
		assert.Truef(t, isPublicIP(net.ParseIP(s)), "%s should be public", s)
	}
	for _, s := range private {
		assert.Falsef(t, isPublicIP(net.ParseIP(s)), "%s should be treated as private", s)
	}
}

func TestEgressLogRingAndFilter(t *testing.T) {
	log := newEgressLog(3)
	for i := 0; i < 5; i++ {
		log.record(EgressEntry{Plugin: "a", Host: "h", Status: i})
	}
	log.record(EgressEntry{Plugin: "b", Host: "h"})
	// Ring keeps only the last 3 across all plugins.
	all := log.list("", 0)
	require.Len(t, all, 3)
	assert.Equal(t, "b", all[0].Plugin, "newest first")
	// Filtering by plugin returns only that plugin's surviving entries.
	assert.Len(t, log.list("a", 0), 2)
	assert.Empty(t, log.list("c", 0))
}
