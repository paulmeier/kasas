package market

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// Sentinel errors the API layer maps to clean status codes.
var (
	ErrUnknownSeries = errors.New("unknown market series")
	ErrNotConfigured = errors.New("market provider is not configured (set the provider API key)")
	ErrDuplicateID   = errors.New("a series with that id already exists")
)

const (
	defaultTTL     = 24 * time.Hour   // a daily series is stale once a newer close should exist
	defaultTimeout = 20 * time.Second // bound on one refresh; comfortably above the rate spacing so a refresh never times out waiting for a token alone
	defaultRate    = 12 * time.Second // min spacing between provider calls (~5/min)
)

// Service is the read-through cache for external market data. It owns single-flight
// per series, per-provider rate limiting, TTL freshness, stale-while-revalidate,
// and market.updated emission, over the market_* tables. It holds the authoritative
// in-memory list of configured series specs (persisted elsewhere as the
// market.series setting) so add/remove takes effect immediately.
type Service struct {
	store    db.Store
	provider Provider
	emitter  *events.Emitter // nil-safe: Record still runs the write tx, just emits nothing
	ttl      time.Duration
	timeout  time.Duration
	logger   *slog.Logger
	limiter  *rate.Limiter
	group    singleflight.Group

	mu         sync.RWMutex
	specs      []SeriesSpec
	refreshing map[string]struct{} // series ids with a background refresh in flight (dedupes goroutine spawn)
}

// Options configures a Service.
type Options struct {
	Store    db.Store
	Provider Provider
	Emitter  *events.Emitter
	TTL      time.Duration
	Timeout  time.Duration
	Logger   *slog.Logger
	Specs    []SeriesSpec
	// Rate is the minimum spacing between provider calls; defaults to ~5/min.
	Rate time.Duration
}

// NewService constructs a Service, applying defaults for any unset option.
func NewService(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	spacing := opts.Rate
	if spacing <= 0 {
		spacing = defaultRate
	}
	return &Service{
		store:      opts.Store,
		provider:   opts.Provider,
		emitter:    opts.Emitter,
		ttl:        ttl,
		timeout:    timeout,
		logger:     logger,
		limiter:    rate.NewLimiter(rate.Every(spacing), 1),
		specs:      opts.Specs,
		refreshing: map[string]struct{}{},
	}
}

// ProviderName returns the active provider's name.
func (s *Service) ProviderName() string { return s.provider.Name() }

// Egress returns the provider's external hosts, for the source descriptor.
func (s *Service) Egress() []string { return s.provider.Egress() }

// Configured reports whether the provider has a usable API key.
func (s *Service) Configured(ctx context.Context) (bool, error) { return s.provider.Configured(ctx) }

// SetKey stores the provider's API key (the source credential).
func (s *Service) SetKey(ctx context.Context, key string) error { return s.provider.SetKey(ctx, key) }

// Specs returns a copy of the configured series specs.
func (s *Service) Specs() []SeriesSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SeriesSpec, len(s.specs))
	copy(out, s.specs)
	return out
}

// SetSpecs replaces the configured series list (after the API layer has persisted
// the new list to the market.series setting), taking effect immediately.
func (s *Service) SetSpecs(specs []SeriesSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.specs = make([]SeriesSpec, len(specs))
	copy(s.specs, specs)
}

func (s *Service) specByID(id string) (SeriesSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sp := range s.specs {
		if sp.ID == id {
			return sp, true
		}
	}
	return SeriesSpec{}, false
}

// ListSeries returns every configured series joined with its cache freshness.
func (s *Service) ListSeries(ctx context.Context) ([]Series, error) {
	specs := s.Specs()
	out := make([]Series, 0, len(specs))
	cutoff := time.Now().Unix() - int64(s.ttl.Seconds())
	for _, spec := range specs {
		ser := Series{SeriesSpec: spec, Provider: s.provider.Name()}
		latest, err := s.store.LatestMarketPoint(ctx, spec.ID)
		switch {
		case err == nil:
			ser.AsOf = latest.Date
			ser.FetchedAt = latest.FetchedAt
			ser.Fresh = latest.FetchedAt >= cutoff
			if n, cerr := s.store.CountMarketPoints(ctx, spec.ID); cerr == nil {
				ser.Points = int(n)
			}
		case errors.Is(err, sql.ErrNoRows):
			// cold: never fetched, leave zero values
		default:
			return nil, err
		}
		out = append(out, ser)
	}
	return out, nil
}

