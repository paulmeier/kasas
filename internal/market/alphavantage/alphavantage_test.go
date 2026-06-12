package alphavantage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/market"
	"github.com/paulmeier/kasas/internal/vault"
)

// newTestProvider builds a provider pointed at a test server, with a key preset.
func newTestProvider(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, secrets.SetSecretValue(context.Background(), apiKeyName, "TESTKEY"))
	return New(market.ProviderEnv{Secrets: secrets, BaseURL: srv.URL, Client: srv.Client()})
}

func equitySpec() market.SeriesSpec {
	return market.SeriesSpec{ID: "ibm", Symbol: "IBM", Kind: market.KindEquity, Currency: "USD"}
}

const dailyBody = `{
  "Meta Data": {"2. Symbol": "IBM", "3. Last Refreshed": "2024-01-03"},
  "Time Series (Daily)": {
    "2024-01-03": {"1. open": "160.0", "2. high": "162.0", "3. low": "159.0", "4. close": "161.50", "5. volume": "1000"},
    "2024-01-02": {"1. open": "158.0", "2. high": "161.0", "3. low": "157.0", "4. close": "160.00", "5. volume": "900"}
  }
}`

func TestFetchDailyEquity(t *testing.T) {
	var gotFunc, gotSymbol string
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotFunc = r.URL.Query().Get("function")
		gotSymbol = r.URL.Query().Get("symbol")
		assert.Equal(t, "TESTKEY", r.URL.Query().Get("apikey"))
		_, _ = w.Write([]byte(dailyBody))
	})

	pts, err := p.Fetch(context.Background(), equitySpec(), time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "TIME_SERIES_DAILY", gotFunc)
	assert.Equal(t, "IBM", gotSymbol)
	require.Len(t, pts, 2)
	byDate := map[string]string{}
	for _, pt := range pts {
		byDate[pt.Date] = pt.Value
	}
	assert.Equal(t, "161.50", byDate["2024-01-03"])
	assert.Equal(t, "160.00", byDate["2024-01-02"])
}

func TestFetchAdjustedUsesAdjustedFunction(t *testing.T) {
	body := `{"Time Series (Daily)": {"2024-01-03": {"4. close": "161.50", "5. adjusted close": "150.25"}}}`
	var gotFunc string
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotFunc = r.URL.Query().Get("function")
		_, _ = w.Write([]byte(body))
	})
	spec := market.SeriesSpec{ID: "spy", Symbol: "SPY", Kind: market.KindEquity, Currency: "USD", Adjusted: true}
	pts, err := p.Fetch(context.Background(), spec, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "TIME_SERIES_DAILY_ADJUSTED", gotFunc)
	require.Len(t, pts, 1)
	assert.Equal(t, "150.25", pts[0].Value, "adjusted close preferred when present")
}

func TestFetchFXSplitsPair(t *testing.T) {
	body := `{"Time Series FX (Daily)": {"2024-01-03": {"1. open": "1.10", "4. close": "1.1050"}}}`
	var from, to string
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		from = r.URL.Query().Get("from_symbol")
		to = r.URL.Query().Get("to_symbol")
		assert.Equal(t, "FX_DAILY", r.URL.Query().Get("function"))
		_, _ = w.Write([]byte(body))
	})
	spec := market.SeriesSpec{ID: "eurusd", Symbol: "EUR/USD", Kind: market.KindFX, Currency: "USD"}
	pts, err := p.Fetch(context.Background(), spec, time.Time{})
	require.NoError(t, err)
	assert.Equal(t, "EUR", from)
	assert.Equal(t, "USD", to)
	require.Len(t, pts, 1)
	assert.Equal(t, "1.1050", pts[0].Value)
}

func TestFetchCryptoModernAndLegacyKeys(t *testing.T) {
	cryptoSpec := market.SeriesSpec{ID: "btc", Symbol: "BTC", Kind: market.KindCrypto, Currency: "USD"}

	modern := `{"Time Series (Digital Currency Daily)": {"2024-01-03": {"4. close": "42000.00"}}}`
	p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(modern)) })
	pts, err := p.Fetch(context.Background(), cryptoSpec, time.Time{})
	require.NoError(t, err)
	require.Len(t, pts, 1)
	assert.Equal(t, "42000.00", pts[0].Value)

	legacy := `{"Time Series (Digital Currency Daily)": {"2024-01-03": {"4a. close (USD)": "42100.00"}}}`
	p2 := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(legacy)) })
	pts2, err := p2.Fetch(context.Background(), cryptoSpec, time.Time{})
	require.NoError(t, err)
	require.Len(t, pts2, 1)
	assert.Equal(t, "42100.00", pts2[0].Value)
}

func TestFetchSurfacesRateLimitNote(t *testing.T) {
	// Alpha Vantage returns HTTP 200 with a Note/Information key when throttled.
	body := `{"Note": "Thank you for using Alpha Vantage! Our standard API rate limit is 25 requests per day."}`
	p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	_, err := p.Fetch(context.Background(), equitySpec(), time.Time{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
}

func TestFetchSurfacesErrorMessage(t *testing.T) {
	body := `{"Error Message": "Invalid API call. Please retry or visit the documentation for TIME_SERIES_DAILY."}`
	p := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) })
	spec := market.SeriesSpec{ID: "bad", Symbol: "NOPE", Kind: market.KindEquity, Currency: "USD"}
	_, err := p.Fetch(context.Background(), spec, time.Time{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid API call")
}

func TestFetchRequiresKey(t *testing.T) {
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	p := New(market.ProviderEnv{Secrets: secrets})
	_, err := p.Fetch(context.Background(), equitySpec(), time.Time{})
	require.Error(t, err)
}

func TestErrorsDoNotLeakKey(t *testing.T) {
	// A transport-level failure produces a *url.Error that embeds the request URL,
	// which carries the API key as a query parameter. The provider must scrub it.
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	require.NoError(t, secrets.SetSecretValue(context.Background(), apiKeyName, "SUPERSECRETKEY"))
	// Port 0 is unbindable, so client.Do fails with a dial error wrapping the URL.
	p := New(market.ProviderEnv{Secrets: secrets, BaseURL: "http://127.0.0.1:0/query"})
	_, err := p.Fetch(context.Background(), equitySpec(), time.Time{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "SUPERSECRETKEY", "the API key must never appear in an error")
}

func TestConfiguredReflectsKey(t *testing.T) {
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	p := New(market.ProviderEnv{Secrets: secrets})
	ok, err := p.Configured(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)

	require.NoError(t, p.SetKey(context.Background(), "ABC123"))
	ok, err = p.Configured(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestSplitPair(t *testing.T) {
	cases := map[string][2]string{
		"EUR/USD": {"EUR", "USD"},
		"eur-usd": {"EUR", "USD"},
		"EURUSD":  {"EUR", "USD"},
	}
	for in, want := range cases {
		from, to, ok := splitPair(in)
		require.True(t, ok, in)
		assert.Equal(t, want[0], from)
		assert.Equal(t, want[1], to)
	}
	if _, _, ok := splitPair("BTC"); ok {
		t.Fatal("a bare symbol is not a valid pair")
	}
}
