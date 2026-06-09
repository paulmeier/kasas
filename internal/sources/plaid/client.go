// Package plaid implements Plaid (https://plaid.com) as a kasas ingestion source.
// It is a first-party [source.Puller]: it fetches accounts, balances, and
// transactions from the Plaid API and returns them as a neutral
// source.ImportBatch for the ingestion engine to persist.
//
// Plaid's auth model has two layers, both handled here. The app-level client_id
// and secret (one pair per environment) authenticate every request and are sent
// in the JSON body, not a header. The per-Item access token — one per linked bank,
// obtained by exchanging a Plaid Link public_token — identifies whose data to
// fetch and is also sent in the body. A household links several banks, so the
// source fans out over a set of access tokens, exactly like Teller. See
// https://plaid.com/docs/api.
package plaid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// apiVersion pins the Plaid API version. Plaid is versioned by date; pinning keeps
// response shapes stable regardless of the account's dashboard default.
const apiVersion = "2020-09-14"

// dateLayout is Plaid's transaction date format (e.g. "2024-06-10").
const dateLayout = "2006-01-02"

// epochStart is the start_date used for an unbounded ("all available") fetch, since
// Plaid's /transactions/get requires a concrete start date.
const epochStart = "2000-01-01"

const (
	// pageCount is the per-page transaction count; 500 is Plaid's maximum.
	pageCount = 500
	// maxPages bounds pagination as a runaway guard (500 * 100 = 50k transactions);
	// termination normally relies on total_transactions, not this.
	maxPages = 100
)

// baseURLFor maps a Plaid environment name to its API host. An empty value
// defaults to the sandbox; an unrecognized value is a configuration error.
func baseURLFor(env string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "", "sandbox":
		return "https://sandbox.plaid.com", nil
	case "development":
		return "https://development.plaid.com", nil
	case "production":
		return "https://production.plaid.com", nil
	default:
		return "", fmt.Errorf("unknown plaid environment %q (want sandbox, development, or production)", env)
	}
}

// Client talks to the Plaid API over HTTP. Every request is a POST with a JSON
// body that carries the app credentials and (where applicable) the access token.
type Client struct {
	httpClient *http.Client
	baseURL    string
	clientID   string
	secret     string
}

// NewClient returns a client for baseURL authenticating with the given app
// credentials. baseURL is the environment host (see baseURLFor) and is overridable
// so tests can point the client at an httptest server.
func NewClient(baseURL, clientID, secret string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
		clientID:   clientID,
		secret:     secret,
	}
}

// baseRequest is embedded in every request body to carry the app credentials Plaid
// requires on each call.
type baseRequest struct {
	ClientID string `json:"client_id"`
	Secret   string `json:"secret"`
}

func (c *Client) auth() baseRequest { return baseRequest{ClientID: c.clientID, Secret: c.secret} }

// Balances are an account's balances. The figures are decimal numbers captured as
// json.Number so the exact value is preserved (no float round-trip); a null in the
// JSON leaves the field empty.
type Balances struct {
	Available              json.Number `json:"available"`
	Current                json.Number `json:"current"`
	ISOCurrencyCode        string      `json:"iso_currency_code"`
	UnofficialCurrencyCode string      `json:"unofficial_currency_code"`
}

// Account is a Plaid account.
type Account struct {
	AccountID    string   `json:"account_id"`
	Name         string   `json:"name"`
	OfficialName string   `json:"official_name"`
	Mask         string   `json:"mask"`
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	Balances     Balances `json:"balances"`
}

// Item is the Plaid Item (one access token = one institution login). It carries the
// institution id, which the source resolves to a display name.
type Item struct {
	ItemID        string `json:"item_id"`
	InstitutionID string `json:"institution_id"`
}

// Institution is the financial institution behind an Item.
type Institution struct {
	InstitutionID string `json:"institution_id"`
	Name          string `json:"name"`
}

// Transaction is a single posted or pending transaction. Amount is a decimal number
// captured as json.Number (preserving the exact value); NOTE Plaid signs OUTFLOWS
// POSITIVE — the opposite of kasas — so the source negates it on mapping.
type Transaction struct {
	TransactionID          string      `json:"transaction_id"`
	AccountID              string      `json:"account_id"`
	Amount                 json.Number `json:"amount"`
	ISOCurrencyCode        string      `json:"iso_currency_code"`
	UnofficialCurrencyCode string      `json:"unofficial_currency_code"`
	Date                   string      `json:"date"` // YYYY-MM-DD
	Name                   string      `json:"name"`
	MerchantName           string      `json:"merchant_name"`
	Pending                bool        `json:"pending"`
}