// PointsResult is a series' cached points plus freshness metadata for the read API.
type PointsResult struct {
	Provider string
	Points   []Point
	AsOf     string
	Fresh    bool
}

// Points serves a series' daily closes through the read-through cache: a cold
// series is fetched synchronously under a timeout; a stale series is served from
// cache and refreshed in the background (stale-while-revalidate); a fresh series is
// a straight cache hit. since/until bound the returned window (empty = unbounded).
func (s *Service) Points(ctx context.Context, id, since, until string) (PointsResult, error) {
	spec, ok := s.specByID(id)
	if !ok {
		return PointsResult{}, ErrUnknownSeries
	}

	latest, err := s.store.LatestMarketPoint(ctx, id)
	cold := errors.Is(err, sql.ErrNoRows)
	if err != nil && !cold {
		return PointsResult{}, err
	}
	cutoff := time.Now().Unix() - int64(s.ttl.Seconds())

	servedStale := false
	switch {
	case cold:
		// Cold cache: one synchronous fetch (single-flighted, on its own bounded
		// context so a slow or cancelled caller doesn't abort the shared refresh),
		// then serve.
		if rerr := s.refresh(spec); rerr != nil {
			return PointsResult{}, rerr
		}
	case latest.FetchedAt < cutoff:
		// Stale cache: serve immediately, refresh in the background (SWR).
		s.backgroundRefresh(spec)
		servedStale = true
	}

	pts, err := s.readPoints(ctx, id, since, until)
	if err != nil {
		return PointsResult{}, err
	}
	res := PointsResult{Provider: s.provider.Name(), Points: pts}
	if l, err := s.store.LatestMarketPoint(ctx, id); err == nil {
		res.AsOf = l.Date
		// A stale read reports Fresh=false deterministically: it served stale data and
		// kicked a background refresh, so re-deriving freshness from a re-read here would
		// race that refresh — which may already have upserted a fresh point (the source of
		// the flaky TestServiceStaleRefresh). The next read sees the refreshed point.
		res.Fresh = !servedStale && l.FetchedAt >= cutoff
	}
	return res, nil
}

// Warm refreshes every configured series whose cache is cold or stale. It is the
// optional "warm the cache" path driven by the generic per-source sync; the
// read-through path is primary, so a fresh cache makes this a no-op.
func (s *Service) Warm(ctx context.Context) error {
	specs := s.Specs()
	if len(specs) == 0 {
		return nil
	}
	configured, err := s.provider.Configured(ctx)
	if err != nil {
		return err
	}
	if !configured {
		return ErrNotConfigured
	}
	cutoff := time.Now().Unix() - int64(s.ttl.Seconds())
	var errs []error
	for _, spec := range specs {
		latest, lerr := s.store.LatestMarketPoint(ctx, spec.ID)
		cold := errors.Is(lerr, sql.ErrNoRows)
		if lerr != nil && !cold {
			errs = append(errs, fmt.Errorf("series %s: %w", spec.ID, lerr))
			continue
		}
		if !cold && latest.FetchedAt >= cutoff {
			continue // fresh
		}
		if rerr := s.refresh(spec); rerr != nil {
			errs = append(errs, fmt.Errorf("series %s: %w", spec.ID, rerr))
		}
	}
	return errors.Join(errs...)
}

// readPoints reads a series' cached points within the optional date window.
func (s *Service) readPoints(ctx context.Context, id, since, until string) ([]Point, error) {
	rows, err := s.store.ListMarketPoints(ctx, db.ListMarketPointsParams{
		SeriesID: id,
		Since:    strings.TrimSpace(since),
		Until:    strings.TrimSpace(until),
	})
	if err != nil {
		return nil, err
	}
	pts := make([]Point, len(rows))
	for i, r := range rows {
		pts[i] = Point{Date: r.Date, Value: r.Value}
	}
	return pts, nil
}

