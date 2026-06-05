package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// These structs mirror the subset of the kasas REST DTOs the dashboard needs
// (see internal/api/dto.go). They are defined locally so the WASM build does
// not import internal/api (which pulls in sqlite, the MCP SDK, etc.).

type account struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Currency string `json:"currency"`
	Balance  string `json:"balance"`
}

type transaction struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Amount      string    `json:"amount"`
	Pending     bool      `json:"pending"`
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
	Payee       string    `json:"payee"`
}

type updateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"update_available"`
	URL       string `json:"release_url"`
	CanApply  bool   `json:"can_apply"`
}

type applyResult struct {
	Updated    bool   `json:"updated"`
	Version    string `json:"version"`
	Restarting bool   `json:"restarting"`
	Message    string `json:"message"`
}

// apiClient calls the same-origin kasas REST API from the browser. In WASM,
// net/http is backed by the Fetch API.
type apiClient struct {
	base string
	http *http.Client
}

func newAPIClient(base string) *apiClient {
	return &apiClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *apiClient) accounts(ctx context.Context) ([]account, error) {
	var out struct {
		Accounts []account `json:"accounts"`
	}
	if err := c.get(ctx, "/api/v1/accounts", nil, &out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

func (c *apiClient) transactions(ctx context.Context, accountID string, limit, offset int) ([]transaction, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	if accountID != "" {
		q.Set("account_id", accountID)
	}
	var out struct {
		Transactions []transaction `json:"transactions"`
	}
	if err := c.get(ctx, "/api/v1/transactions", q, &out); err != nil {
		return nil, err
	}
	return out.Transactions, nil
}

// allTransactions fetches every transaction for the given account filter ("" =
// all accounts) by paging through the API in the largest batches the server
// allows. The dashboard sorts and paginates client-side, so it needs the full
// set rather than a single server page.
func (c *apiClient) allTransactions(ctx context.Context, accountID string) ([]transaction, error) {
	const (
		batch    = 1000 // server maxLimit; fetch in the largest pages allowed
		maxPages = 500  // safety bound (~500k rows) against a misbehaving server
	)
	var all []transaction
	for page := 0; page < maxPages; page++ {
		rows, err := c.transactions(ctx, accountID, batch, page*batch)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < batch {
			break
		}
	}
	return all, nil
}

func (c *apiClient) updateStatus(ctx context.Context) (updateStatus, error) {
	var out updateStatus
	if err := c.get(ctx, "/api/v1/update", nil, &out); err != nil {
		return updateStatus{}, err
	}
	return out, nil
}

// applyUpdate triggers the server-side self-update. It can take a while (the
// server downloads and verifies a release), so it uses a longer timeout than
// the default client and surfaces the server's error message on failure.
func (c *apiClient) applyUpdate(ctx context.Context) (applyResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/update", nil)
	if err != nil {
		return applyResult{}, err
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return applyResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return applyResult{}, fmt.Errorf("%s", e.Error)
		}
		return applyResult{}, fmt.Errorf("update request failed: status %d", resp.StatusCode)
	}
	var out applyResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return applyResult{}, err
	}
	return out, nil
}

// buildVersion fetches the server's build version string (the go-app
// service-worker cache key: "<binary-version>-<wasmhash>"). It is plain text
// rather than JSON, served from the dashboard handler at /web/version.
func (c *apiClient) buildVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/web/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET /web/version: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *apiClient) get(ctx context.Context, path string, q url.Values, dst any) error {
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
