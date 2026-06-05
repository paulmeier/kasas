package poller

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

// SimpleFINClient talks to a SimpleFIN bridge. See https://www.simplefin.org/protocol.html.
type SimpleFINClient struct {
	httpClient *http.Client
}

// NewSimpleFINClient returns a client with a sane default timeout.
func NewSimpleFINClient() *SimpleFINClient {
	return &SimpleFINClient{
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// Claim exchanges a base64-encoded setup token for a long-lived access URL.
// The setup token decodes to a one-time claim URL that is POSTed to obtain the
// access URL (which embeds HTTP basic-auth credentials in its userinfo).
func (c *SimpleFINClient) Claim(ctx context.Context, setupToken string) (string, error) {
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

// Fetch retrieves accounts and their transactions from the access URL. When
// since is non-zero, only transactions on or after that time are requested.
func (c *SimpleFINClient) Fetch(ctx context.Context, accessURL string, since time.Time) (*AccountSet, error) {
	base := strings.TrimRight(accessURL, "/")
	u, err := url.Parse(base + "/accounts")
	if err != nil {
		// Do not wrap err: a parse error echoes the raw URL, which embeds the
		// SimpleFIN credentials. (HTTP client errors below redact the password.)
		return nil, errors.New("invalid SimpleFIN access URL")
	}

	q := u.Query()
	if !since.IsZero() {
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
	Errors   []string           `json:"errors"`
	Accounts []SimpleFINAccount `json:"accounts"`
}

// SimpleFINOrg describes the financial institution that owns an account.
type SimpleFINOrg struct {
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	SfinURL string `json:"sfin-url"`
	URL     string `json:"url"`
	ID      string `json:"id"`
}

// SimpleFINAccount is a single account with its transactions.
type SimpleFINAccount struct {
	Org              SimpleFINOrg           `json:"org"`
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Currency         string                 `json:"currency"`
	Balance          string                 `json:"balance"`
	AvailableBalance string                 `json:"available-balance"`
	BalanceDate      int64                  `json:"balance-date"`
	Transactions     []SimpleFINTransaction `json:"transactions"`
}

// SimpleFINTransaction is a single posted or pending transaction.
type SimpleFINTransaction struct {
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
func (o SimpleFINOrg) StableOrgID() string {
	switch {
	case o.ID != "":
		return o.ID
	case o.Domain != "":
		return o.Domain
	default:
		return o.SfinURL
	}
}
