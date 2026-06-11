package plugins

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// This file implements ADR 0002: host-mediated plugin network access. A plugin
// never opens a socket — it calls kasas.fetch, and the HOST performs the request
// under rules the host owns, exactly as kasas.apply_labels routes a write through
// the capability-checked facade rather than letting the VM touch the database.
//
// The whole control lives in netGate.do + netGate.dialContext: egress is
// default-deny and allowlisted (only manifest-declared hosts), the SSRF rule
// refuses private/loopback/link-local/metadata targets unless the operator
// granted that specific host, DNS is pinned at request time so a rebind can't
// swap a permitted IP for an internal one, redirects are re-validated, and every
// attempt is logged, rate-limited, timed, and size-capped.

// FetchRequest is a plugin's outbound HTTP request (the argument to kasas.fetch).
// TimeoutMS may only SHORTEN the host's configured per-request timeout, never
// exceed it.
type FetchRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

// FetchResponse is the host's answer to kasas.fetch. Body is the response body up
// to the host's size cap; Truncated reports that the body was cut at the cap.
type FetchResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated,omitempty"`
}

// NetLimits bounds plugin egress. They are host-owned (config / settings), not
// plugin-declared, so a plugin can never raise its own ceiling.
type NetLimits struct {
	Timeout          time.Duration // per-request timeout (a request may only shorten it)
	MaxResponseBytes int64         // response body read cap
	RatePerMinute    int           // per-plugin request rate (<=0 disables limiting)
	MaxRedirects     int           // max redirect hops, each re-validated against the allowlist
}

// Default egress limits, applied when a knob is left at its zero value.
const (
	defaultNetTimeout          = 10 * time.Second
	defaultNetMaxResponseBytes = 5 << 20 // 5 MiB
	defaultNetRatePerMinute    = 60
	defaultNetMaxRedirects     = 5
)

func (l NetLimits) withDefaults() NetLimits {
	if l.Timeout <= 0 {
		l.Timeout = defaultNetTimeout
	}
	if l.MaxResponseBytes <= 0 {
		l.MaxResponseBytes = defaultNetMaxResponseBytes
	}
	if l.RatePerMinute == 0 {
		l.RatePerMinute = defaultNetRatePerMinute
	}
	if l.MaxRedirects <= 0 {
		l.MaxRedirects = defaultNetMaxRedirects
	}
	return l
}

var (
	// ErrNetUnconfigured is returned by Fetch when the plugin has the net:fetch
	// capability but no egress gate was built (no [net] block). Defensive: the
	// manifest validation makes the two come as a unit.
	ErrNetUnconfigured = errors.New("plugins: net:fetch is not configured for this plugin")
	// ErrNetDenied is returned for a request (or redirect) to a host the manifest's
	// [net].allow list does not declare. Egress is default-deny.
	ErrNetDenied = errors.New("plugins: net:fetch host is not in the manifest allowlist")
	// ErrNetBlocked is returned when a declared host resolves to a private,
	// loopback, link-local, or metadata address the operator did not grant. The
	// SSRF rule, tuned for self-hosting (ADR 0002 #3).
	ErrNetBlocked = errors.New("plugins: net:fetch blocked: host resolves to a private address the operator has not granted")
	// ErrNetRateLimited is returned when the plugin exceeds its per-minute request budget.
	ErrNetRateLimited = errors.New("plugins: net:fetch rate limit exceeded")
)

// allowedFetchMethods is the set of HTTP verbs a plugin may use. CONNECT/TRACE and
// anything exotic are refused.
var allowedFetchMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
	http.MethodOptions: true,
}

