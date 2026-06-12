// Package market implements external market/reference data as a first-class,
// provider-agnostic read-through cache (ADR 0006). It owns fetching daily
// time series (benchmark indices, fund NAVs, FX, crypto) from a market provider
// on demand, caching them with a TTL in the market_* tables, and serving them
// through the read-tier API — never copied wholesale on a schedule.
//
// The design has two layers. The [Service] is the cache: it owns single-flight
// per series, per-provider rate limiting, TTL freshness, stale-while-revalidate,
// and market.updated emission. A small [Provider] interface abstracts the upstream
// data source so the first concrete pick (Alpha Vantage) is not load-bearing — a
// provider migration is a remap, not a data loss, because series carry stable
// internal ids with the provider symbol recorded alongside.
//
// A thin reference-archetype [Source] adapter (source.go) makes the market source
// a first-class citizen of the ingestion machinery — listed in /api/v1/sources,
// with a runtime-settable provider API key and a generic "warm the cache" sync —
// without ever touching the ledger's accounts or transactions.
package market

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/paulmeier/kasas/internal/vault"
)

// Kind classifies what a series tracks. It selects the provider's data path
// (e.g. an equity daily series vs an FX daily series) and is recorded on the
// series so the dashboard can label it honestly.
type Kind string

const (
	KindEquity Kind = "equity"
	KindFund   Kind = "fund"
	KindIndex  Kind = "index"
	KindFX     Kind = "fx"
	KindCrypto Kind = "crypto"
)

// ValidKind reports whether k is a recognized series kind.
func ValidKind(k Kind) bool {
	switch k {
	case KindEquity, KindFund, KindIndex, KindFX, KindCrypto:
		return true
	default:
		return false
	}
}

// SeriesSpec is one configured series: a stable internal id plus what to fetch.
// Specs are the authoritative definition (persisted as the market.series setting);
// the market_series table is a cache snapshot of them keyed by the same id.
type SeriesSpec struct {
	// ID is the stable internal id (e.g. "spy", "sp500tr"). It is how clients and
	// the cache reference the series and survives a provider migration.
	ID string `json:"id"`
	// Symbol is the provider-native symbol (e.g. "SPY", or "EUR/USD" for FX,
	// "BTC" for crypto).
	Symbol string `json:"symbol"`
	// Kind selects the provider data path and labels the series.
	Kind Kind `json:"kind"`
	// Currency is the ISO code the values are quoted in (e.g. "USD"). For crypto it
	// is the quote market.
	Currency string `json:"currency"`
	// Adjusted requests a total-return / split-adjusted series where the provider
	// supports it (often a premium feature).
	Adjusted bool `json:"adjusted,omitempty"`
	// Name is an optional human-readable display name, stored in the series meta.
	Name string `json:"name,omitempty"`
}

// Point is one daily close: an ISO-8601 date and a decimal-string value (a price
// is money per unit, so it gets the same no-float discipline as ledger money).
type Point struct {
	Date  string `json:"date"`
	Value string `json:"value"`
}

// Series is a configured spec joined with its cache freshness, returned by the
// list endpoint so a client can show "as of yesterday's close" honestly.
type Series struct {
	SeriesSpec
	Provider  string `json:"provider"`
	AsOf      string `json:"as_of,omitempty"`      // newest cached date, "" if never fetched
	Points    int    `json:"points"`               // cached point count
	FetchedAt int64  `json:"fetched_at,omitempty"` // unix seconds of the last refresh
	Fresh     bool   `json:"fresh"`                // cache is within the TTL
}

// Provider is the provider-agnostic upstream. A concrete provider (e.g. Alpha
// Vantage) talks to one external API and shapes its data into neutral [Point]s.
// It never touches the database; the [Service] owns caching and persistence.
type Provider interface {
	// Name is the stable provider identifier (e.g. "alphavantage"), recorded on
	// each cached series.
	Name() string
	// Egress lists the external hostnames the provider contacts, surfaced to the
	// operator so the source's network reach is visible, never silent.
	Egress() []string
	// Fetch returns the daily-close points for a series (the recent window the free
	// tier gives, at least covering `since`). It must be idempotent.
	Fetch(ctx context.Context, spec SeriesSpec, since time.Time) ([]Point, error)
	// Configured reports whether the provider has a usable API key stored.
	Configured(ctx context.Context) (bool, error)
	// SetKey stores the provider's API key for future fetches, no restart required.
	SetKey(ctx context.Context, key string) error
}

// ProviderEnv carries the shared infrastructure a provider factory needs.
type ProviderEnv struct {
	Logger  *slog.Logger
	Secrets vault.SecretStore
	// BaseURL overrides the provider's API base URL (for self-hosting proxies and
	// tests). Empty uses the provider default.
	BaseURL string
	// Client is an optional HTTP client (tests inject one). Empty uses a default.
	Client *http.Client
}

// ProviderFactory constructs a provider from an env. Each provider registers one
// from its package init, mirroring the ingestion source registry.
type ProviderFactory func(ProviderEnv) (Provider, error)

var (
	providerMu       sync.RWMutex
	providerRegistry = map[string]ProviderFactory{}
)

// RegisterProvider adds a provider factory to the registry. Providers call this
// from an init() so importing the package makes the provider available. It panics
// on an empty name, a nil factory, or a duplicate registration.
func RegisterProvider(name string, f ProviderFactory) {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("market: RegisterProvider with empty name")
	}
	if f == nil {
		panic("market: RegisterProvider with nil factory for " + name)
	}
	providerMu.Lock()
	defer providerMu.Unlock()
	if _, dup := providerRegistry[name]; dup {
		panic("market: duplicate RegisterProvider for " + name)
	}
	providerRegistry[name] = f
}

// NewProvider constructs a registered provider by name.
func NewProvider(name string, env ProviderEnv) (Provider, error) {
	providerMu.RLock()
	f, ok := providerRegistry[strings.TrimSpace(name)]
	providerMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("market: unknown provider %q", name)
	}
	if env.Logger == nil {
		env.Logger = slog.Default()
	}
	return f(env)
}

// Providers returns the names of every registered provider, sorted, for listing
// available choices.
func Providers() []string {
	providerMu.RLock()
	defer providerMu.RUnlock()
	out := make([]string, 0, len(providerRegistry))
	for name := range providerRegistry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
