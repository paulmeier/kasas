// Package alphavantage implements a market-data [market.Provider] backed by Alpha
// Vantage (https://www.alphavantage.co). It fetches daily-close time series for
// equities/ETFs/funds, FX pairs, and digital currencies, and shapes them into the
// neutral decimal-string points the market cache stores.
//
// Alpha Vantage's free tier is ~25 requests/day, which is viable only behind the
// market cache's daily TTL. Errors and rate-limit notices arrive as HTTP 200 with
// a "Note" / "Information" / "Error Message" key instead of a time series, so the
// parser treats a missing time-series object as an error and surfaces that message.
package alphavantage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/market"
	"github.com/paulmeier/kasas/internal/vault"
)

// ProviderName is the stable identifier this provider registers under and stamps on
// every series it caches.
const ProviderName = "alphavantage"

// apiKeyName is the secret-store key holding the runtime-set Alpha Vantage API key.
const apiKeyName = "market_alphavantage_api_key"

// host is the single external host this provider contacts (surfaced as egress).
const host = "www.alphavantage.co"

// defaultBaseURL is the Alpha Vantage query endpoint. Overridable for tests/proxies.
const defaultBaseURL = "https://www.alphavantage.co/query"

// register makes the provider available when this package is imported.
func init() {
	market.RegisterProvider(ProviderName, func(env market.ProviderEnv) (market.Provider, error) {
		return New(env), nil
	})
}

// Provider is the Alpha Vantage market-data provider.
type Provider struct {
	secrets vault.SecretStore
	baseURL string
	client  *http.Client
}

