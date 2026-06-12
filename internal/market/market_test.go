package market

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeProvider is a controllable market.Provider for testing the cache logic.
type fakeProvider struct {
	mu         sync.Mutex
	fetches    int
	configured bool
	points     []Point
	err        error
	block      chan struct{} // when non-nil, Fetch blocks until it is closed
}

func (f *fakeProvider) Name() string                             { return "fake" }
func (f *fakeProvider) Egress() []string                         { return []string{"fake.example"} }
func (f *fakeProvider) Configured(context.Context) (bool, error) { return f.configured, nil }
func (f *fakeProvider) SetKey(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = key != ""
	return nil
}

func (f *fakeProvider) Fetch(_ context.Context, _ SeriesSpec, _ time.Time) ([]Point, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.fetches++
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.points, nil
}

func (f *fakeProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func spyPoints() []Point {
	return []Point{{Date: "2024-01-02", Value: "101.00"}, {Date: "2024-01-01", Value: "100.00"}}
}

func spySpec() SeriesSpec {
	return SeriesSpec{ID: "spy", Symbol: "SPY", Kind: KindEquity, Currency: "USD"}
}

func newTestService(t *testing.T, fp *fakeProvider, opts Options) *Service {
	t.Helper()
	if opts.Store == nil {
		opts.Store = testutil.NewStore(t)
	}
	opts.Provider = fp
	if opts.Logger == nil {
		opts.Logger = discardLogger()
	}
	if opts.Rate == 0 {
		opts.Rate = time.Nanosecond // don't rate-limit tests
	}
	return NewService(opts)
}

func TestServiceColdThenCacheHit(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Specs: []SeriesSpec{spySpec()}})
	ctx := context.Background()

	res, err := svc.Points(ctx, "spy", "", "")
	require.NoError(t, err)
	require.Len(t, res.Points, 2)
	assert.Equal(t, 1, fp.count(), "cold read fetches once")
	assert.Equal(t, "2024-01-01", res.Points[0].Date, "points returned ascending by date")
	assert.Equal(t, "2024-01-02", res.AsOf)
	assert.True(t, res.Fresh)

	// A second read while fresh is a pure cache hit: no second provider call.
	res2, err := svc.Points(ctx, "spy", "", "")
	require.NoError(t, err)
	require.Len(t, res2.Points, 2)
	assert.Equal(t, 1, fp.count(), "fresh read does not refetch")
}

func TestServiceWindowFilter(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Specs: []SeriesSpec{spySpec()}})
	ctx := context.Background()

	_, err := svc.Points(ctx, "spy", "", "")
	require.NoError(t, err)

	res, err := svc.Points(ctx, "spy", "2024-01-02", "")
	require.NoError(t, err)
	require.Len(t, res.Points, 1)
	assert.Equal(t, "2024-01-02", res.Points[0].Date)
}

func TestServiceUnknownSeries(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{Specs: []SeriesSpec{spySpec()}})
	_, err := svc.Points(context.Background(), "nope", "", "")
	assert.ErrorIs(t, err, ErrUnknownSeries)
}

func TestServiceNotConfigured(t *testing.T) {
	fp := &fakeProvider{configured: false, points: spyPoints()}
	svc := newTestService(t, fp, Options{Specs: []SeriesSpec{spySpec()}})
	_, err := svc.Points(context.Background(), "spy", "", "")
	assert.ErrorIs(t, err, ErrNotConfigured)
	assert.Equal(t, 0, fp.count(), "no provider call without a key")
}

func TestServiceSingleFlight(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints(), block: make(chan struct{})}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Specs: []SeriesSpec{spySpec()}})
	ctx := context.Background()

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = svc.Points(ctx, "spy", "", "")
		}()
	}
	// Let the goroutines collapse onto one in-flight fetch, then release it.
	time.Sleep(50 * time.Millisecond)
	close(fp.block)
	wg.Wait()

	assert.Equal(t, 1, fp.count(), "concurrent cold reads collapse to one provider call")
}

func TestServiceEmitsMarketUpdated(t *testing.T) {
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	emitter := events.NewEmitter(bus)
	ch, unsub := bus.Subscribe()
	t.Cleanup(unsub)

	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Emitter: emitter, Specs: []SeriesSpec{spySpec()}})

	_, err := svc.Points(context.Background(), "spy", "", "")
	require.NoError(t, err)

	select {
	case ev := <-ch:
		assert.Equal(t, events.TypeMarketUpdated, ev.Type)
		assert.Equal(t, "spy", ev.EntityID)
		assert.Equal(t, events.EntityMarketSeries, ev.EntityType)
	case <-time.After(2 * time.Second):
		t.Fatal("no market.updated event published")
	}
}

func TestServiceStaleRefresh(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	// Seed a stale cached point (fetched two days ago) directly.
	require.NoError(t, store.UpsertMarketSeries(ctx, db.UpsertMarketSeriesParams{
		ID: "spy", Provider: "fake", Symbol: "SPY", Kind: "equity", Currency: "USD", Meta: "{}",
	}))
	require.NoError(t, store.UpsertMarketPoint(ctx, db.UpsertMarketPointParams{
		SeriesID: "spy", Date: "2024-01-01", Value: "100.00",
		FetchedAt: time.Now().Add(-48 * time.Hour).Unix(),
	}))

	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{Store: store, TTL: 24 * time.Hour, Specs: []SeriesSpec{spySpec()}})

	// Stale read serves the cached point immediately (no synchronous fetch)...
	res, err := svc.Points(ctx, "spy", "", "")
	require.NoError(t, err)
	require.NotEmpty(t, res.Points)
	assert.False(t, res.Fresh, "served stale")

	// ...and refreshes in the background.
	require.Eventually(t, func() bool { return fp.count() >= 1 }, 2*time.Second, 10*time.Millisecond,
		"stale read triggers a background refresh")
}

func TestServiceWarm(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints()}
	specs := []SeriesSpec{
		{ID: "spy", Symbol: "SPY", Kind: KindEquity, Currency: "USD"},
		{ID: "agg", Symbol: "AGG", Kind: KindEquity, Currency: "USD"},
	}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Specs: specs})

	require.NoError(t, svc.Warm(context.Background()))
	assert.Equal(t, 2, fp.count(), "warm fetches each cold series once")

	// A second warm is a no-op: both series are now fresh.
	require.NoError(t, svc.Warm(context.Background()))
	assert.Equal(t, 2, fp.count(), "warm skips fresh series")
}

func TestServiceResetAndList(t *testing.T) {
	fp := &fakeProvider{configured: true, points: spyPoints()}
	svc := newTestService(t, fp, Options{TTL: time.Hour, Specs: []SeriesSpec{spySpec()}})
	ctx := context.Background()

	_, err := svc.Points(ctx, "spy", "", "")
	require.NoError(t, err)

	list, err := svc.ListSeries(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, 2, list[0].Points)
	assert.True(t, list[0].Fresh)

	require.NoError(t, svc.Reset(ctx))
	list, err = svc.ListSeries(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1, "definition survives the cache reset")
	assert.Equal(t, 0, list[0].Points, "cache wiped")
	assert.Empty(t, list[0].AsOf)
}

func TestServiceProviderError(t *testing.T) {
	fp := &fakeProvider{configured: true, err: errors.New("bad symbol")}
	svc := newTestService(t, fp, Options{Specs: []SeriesSpec{spySpec()}})
	_, err := svc.Points(context.Background(), "spy", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bad symbol")
}
