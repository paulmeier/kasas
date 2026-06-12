package market

import (
	"context"

	"github.com/paulmeier/kasas/internal/source"
)

// SourceType is the ingestion-source type the market data source registers under
// and the credential scope for its provider API key.
const SourceType = "market"

// Source adapts the market [Service] to the ingestion source contract so external
// market data is a first-class source: listed in /api/v1/sources, with a
// runtime-settable provider API key and the generic "warm the cache" sync. It is a
// reference-archetype source — it implements [source.Warmer], not [source.Puller],
// so it never produces ledger accounts or transactions.
//
// Unlike the pull sources it is not in the global source registry (it needs the
// constructed Service, not just an Env), so it is wired directly into the engine
// at startup and is always active.
type Source struct {
	svc *Service
}

// NewSource wraps a Service as an ingestion source.
func NewSource(svc *Service) *Source { return &Source{svc: svc} }

// Descriptor implements source.Source. Egress is taken from the active provider so
// the source's network reach (the provider's host) is visible on the Sources page.
func (s *Source) Descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypeReference,
		Title:     "Market data",
		Egress:    s.svc.Egress(),
		Credentials: []source.CredentialField{
			{
				Key:   "api_key",
				Title: "Provider API key",
				Help:  "API key for the configured market-data provider (e.g. a free Alpha Vantage key from https://www.alphavantage.co/support/#api-key). Stored on the server; required to fetch series.",
			},
		},
	}
}

// Warm implements source.Warmer: refresh the configured series whose cache is cold
// or stale. Driven by the scheduler (only when an interval is configured) and by
// POST /api/v1/sources/market/sync.
func (s *Source) Warm(ctx context.Context) error { return s.svc.Warm(ctx) }

// CredentialConfigured implements source.Credentialed: the provider API key is set.
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	return s.svc.Configured(ctx)
}

// SetCredential implements source.Credentialed by storing the provider API key.
func (s *Source) SetCredential(ctx context.Context, input string) error {
	return s.svc.SetKey(ctx, input)
}

// Compile-time checks that Source satisfies the engine's contracts.
var (
	_ source.Source       = (*Source)(nil)
	_ source.Warmer       = (*Source)(nil)
	_ source.Credentialed = (*Source)(nil)
)