type accountsGetRequest struct {
	baseRequest
	AccessToken string `json:"access_token"`
}

// AccountsResponse is the /accounts/get result: the Item's accounts (with balances)
// plus the Item itself (for the institution id).
type AccountsResponse struct {
	Accounts []Account `json:"accounts"`
	Item     Item      `json:"item"`
}

// Accounts lists the accounts an access token can see, with current balances.
func (c *Client) Accounts(ctx context.Context, accessToken string) (*AccountsResponse, error) {
	var out AccountsResponse
	if err := c.post(ctx, "/accounts/get", accountsGetRequest{baseRequest: c.auth(), AccessToken: accessToken}, &out); err != nil {
		return nil, fmt.Errorf("get accounts: %w", err)
	}
	return &out, nil
}

type transactionsGetRequest struct {
	baseRequest
	AccessToken string                 `json:"access_token"`
	StartDate   string                 `json:"start_date"`
	EndDate     string                 `json:"end_date"`
	Options     transactionsGetOptions `json:"options"`
}

type transactionsGetOptions struct {
	Count  int `json:"count"`
	Offset int `json:"offset"`
}

type transactionsResponse struct {
	Transactions      []Transaction `json:"transactions"`
	TotalTransactions int           `json:"total_transactions"`
}

// Transactions returns an Item's transactions on or after since (zero means all
// available), across every account the access token covers. Plaid returns a date
// window and paginates by offset, reporting the full count as total_transactions;
// this walks pages forward until it has them all. The engine deduplicates by
// (source, transaction id), so a re-fetched window is harmless.
func (c *Client) Transactions(ctx context.Context, accessToken string, since time.Time) ([]Transaction, error) {
	start := epochStart
	if !since.IsZero() {
		start = since.UTC().Format(dateLayout)
	}
	end := time.Now().UTC().Format(dateLayout)

	var all []Transaction
	for page := 0; page < maxPages; page++ {
		var resp transactionsResponse
		err := c.post(ctx, "/transactions/get", transactionsGetRequest{
			baseRequest: c.auth(),
			AccessToken: accessToken,
			StartDate:   start,
			EndDate:     end,
			Options:     transactionsGetOptions{Count: pageCount, Offset: len(all)},
		}, &resp)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Transactions...)
		if len(resp.Transactions) == 0 || len(all) >= resp.TotalTransactions {
			break
		}
	}
	return all, nil
}

type institutionGetByIDRequest struct {
	baseRequest
	InstitutionID string   `json:"institution_id"`
	CountryCodes  []string `json:"country_codes"`
}

type institutionResponse struct {
	Institution Institution `json:"institution"`
}

// Institution resolves an institution id to its details (chiefly the display name).
// countryCodes scopes the lookup (Plaid requires it); it defaults to US when empty.
// This call authenticates with the app credentials only — no access token.
func (c *Client) Institution(ctx context.Context, institutionID string, countryCodes []string) (*Institution, error) {
	if strings.TrimSpace(institutionID) == "" {
		return nil, errors.New("no institution id")
	}
	if len(countryCodes) == 0 {
		countryCodes = []string{"US"}
	}
	var out institutionResponse
	if err := c.post(ctx, "/institutions/get_by_id", institutionGetByIDRequest{
		baseRequest:   c.auth(),
		InstitutionID: institutionID,
		CountryCodes:  countryCodes,
	}, &out); err != nil {
		return nil, fmt.Errorf("get institution: %w", err)
	}
	return &out.Institution, nil
}

// post performs an authenticated POST with a JSON body and decodes the JSON response
// into out. The request body carries the secret, so it is never echoed in an error;
// Plaid error responses do not contain the secret, so surfacing them is safe.
func (c *Client) post(ctx context.Context, path string, reqBody, out any) error {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Plaid-Version", apiVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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

// apiError formats a non-200 response. Plaid returns errors as
// {"error_type":...,"error_code":...,"error_message":...}; the body never contains
// the access token or secret (those travel only in the request), so it is safe to
// surface.
func apiError(status int, body []byte) error {
	var env struct {
		ErrorType    string `json:"error_type"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	if json.Unmarshal(body, &env) == nil && env.ErrorMessage != "" {
		if env.ErrorCode != "" {
			return fmt.Errorf("plaid API error (status %d): %s: %s", status, env.ErrorCode, env.ErrorMessage)
		}
		return fmt.Errorf("plaid API error (status %d): %s", status, env.ErrorMessage)
	}
	return fmt.Errorf("plaid API error (status %d): %s", status, strings.TrimSpace(string(body)))
}
