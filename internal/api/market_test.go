package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/market"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeMarketProvider is a market.Provider that returns fixed points, for testing
// the API layer without an upstream.
type fakeMarketProvider struct {
	points     []market.Point
	configured bool
}

func (f *fakeMarketProvider) Name() string                             { return "fake" }
func (f *fakeMarketProvider) Egress() []string                         { return []string{"fake.example"} }
func (f *fakeMarketProvider) Configured(context.Context) (bool, error) { return f.configured, nil }
func (f *fakeMarketProvider) SetKey(context.Context, string) error     { f.configured = true; return nil }
func (f *fakeMarketProvider) Fetch(context.Context, market.SeriesSpec, time.Time) ([]market.Point, error) {
	return f.points, nil
}

func newMarketServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	fp := &fakeMarketProvider{
		configured: true,
		points:     []market.Point{{Date: "2024-01-02", Value: "101.00"}, {Date: "2024-01-01", Value: "100.00"}},
	}
	svc := market.NewService(market.Options{
		Store:    store,
		Provider: fp,
		TTL:      time.Hour,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Rate:     time.Nanosecond,
	})
	s := api.New(api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Market:  svc,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv
}

func TestMarketSeriesLifecycle(t *testing.T) {
	srv := newMarketServer(t)

	// Initially no series, but the subsystem is enabled.
	var list struct {
		Enabled    bool                  `json:"enabled"`
		Configured bool                  `json:"configured"`
		Series     []api.MarketSeriesDTO `json:"series"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/market/series", &list))
	assert.True(t, list.Enabled)
	assert.True(t, list.Configured)
	assert.Empty(t, list.Series)

	// Define a series.
	var created api.MarketSeriesDTO
	code := postJSON(t, srv, "/api/v1/market/series",
		map[string]any{"id": "spy", "symbol": "SPY", "kind": "equity", "currency": "USD", "name": "S&P 500 ETF"}, &created)
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "spy", created.ID)
	assert.Equal(t, "fake", created.Provider)

	// It now lists.
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/market/series", &list))
	require.Len(t, list.Series, 1)
	assert.Equal(t, "SPY", list.Series[0].Symbol)

	// Read-through points: cold fetch returns the provider's points, ascending.
	var pts struct {
		Provider string               `json:"provider"`
		AsOf     string               `json:"as_of"`
		Fresh    bool                 `json:"fresh"`
		Points   []api.MarketPointDTO `json:"points"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/market/series/spy/points", &pts))
	require.Len(t, pts.Points, 2)
	assert.Equal(t, "2024-01-01", pts.Points[0].Date)
	assert.Equal(t, "2024-01-02", pts.AsOf)
	assert.True(t, pts.Fresh)

	// Duplicate id is a conflict.
	assert.Equal(t, http.StatusConflict, postJSON(t, srv, "/api/v1/market/series",
		map[string]any{"id": "spy", "symbol": "SPY"}, nil))

	// Invalid id is a 400.
	assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/market/series",
		map[string]any{"id": "Bad ID!", "symbol": "X"}, nil))

	// Remove it.
	var del struct {
		Deleted bool `json:"deleted"`
	}
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/market/series/spy", &del))
	assert.True(t, del.Deleted)

	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/market/series", &list))
	assert.Empty(t, list.Series)

	// Points for an unknown series is a 404.
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/market/series/spy/points", nil))
}

func TestMarketDisabled(t *testing.T) {
	// A server with no market service still answers cleanly (enabled:false), not 404.
	store := db.NewSQLiteStore(testutil.NewDB(t))
	s := api.New(api.Options{Store: store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Version: "test"})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	var list struct {
		Enabled bool `json:"enabled"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/market/series", &list))
	assert.False(t, list.Enabled)
}