// New constructs the provider from an env, applying defaults for the base URL and
// HTTP client.
func New(env market.ProviderEnv) *Provider {
	baseURL := strings.TrimSpace(env.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	client := env.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Provider{secrets: env.Secrets, baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

// Name implements market.Provider.
func (p *Provider) Name() string { return ProviderName }

// Egress implements market.Provider: the one host this provider contacts.
func (p *Provider) Egress() []string { return []string{host} }

// Configured reports whether an API key is stored.
func (p *Provider) Configured(ctx context.Context) (bool, error) {
	key, err := p.apiKey(ctx)
	if err != nil {
		return false, err
	}
	return key != "", nil
}

// SetKey stores the Alpha Vantage API key.
func (p *Provider) SetKey(ctx context.Context, key string) error {
	if p.secrets == nil {
		return errors.New("no secret store configured to hold the market API key")
	}
	return p.secrets.SetSecretValue(ctx, apiKeyName, strings.TrimSpace(key))
}

func (p *Provider) apiKey(ctx context.Context) (string, error) {
	if p.secrets == nil {
		return "", nil
	}
	v, err := p.secrets.SecretValue(ctx, apiKeyName)
	return strings.TrimSpace(v), err
}

// Fetch implements market.Provider. It maps the series kind to an Alpha Vantage
// function, requests the daily series, and returns its closes as decimal-string
// points. since is advisory: the free tier returns a fixed recent window, which the
// cache accumulates over time.
func (p *Provider) Fetch(ctx context.Context, spec market.SeriesSpec, _ time.Time) ([]market.Point, error) {
	key, err := p.apiKey(ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("alpha vantage API key required")
	}

	q, tsKey, closeKeys, err := requestFor(spec)
	if err != nil {
		return nil, err
	}
	q.Set("apikey", key)

	body, err := p.get(ctx, q)
	if err != nil {
		return nil, err
	}
	return parseSeries(body, tsKey, closeKeys)
}

// requestFor builds the query parameters for a spec and returns the response's
// time-series object key plus the candidate close-field keys to read from each day.
func requestFor(spec market.SeriesSpec) (url.Values, string, []string, error) {
	q := url.Values{}
	switch spec.Kind {
	case market.KindEquity, market.KindFund, market.KindIndex:
		if spec.Adjusted {
			q.Set("function", "TIME_SERIES_DAILY_ADJUSTED")
		} else {
			q.Set("function", "TIME_SERIES_DAILY")
		}
		q.Set("symbol", spec.Symbol)
		closeKeys := []string{"4. close"}
		if spec.Adjusted {
			closeKeys = []string{"5. adjusted close", "4. close"}
		}
		return q, "Time Series (Daily)", closeKeys, nil
	case market.KindFX:
		from, to, ok := splitPair(spec.Symbol)
		if !ok {
			return nil, "", nil, fmt.Errorf("fx series %q needs a FROM/TO symbol like EUR/USD", spec.Symbol)
		}
		q.Set("function", "FX_DAILY")
		q.Set("from_symbol", from)
		q.Set("to_symbol", to)
		return q, "Time Series FX (Daily)", []string{"4. close"}, nil
	case market.KindCrypto:
		q.Set("function", "DIGITAL_CURRENCY_DAILY")
		q.Set("symbol", spec.Symbol)
		quote := spec.Currency
		if quote == "" {
			quote = "USD"
		}
		q.Set("market", quote)
		// Alpha Vantage has used two shapes for this series over time; accept the
		// modern simple keys first, then the older market-suffixed ones.
		return q, "Time Series (Digital Currency Daily)", []string{
			"4. close",
			"4a. close (" + quote + ")",
			"4b. close (USD)",
		}, nil
	default:
		return nil, "", nil, fmt.Errorf("unsupported series kind %q", spec.Kind)
	}
}

// get issues the GET request and returns the response body, mapping non-200s to
// errors. The API key travels as a query parameter (Alpha Vantage has no header
// auth), so the request URL contains it; errors from net/http (e.g. a *url.Error)
// embed that URL, so every error out of here is scrubbed of the key before it can
// reach a log or an API response.
func (p *Provider) get(ctx context.Context, q url.Values) ([]byte, error) {
	key := q.Get("apikey")
	reqURL := p.baseURL + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, scrubKey(err, key)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, scrubKey(err, key)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // cap the read at 8 MiB
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("alpha vantage returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// parseSeries extracts daily-close points from an Alpha Vantage response. A missing
// time-series object means the body is an error/rate-limit notice (Alpha Vantage
// returns those as HTTP 200), which is surfaced as an error.
func parseSeries(body []byte, tsKey string, closeKeys []string) ([]market.Point, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode alpha vantage response: %w", err)
	}
	tsRaw, ok := raw[tsKey]
	if !ok {
		return nil, providerMessage(raw)
	}
	var series map[string]map[string]string
	if err := json.Unmarshal(tsRaw, &series); err != nil {
		return nil, fmt.Errorf("decode %q: %w", tsKey, err)
	}
	points := make([]market.Point, 0, len(series))
	for date, fields := range series {
		value, ok := firstField(fields, closeKeys)
		if !ok {
			continue
		}
		points = append(points, market.Point{Date: date, Value: strings.TrimSpace(value)})
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("alpha vantage %q had no readable closes", tsKey)
	}
	return points, nil
}

// providerMessage turns an error/rate-limit response into an error using whichever
// status key Alpha Vantage populated.
func providerMessage(raw map[string]json.RawMessage) error {
	for _, key := range []string{"Error Message", "Note", "Information"} {
		if v, ok := raw[key]; ok {
			var msg string
			if err := json.Unmarshal(v, &msg); err == nil && msg != "" {
				return fmt.Errorf("alpha vantage: %s", msg)
			}
		}
	}
	return errors.New("alpha vantage returned no time series")
}

// firstField returns the first present field among keys (Alpha Vantage close fields
// vary by function and have changed format over time).
func firstField(fields map[string]string, keys []string) (string, bool) {
	for _, k := range keys {
		if v, ok := fields[k]; ok {
			return v, true
		}
	}
	return "", false
}

// scrubKey redacts the API key from an error's message (net/http errors embed the
// request URL, which carries the key as a query parameter). It returns a plain
// error rather than wrapping, so the secret cannot be recovered via Unwrap either.
func scrubKey(err error, key string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "REDACTED")
	}
	return errors.New(msg)
}

// splitPair splits an FX symbol like "EUR/USD" (or "EURUSD") into its two legs.
func splitPair(symbol string) (from, to string, ok bool) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if i := strings.IndexAny(symbol, "/-_"); i > 0 {
		from, to = strings.TrimSpace(symbol[:i]), strings.TrimSpace(symbol[i+1:])
		return from, to, from != "" && to != ""
	}
	if len(symbol) == 6 {
		return symbol[:3], symbol[3:], true
	}
	return "", "", false
}

var _ market.Provider = (*Provider)(nil)