// EgressEntry is one recorded outbound attempt, for the operator-facing egress log.
type EgressEntry struct {
	Time       time.Time `json:"time"`
	Plugin     string    `json:"plugin"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	URL        string    `json:"url"`
	Status     int       `json:"status"`
	Bytes      int64     `json:"bytes"`
	DurationMs int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

// egressRecorder receives every egress attempt. The manager's bounded ring buffer
// implements it; nil disables recording (used in narrow unit tests).
type egressRecorder interface {
	record(EgressEntry)
}

// netGate is one plugin's egress chokepoint, bound to its declared allowlist and
// the operator's per-host private grants. The manager builds one per loaded
// net:fetch plugin and hands it to the host facade.
type netGate struct {
	plugin string
	allow  map[string]bool // declared hosts (lowercased), the egress allowlist
	grants map[string]bool // operator-granted private hosts (lowercased)
	limits NetLimits
	rec    egressRecorder
	logger *slog.Logger

	// resolve and dial are injectable so tests can drive the SSRF rule
	// deterministically (map a host to a chosen IP, connect to a test server)
	// without real DNS or a real private network. Production uses the defaults set
	// in newNetGate.
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)

	client *http.Client
	rl     *rateLimiter
}

// newNetGate builds a gate for plugin name with its declared allow hosts and the
// operator's private-host grants. The returned gate owns an http.Client whose
// transport validates and pins every connection.
func newNetGate(name string, allow, grants []string, limits NetLimits, rec egressRecorder, logger *slog.Logger) *netGate {
	if logger == nil {
		logger = slog.Default()
	}
	g := &netGate{
		plugin: name,
		allow:  toLowerSet(allow),
		grants: toLowerSet(grants),
		limits: limits.withDefaults(),
		rec:    rec,
		logger: logger,
		rl:     newRateLimiter(limits.withDefaults().RatePerMinute),
	}
	g.resolve = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}
	g.dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second}
		return d.DialContext(ctx, network, addr)
	}
	g.client = &http.Client{
		// No proxy: a configured proxy could bypass the validating dialer entirely.
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           g.dialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
			ForceAttemptHTTP2:     true,
			DisableKeepAlives:     true, // low volume; avoids cross-host connection reuse
			MaxIdleConns:          0,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		CheckRedirect: g.checkRedirect,
	}
	return g
}

// do performs one plugin fetch under the full egress policy. It is the single
// place the net:fetch capability turns into bytes on a wire.
func (g *netGate) do(ctx context.Context, req FetchRequest) (FetchResponse, error) {
	start := time.Now()
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	u, perr := url.Parse(strings.TrimSpace(req.URL))
	host := ""
	if u != nil {
		host = strings.ToLower(u.Hostname())
	}
	fail := func(err error) (FetchResponse, error) {
		g.log(EgressEntry{Method: method, Host: host, URL: req.URL, DurationMs: msSince(start), Error: err.Error()})
		return FetchResponse{}, err
	}

	if perr != nil || u == nil {
		return fail(fmt.Errorf("net:fetch: invalid url %q", req.URL))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fail(fmt.Errorf("net:fetch: scheme %q not allowed (http/https only)", u.Scheme))
	}
	if !allowedFetchMethods[method] {
		return fail(fmt.Errorf("net:fetch: method %q not allowed", method))
	}
	if host == "" {
		return fail(fmt.Errorf("net:fetch: url %q has no host", req.URL))
	}
	// Allowlist check up front, so an undeclared host is a clean, specific error
	// before any DNS or connection (the dialer re-checks too, defense in depth).
	if !g.allow[host] {
		return fail(fmt.Errorf("%w: %s", ErrNetDenied, host))
	}
	if g.rl != nil && !g.rl.allow() {
		return fail(ErrNetRateLimited)
	}

	timeout := g.limits.Timeout
	if req.TimeoutMS > 0 {
		if rt := time.Duration(req.TimeoutMS) * time.Millisecond; rt < timeout {
			timeout = rt // a request may shorten, never exceed, the host cap
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(cctx, method, u.String(), body)
	if err != nil {
		return fail(fmt.Errorf("net:fetch: %w", err))
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	if hreq.Header.Get("User-Agent") == "" {
		hreq.Header.Set("User-Agent", "kasas-plugin/"+g.plugin)
	}

	resp, err := g.client.Do(hreq)
	if err != nil {
		return fail(fmt.Errorf("net:fetch: %w", unwrapURLError(err)))
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, g.limits.MaxResponseBytes+1)
	data, rerr := io.ReadAll(limited)
	if rerr != nil {
		return fail(fmt.Errorf("net:fetch: read response: %w", rerr))
	}
	truncated := int64(len(data)) > g.limits.MaxResponseBytes
	if truncated {
		data = data[:g.limits.MaxResponseBytes]
	}

	out := FetchResponse{
		Status:    resp.StatusCode,
		Headers:   flattenHeaders(resp.Header),
		Body:      string(data),
		Truncated: truncated,
	}
	g.log(EgressEntry{
		Method: method, Host: host, URL: u.String(),
		Status: resp.StatusCode, Bytes: int64(len(data)), DurationMs: msSince(start),
	})
	return out, nil
}

// dialContext is the connection-level chokepoint: every connection the client
// makes (the initial request AND each redirect hop) passes through here. It
// re-checks the allowlist, resolves the host, validates the RESOLVED IP against
// the SSRF rule, and dials that exact IP — so the address that was validated is
// the address that is connected (DNS rebinding cannot swap it).
func (g *netGate) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	lhost := strings.ToLower(host)
	if !g.allow[lhost] {
		return nil, fmt.Errorf("%w: %s", ErrNetDenied, lhost)
	}
	ips, err := g.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("net:fetch: resolve %s: %w", host, err)
	}
	var lastErr error
	for _, ip := range ips {
		if !g.ipAllowed(lhost, ip) {
			lastErr = fmt.Errorf("%w: %s -> %s", ErrNetBlocked, lhost, ip)
			continue
		}
		conn, derr := g.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if derr != nil {
			lastErr = derr
			continue
		}
		return conn, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("net:fetch: no usable address for %s", host)
	}
	return nil, lastErr
}

// ipAllowed applies the SSRF rule to one resolved address: a public address is
// fine (the host is already on the allowlist), a private/loopback/link-local/
// metadata address is refused unless the operator granted this specific host.
func (g *netGate) ipAllowed(host string, ip net.IP) bool {
	if isPublicIP(ip) {
		return true
	}
	return g.grants[host]
}

// checkRedirect bounds the redirect chain and re-validates each hop's host against
// the allowlist (the resolved IP is re-validated independently in dialContext), so
// a permitted host cannot 302 a plugin onto an undeclared or internal one.
func (g *netGate) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= g.limits.MaxRedirects {
		return fmt.Errorf("net:fetch: too many redirects (max %d)", g.limits.MaxRedirects)
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fmt.Errorf("net:fetch: redirect to disallowed scheme %q", req.URL.Scheme)
	}
	host := strings.ToLower(req.URL.Hostname())
	if !g.allow[host] {
		return fmt.Errorf("%w (redirect): %s", ErrNetDenied, host)
	}
	return nil
}

// log records and structured-logs one egress attempt. A nil recorder still logs.
func (g *netGate) log(e EgressEntry) {
	e.Time = time.Now()
	e.Plugin = g.plugin
	if g.rec != nil {
		g.rec.record(e)
	}
	outcome := "ok"
	if e.Error != "" {
		outcome = "error"
	}
	pluginEgress.WithLabelValues(g.plugin, outcome).Inc()
	args := []any{"plugin", g.plugin, "method", e.Method, "host", e.Host, "status", e.Status, "bytes", e.Bytes, "ms", e.DurationMs}
	if e.Error != "" {
		g.logger.Warn("plugin egress denied/failed", append(args, "error", e.Error)...)
		return
	}
	g.logger.Info("plugin egress", args...)
}

// --- helpers ---

// isPublicIP reports whether ip is a globally routable address. Everything else —
// RFC 1918, loopback, link-local (incl. the 169.254.169.254 metadata address),
// ULA, multicast/unspecified, and CGNAT 100.64/10 (where Tailscale et al. live) —
// is treated as private and needs a per-host operator grant.
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsPrivate() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false // 100.64.0.0/10 carrier-grade NAT (not globally routable)
	}
	return true
}

func toLowerSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out[s] = true
		}
	}
	return out
}

// flattenHeaders renders a response's headers as a flat string map, joining a
// multi-valued header with ", " (so a plugin sees a simple object).
func flattenHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[k] = strings.Join(vs, ", ")
	}
	return out
}

// unwrapURLError strips the *url.Error wrapper http.Client puts around transport
// errors, so a denied/blocked dial surfaces as ErrNetDenied/ErrNetBlocked (which
// errors.Is can match) rather than a generic "Get ...: ..." string.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func msSince(t time.Time) int64 { return time.Since(t).Milliseconds() }

// rateLimiter is a tiny per-plugin token bucket: capacity = RatePerMinute (the
// burst), refilled at RatePerMinute/60 tokens per second. A nil limiter is
// unlimited.
type rateLimiter struct {
	mu       sync.Mutex
	capacity float64
	tokens   float64
	refill   float64 // tokens per second
	last     time.Time
}

func newRateLimiter(perMinute int) *rateLimiter {
	if perMinute <= 0 {
		return nil
	}
	return &rateLimiter{
		capacity: float64(perMinute),
		tokens:   float64(perMinute),
		refill:   float64(perMinute) / 60,
		last:     time.Now(),
	}
}

func (r *rateLimiter) allow() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.tokens = min(r.capacity, r.tokens+now.Sub(r.last).Seconds()*r.refill)
	r.last = now
	if r.tokens >= 1 {
		r.tokens--
		return true
	}
	return false
}

// --- egress log (manager-owned, in-memory ring) ---

// egressLog is the operator-facing record of recent plugin egress. It is an
// in-memory bounded ring, not a table: egress is high-volume observability (like
// the structured log), and a per-request DB row would add schema weight for data
// that is only ever read as "the last N requests". It survives as long as the
// process, which is the right lifetime for an activity log.
type egressLog struct {
	mu      sync.Mutex
	entries []EgressEntry
	max     int
}

func newEgressLog(max int) *egressLog {
	if max <= 0 {
		max = defaultEgressLogSize
	}
	return &egressLog{max: max}
}

const defaultEgressLogSize = 512

func (e *egressLog) record(entry EgressEntry) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	if len(e.entries) > e.max {
		// Drop the oldest, keeping the most recent e.max.
		e.entries = e.entries[len(e.entries)-e.max:]
	}
}

// list returns the most recent entries for plugin (newest first), up to limit.
// An empty plugin returns entries across all plugins.
func (e *egressLog) list(plugin string, limit int) []EgressEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	if limit <= 0 || limit > e.max {
		limit = e.max
	}
	out := make([]EgressEntry, 0, limit)
	for i := len(e.entries) - 1; i >= 0 && len(out) < limit; i-- {
		if plugin == "" || e.entries[i].Plugin == plugin {
			out = append(out, e.entries[i])
		}
	}
	return out
}
