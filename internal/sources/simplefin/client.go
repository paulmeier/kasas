// Package simplefin implements the SimpleFIN bridge as a kasas ingestion source.
// It is a first-party [source.Puller]: it fetches accounts and transactions from
// a SimpleFIN bridge and returns them as a neutral source.ImportBatch for the
// ingestion engine to persist. See https://www.simplefin.org/protocol.html.
package simplefin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to a SimpleFIN bridge over HTTP.
type Client struct {
	httpClient *http.Client
}

// NewClient returns a client with a sane default timeout.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Claim exchanges a base64-encoded setup token for a long-lived access URL.
// The setup token decodes to a one-time claim URL that is POSTed to obtain the
// access URL (which embeds HTTP basic-auth credentials in its userinfo).
func (c *Client) Claim(ctx context.Context, setupToken string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(setupToken))
	if err != nil {
		return "", fmt.Errorf("decode setup token: %w", err)
	}
	claimURL := strings.TrimSpace(string(decoded))
	if claimURL == "" {
		return "", fmt.Errorf("setup token decoded to an empty claim URL")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claimURL, nil)
	if err != nil {
		return "", fmt.Errorf("build claim request: %w", err)
	}
	req.Header.Set("Content-Length", "0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("claim setup token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("read claim response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claim failed: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	accessURL := strings.TrimSpace(string(body))
	if accessURL == "" {
		return "", fmt.Errorf("claim returned an empty access URL")
	}
	return accessURL, nil
}

// maxLookback is the widest history a SimpleFIN bridge will serve. The protocol
// caps a requested range to 90 days and reports "Requested date range exceeds
// limit of 90 days and was capped." whenever a request crosses that line. We
// hold start-date a day inside the ceiling so clock skew and request latency
// between our clock and the bridge's can't tip a full-90-day lookback past 90
// days, which would surface that warning on every sync. The bridge drops
// anything older than 90 days regardless, so the clamp costs no data.
const maxLookback = 89 * 24 * time.Hour

// Fetch retrieves accounts and their transactions from the access URL. When
// since is non-zero, only transactions on or after that time are requested
// (clamped to the bridge's maxLookback window so we never ask for more history
// than it will return).
func (c *Client) Fetch(ctx context.Context, accessURL string, since time.Time) (*AccountSet, error) {
	base := strings.TrimRight(accessURL, "/")
	u, err := url.Parse(base + "/accounts")
	if err != nil {
		// Do not wrap err: a parse error echoes the raw URL, which embeds the
		// SimpleFIN credentials. (HTTP client errors below redact the password.)
		return nil, errors.New("invalid SimpleFIN access URL")
	}

	q := u.Query()
	if !since.IsZero() {
		if earliest := time.Now().Add(-maxLookback); since.Before(earliest) {
			since = earliest
		}
		q.Set("start-date", strconv.FormatInt(since.Unix(), 10))
	}
	q.Set("pending", "1") // include pending transactions
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build accounts request: %w", err)
	}
	// Credentials are carried in the access URL userinfo; net/http sends them
	// as HTTP basic auth automatically.

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch accounts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("fetch accounts: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var set AccountSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode accounts: %w", err)
	}
	return &set, nil
}

// AccountSet is the top-level SimpleFIN /accounts response.
type AccountSet struct {
	Errors   []string  `json:"errors"`
	Accounts []Account `json:"accounts"`
}

// Org describes the financial institution that owns an account.
type Org struct {
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	SfinURL string `json:"sfin-url"`
	URL     string `json:"url"`
	ID      string `json:"id"`
}

// Account is a single account with its transactions.
type Account struct {
	Org              Org           `json:"org"`
	ID               string        `json:"id"`
	Name             string        `json:"name"`
	Currency         string        `json:"currency"`
	Balance          string        `json:"balance"`
	AvailableBalance string        `json:"available-balance"`
	BalanceDate      int64         `json:"balance-date"`
	Transactions     []Transaction `json:"transactions"`
}

// Transaction is a single posted or pending transaction.
type Transaction struct {
	ID           string `json:"id"`
	Posted       int64  `json:"posted"`
	Amount       string `json:"amount"`
	Description  string `json:"description"`
	Payee        string `json:"payee"`
	Memo         string `json:"memo"`
	Pending      bool   `json:"pending"`
	TransactedAt int64  `json:"transacted_at"`
}

// StableOrgID returns a stable identifier for an organization. SimpleFIN orgs
// are not guaranteed to carry an id, so we fall back to the domain, then the
// SFIN URL.
func (o Org) StableOrgID() string {
	switch {
	case o.ID != "":
		return o.ID
	case o.Domain != "":
		return o.Domain
	default:
		return o.SfinURL
	}
}

// transactionDate returns the posted time when set, else the transacted time.
func transactionDate(t Transaction) int64 {
	if t.Posted != 0 {
		return t.Posted
	}
	return t.TransactedAt
}