// refresh fetches a series from the provider and upserts its points, collapsing
// concurrent refreshes of the same series into one provider call (single-flight).
// The provider call runs on its OWN bounded context derived from
// context.Background(), not any caller's context: the shared single-flight result
// must not be aborted because the first caller (a request that timed out, or a
// disconnected client) went away — the cache fills for everyone else regardless.
func (s *Service) refresh(spec SeriesSpec) error {
	_, err, _ := s.group.Do(spec.ID, func() (any, error) {
		ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
		defer cancel()
		return nil, s.doRefresh(ctx, spec)
	})
	return err
}

// backgroundRefresh kicks a single-flighted refresh in a goroutine so a stale read
// returns immediately while the cache is updated behind it. It dedupes per series:
// repeated stale reads of the same series while a refresh is in flight do not each
// spawn a goroutine (single-flight already collapses the provider call, but this
// also bounds goroutine creation under rapid reads).
func (s *Service) backgroundRefresh(spec SeriesSpec) {
	s.mu.Lock()
	if _, busy := s.refreshing[spec.ID]; busy {
		s.mu.Unlock()
		return
	}
	s.refreshing[spec.ID] = struct{}{}
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.refreshing, spec.ID)
			s.mu.Unlock()
		}()
		if err := s.refresh(spec); err != nil {
			s.logger.Warn("market: background refresh failed", "series", spec.ID, "error", err)
		}
	}()
}

// doRefresh performs the actual provider fetch + cache upsert + market.updated
// emission within one transaction (the emit is a no-op when events are disabled).
func (s *Service) doRefresh(ctx context.Context, spec SeriesSpec) error {
	configured, err := s.provider.Configured(ctx)
	if err != nil {
		return err
	}
	if !configured {
		return ErrNotConfigured
	}
	if err := s.limiter.Wait(ctx); err != nil { // per-provider rate limit, at the server
		return err
	}
	pts, err := s.provider.Fetch(ctx, spec, time.Time{})
	if err != nil {
		return err
	}
	if len(pts) == 0 {
		return nil // nothing to write; not an error
	}
	now := time.Now().Unix()
	asOf := newestDate(pts)
	provider := s.provider.Name()

	return s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		if err := q.UpsertMarketSeries(ctx, db.UpsertMarketSeriesParams{
			ID:       spec.ID,
			Provider: provider,
			Symbol:   spec.Symbol,
			Kind:     string(spec.Kind),
			Currency: spec.Currency,
			Adjusted: boolToInt(spec.Adjusted),
			Meta:     seriesMeta(spec),
		}); err != nil {
			return fmt.Errorf("upsert series %q: %w", spec.ID, err)
		}
		for _, p := range pts {
			if err := q.UpsertMarketPoint(ctx, db.UpsertMarketPointParams{
				SeriesID:  spec.ID,
				Date:      p.Date,
				Value:     p.Value,
				FetchedAt: now,
			}); err != nil {
				return fmt.Errorf("upsert point %s/%s: %w", spec.ID, p.Date, err)
			}
		}
		return rec.Emit(ctx, q, events.TypeMarketUpdated, events.EntityMarketSeries, spec.ID, events.MarketUpdatedPayload{
			SeriesID: spec.ID,
			Provider: provider,
			AsOf:     asOf,
			Points:   len(pts),
		})
	})
}

// DeleteCached removes a series' cache rows (its spec snapshot and all points). The
// definition lives in the market.series setting, so this only clears the cache —
// used when a series is removed, and as the building block of "kasas market reset".
func (s *Service) DeleteCached(ctx context.Context, id string) error {
	if err := s.store.DeleteMarketSeriesPoints(ctx, id); err != nil {
		return err
	}
	_, err := s.store.DeleteMarketSeries(ctx, id)
	return err
}

// Reset wipes the entire market cache (every series and point). Definitions survive
// in the market.series setting and rebuild on the next read. Powers "kasas market reset".
func (s *Service) Reset(ctx context.Context) error {
	if err := s.store.TruncateMarketPoints(ctx); err != nil {
		return err
	}
	return s.store.TruncateMarketSeries(ctx)
}

// newestDate returns the lexically-greatest date in pts (ISO dates sort lexically).
func newestDate(pts []Point) string {
	newest := ""
	for _, p := range pts {
		if p.Date > newest {
			newest = p.Date
		}
	}
	return newest
}

// seriesMeta renders a series' meta JSON (currently just the display name).
func seriesMeta(spec SeriesSpec) string {
	m := map[string]string{}
	if spec.Name != "" {
		m["name"] = spec.Name
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
