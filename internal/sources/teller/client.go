// Package teller implements Teller (https://teller.io) as a kasas ingestion
// source. It is a first-party [source.Puller]: it fetches accounts, balances,
// and transactions from the Teller API and returns them as a neutral
// source.ImportBatch for the ingestion engine to persist.
//
// Teller's auth model differs from SimpleFIN in two ways the client handles: the
// per-enrollment access token is sent as the HTTP basic-auth username (with an
// empty password), and requests for real user data (the development and
// production environments) require a mutual-TLS client certificate. The sandbox
// environment needs no certificate. See https://teller.io/docs/api.
package teller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiBaseURL is the Teller API root. It is a package var so tests can point the
// client at an httptest server.
const apiBaseURL = "https://api.teller.io"

// dateLayout is Teller's transaction date format (e.g. "2023-07-13").
const dateLayout = "2006-01-02"

const (
	// pageCount is the per-page transaction count hint. Termination never relies on
	// it (Teller may return fewer), so a conservative value is safe.
	pageCount = 100
	// maxPages bounds backward pagination as a runaway guard. With a non-zero since
	// window the date cutoff stops paging long before this; it only matters for an
	// unbounded ("all available") fetch of an extremely long history.
	maxPages = 1000
)

// Client talks to the Teller API over HTTP.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient returns a client for baseURL. When cert is non-nil it is presented as
// a mutual-TLS client certificate on every request (required for Teller's
// development and production environments); a nil cert yields a plain HTTPS client
// suitable for the sandbox.
func NewClient(baseURL string, cert *tls.Certificate) *Client {
	hc := &http.Client{Timeout: 60 * time.Second}
	if cert != nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{
			Certificates: []tls.Certificate{*cert},
			MinVersion:   tls.VersionTLS12,
		}
		hc.Transport = transport
	}
	return &Client{httpClient: hc, baseURL: strings.TrimRight(baseURL, "/")}
}

// Account is a Teller account.
type Account struct {
	ID           string      `json:"id"`
	EnrollmentID string      `json:"enrollment_id"`
	Name         string      `json:"name"`
	Type         string      `json:"type"`
	Subtype      string      `json:"subtype"`
	Currency     string      `json:"currency"`
	LastFour     string      `json:"last_four"`
	Status       string      `json:"status"`
	Institution  Institution `json:"institution"`
}

// Institution is the bank that owns an account.
type Institution struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Balance is an account's balances. Both figures are decimal strings; ledger is
// the booked balance, available the spendable one.
type Balance struct {
	AccountID string `json:"account_id"`
	Ledger    string `json:"ledger"`
	Available string `json:"available"`
}

// Transaction is a single posted or pending transaction. Amount is the signed
// decimal amount as a string (Teller signs outflows negative, matching kasas).
type Transaction struct {
	ID             string             `json:"id"`
	AccountID      string             `json:"account_id"`
	Amount         string             `json:"amount"`
	Date           string             `json:"date"` // YYYY-MM-DD
	Description    string             `json:"description"`
	Status         string             `json:"status"` // "posted" | "pending"
	Type           string             `json:"type"`
	RunningBalance string             `json:"running_balance"`
	Details        TransactionDetails `json:"details"`
}

// TransactionDetails is Teller's enrichment of a transaction.
type TransactionDetails struct {
	ProcessingStatus string       `json:"processing_status"`
	Category         string       `json:"category"`
	Counterparty     Counterparty `json:"counterparty"`
}

// Counterparty is the cleaned merchant or person on the other side of a
// transaction.
type Counterparty struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Accounts lists all accounts the access token can see.
func (c *Client) Accounts(ctx context.Context, token string) ([]Account, error) {
	var accs []Account
	if err := c.get(ctx, token, "/accounts", &accs); err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	return accs, nil
}

// Balance fetches one account's balances.
func (c *Client) Balance(ctx context.Context, token, accountID string) (*Balance, error) {
	var b Balance
	if err := c.get(ctx, token, "/accounts/"+url.PathEscape(accountID)+"/balances", &b); err != nil {
		return nil, fmt.Errorf("get balance: %w", err)
	}
	return &b, nil
}

// Transactions returns an account's transactions on or after since (zero means
// everything). Teller returns transactions newest-first and paginates backward
// via from_id, so this walks pages older and older, stopping once it reaches a
// transaction before the window. A seen set guards against an inclusive from_id
// returning the cursor row again (and thus a non-terminating loop).
func (c *Client) Transactions(ctx context.Context, token, accountID string, since time.Time) ([]Transaction, error) {
	var all []Transaction
	seen := make(map[string]bool)
	fromID := ""
	for page := 0; page < maxPages; page++ {
		items, err := c.transactionsPage(ctx, token, accountID, fromID)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		stop, progressed := false, false
		for _, t := range items {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			progressed = true
			if !since.IsZero() {
				if d, derr := time.Parse(dateLayout, t.Date); derr == nil && d.Before(since) {
					stop = true
					break
				}
			}
			all = append(all, t)
		}
		last := items[len(items)-1].ID
		if stop || !progressed || last == fromID {
			break
		}
		fromID = last
	}
	return all, nil
}

// transactionsPage fetches one page of an account's transactions, starting before
// fromID when it is set.
func (c *Client) transactionsPage(ctx context.Context, token, accountID, fromID string) ([]Transaction, error) {
	path := fmt.Sprintf("/accounts/%s/transactions?count=%d", url.PathEscape(accountID), pageCount)
	if fromID != "" {
		path += "&from_id=" + url.QueryEscape(fromID)
	}
	var items []Transaction
	if err := c.get(ctx, token, path, &items); err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	return items, nil
}

// get performs an authenticated GET and decodes the JSON body into out. The token
// is sent as the basic-auth username with an empty password, as Teller expects.
func (c *Client) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(token, "")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// apiError formats a non-200 response. Teller returns errors as
// {"error":{"code":...,"message":...}}; the body never echoes the access token
// (it travels in the Authorization header), so it is safe to surface.
func apiError(status int, body []byte) error {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &env) == nil && env.Error.Message != "" {
		if env.Error.Code != "" {
			return fmt.Errorf("teller API error (status %d): %s: %s", status, env.Error.Code, env.Error.Message)
		}
		return fmt.Errorf("teller API error (status %d): %s", status, env.Error.Message)
	}
	return fmt.Errorf("teller API error (status %d): %s", status, strings.TrimSpace(string(body)))
}
