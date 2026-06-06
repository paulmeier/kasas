package dashboard

import (
	"bytes"
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
	ID          string            `json:"id"`
	AccountID   string            `json:"account_id"`
	Amount      string            `json:"amount"`
	Pending     bool              `json:"pending"`
	Date        time.Time         `json:"date"`
	Description string            `json:"description"`
	Payee       string            `json:"payee"`
	Memo        string            `json:"memo"`
	SyncedAt    time.Time         `json:"synced_at"`
	Labels      map[string]string `json:"labels"`
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
	base  string
	token string // dashboard token, attached as a Bearer header when non-empty
	http  *http.Client
}

func newAPIClient(base, token string) *apiClient {
	c := &apiClient{base: base, token: token}
	c.http = c.client(30 * time.Second)
	return c
}

// client builds an *http.Client with the given timeout whose transport attaches
// the dashboard token. Used for the default client and the longer-lived one in
// applyUpdate, so both authenticate.
func (c *apiClient) client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &authTransport{base: http.DefaultTransport, token: c.token}}
}

// authTransport attaches "Authorization: Bearer <token>" to every request when a
// token is set. http.DefaultTransport is the Fetch-backed RoundTripper in WASM.
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.token == "" {
		return t.base.RoundTrip(req)
	}
	// Clone before mutating: RoundTrippers must not modify the caller's request.
	r := req.Clone(req.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
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

// labelCount is one label (key/value pair) in the global vocabulary with the
// number of transactions that carry it (mirrors api.LabelDTO).
type labelCount struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Count int    `json:"transaction_count"`
}

// labelCounts fetches the global label vocabulary with per-pair transaction
// counts, used by the Labels page.
func (c *apiClient) labelCounts(ctx context.Context) ([]labelCount, error) {
	var out struct {
		Labels []labelCount `json:"labels"`
	}
	if err := c.get(ctx, "/api/v1/labels", nil, &out); err != nil {
		return nil, err
	}
	return out.Labels, nil
}

// labelSuggestions fetches the vocabulary as "key: value" strings to drive the
// dashboard typeahead.
func (c *apiClient) labelSuggestions(ctx context.Context) ([]string, error) {
	counts, err := c.labelCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(counts))
	for i, l := range counts {
		out[i] = formatLabel(l.Key, l.Value)
	}
	return out, nil
}

// deleteLabel removes a label (key with the given value) from every transaction
// that carries it and returns the number of transactions affected.
func (c *apiClient) deleteLabel(ctx context.Context, key, value string) (int, error) {
	u := c.base + "/api/v1/labels/" + url.PathEscape(key) + "?value=" + url.QueryEscape(value)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return 0, fmt.Errorf("%s", e.Error)
		}
		return 0, fmt.Errorf("DELETE label: status %d", resp.StatusCode)
	}
	var out struct {
		RemovedFrom int `json:"removed_from"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.RemovedFrom, nil
}

// setLabels replaces a transaction's entire label set and returns the
// server-normalized result (trimmed, lowercased keys, invalid pairs dropped),
// which the caller adopts as the canonical value.
func (c *apiClient) setLabels(ctx context.Context, id string, labels map[string]string) (map[string]string, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	body, err := json.Marshal(map[string]map[string]string{"labels": labels})
	if err != nil {
		return nil, err
	}
	u := c.base + "/api/v1/transactions/" + url.PathEscape(id) + "/labels"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return nil, fmt.Errorf("%s", e.Error)
		}
		return nil, fmt.Errorf("PUT labels: status %d", resp.StatusCode)
	}
	var out struct {
		Labels map[string]string `json:"labels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Labels == nil {
		out.Labels = map[string]string{}
	}
	return out.Labels, nil
}

// rule mirrors api.RuleDTO: an auto-labeling rule (a condition query plus the
// labels applied to every matching transaction).
type rule struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	Query     string            `json:"query"`
	Labels    map[string]string `json:"labels"`
	Enabled   bool              `json:"enabled"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// rulePayload is the create/update request body (mirrors api.ruleInput).
type rulePayload struct {
	Name    string            `json:"name"`
	Query   string            `json:"query"`
	Labels  map[string]string `json:"labels"`
	Enabled bool              `json:"enabled"`
}

// runResult is the rules run response: how many transactions matched and how many
// were newly labeled.
type runResult struct {
	Matched int `json:"matched"`
	Updated int `json:"updated"`
}

func (c *apiClient) listRules(ctx context.Context) ([]rule, error) {
	var out struct {
		Rules []rule `json:"rules"`
	}
	if err := c.get(ctx, "/api/v1/rules", nil, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

func (c *apiClient) createRule(ctx context.Context, p rulePayload) (rule, error) {
	return c.sendRule(ctx, http.MethodPost, "/api/v1/rules", p)
}

func (c *apiClient) updateRule(ctx context.Context, id int64, p rulePayload) (rule, error) {
	return c.sendRule(ctx, http.MethodPut, "/api/v1/rules/"+strconv.FormatInt(id, 10), p)
}

// sendRule POSTs or PUTs a rule payload and decodes the returned rule, surfacing
// the server's error message (e.g. an invalid query) on failure.
func (c *apiClient) sendRule(ctx context.Context, method, path string, p rulePayload) (rule, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return rule{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return rule{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return rule{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return rule{}, decodeAPIError(resp, "save rule")
	}
	var out rule
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return rule{}, err
	}
	return out, nil
}

func (c *apiClient) deleteRule(ctx context.Context, id int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/v1/rules/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp, "delete rule")
	}
	return nil
}

func (c *apiClient) runRule(ctx context.Context, id int64) (runResult, error) {
	return c.postRun(ctx, "/api/v1/rules/"+strconv.FormatInt(id, 10)+"/run")
}

func (c *apiClient) runAllRules(ctx context.Context) (runResult, error) {
	return c.postRun(ctx, "/api/v1/rules/run")
}

func (c *apiClient) postRun(ctx context.Context, path string) (runResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return runResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return runResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return runResult{}, decodeAPIError(resp, "run rules")
	}
	var out runResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return runResult{}, err
	}
	return out, nil
}

// decodeAPIError reads a non-2xx response's {"error": "..."} body and returns it
// as an error, falling back to the status code.
func decodeAPIError(resp *http.Response, op string) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("%s: status %d", op, resp.StatusCode)
}

// These config structs mirror api.ConfigDTO (secrets already redacted server-side)
// for the read-only Settings display.
type configData struct {
	Server    serverConfig    `json:"server"`
	Log       logConfig       `json:"log"`
	Database  databaseConfig  `json:"database"`
	SimpleFIN simplefinConfig `json:"simplefin"`
	Sync      syncConfig      `json:"sync"`
	Vault     vaultConfig     `json:"vault"`
	Secrets   secretsConfig   `json:"secrets"`
	MCP       mcpConfig       `json:"mcp"`
	Dashboard dashboardConfig `json:"dashboard"`
	Update    updateConfig    `json:"update"`
	Security  securityConfig  `json:"security"`
}

type serverConfig struct {
	Addr string `json:"addr"`
}
type logConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
}
type databaseConfig struct {
	Driver string `json:"driver"`
	Path   string `json:"path"`
	DSN    string `json:"dsn"`
}
type simplefinConfig struct {
	Connected bool `json:"connected"`
}
type syncConfig struct {
	Enabled      bool   `json:"enabled"`
	Interval     string `json:"interval"`
	LookbackDays int    `json:"lookback_days"`
	RunOnStart   bool   `json:"run_on_start"`
}
type vaultConfig struct {
	Enabled      bool   `json:"enabled"`
	Address      string `json:"address"`
	Mount        string `json:"mount"`
	Path         string `json:"path"`
	AccessURLKey string `json:"access_url_key"`
	TokenSet     bool   `json:"token_set"`
}
type secretsConfig struct {
	File string `json:"file"`
}
type mcpConfig struct {
	Enabled bool `json:"enabled"`
}
type dashboardConfig struct {
	Enabled bool `json:"enabled"`
}
type updateConfig struct {
	Check      bool   `json:"check"`
	AllowApply bool   `json:"allow_apply"`
	Repository string `json:"repository"`
}
type securityConfig struct {
	AuthRequired bool   `json:"auth_required"`
	TokenSource  string `json:"token_source"` // "config" | "stored" | "none"
}

// authState mirrors api.authStatusResponse: whether a token is required and
// whether the caller's token (if any) is valid. Drives the login gate.
type authState struct {
	AuthRequired  bool `json:"auth_required"`
	Authenticated bool `json:"authenticated"`
}

// tokenResult mirrors api.tokenResponse: the value minted by generate/set plus the
// resulting auth state.
type tokenResult struct {
	Token        string `json:"token"`
	AuthRequired bool   `json:"auth_required"`
	TokenSource  string `json:"token_source"`
}

// syncRun mirrors api.SyncDTO: one sync_log entry. CompletedAt is the zero time
// while a sync is still running (the API sends null).
type syncRun struct {
	ID          int64     `json:"id"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	Status      string    `json:"status"`
	Error       string    `json:"error"`
}

// config fetches the effective configuration (secrets redacted) for the Settings page.
func (c *apiClient) config(ctx context.Context) (configData, error) {
	var out configData
	if err := c.get(ctx, "/api/v1/config", nil, &out); err != nil {
		return configData{}, err
	}
	return out, nil
}

// authStatus reports whether a dashboard token is required and whether the
// client's current token (if any) is accepted. It is the one endpoint reachable
// without a valid token, so it can drive the login gate.
func (c *apiClient) authStatus(ctx context.Context) (authState, error) {
	var out authState
	if err := c.get(ctx, "/api/v1/auth", nil, &out); err != nil {
		return authState{}, err
	}
	return out, nil
}

// setToken generates a new dashboard token (custom == "") or stores a caller-
// supplied one, returning the resulting token value and auth state.
func (c *apiClient) setToken(ctx context.Context, custom string) (tokenResult, error) {
	var body io.Reader
	if custom != "" {
		b, err := json.Marshal(map[string]string{"token": custom})
		if err != nil {
			return tokenResult{}, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/security/token", body)
	if err != nil {
		return tokenResult{}, err
	}
	if custom != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return tokenResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tokenResult{}, decodeAPIError(resp, "set token")
	}
	var out tokenResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return tokenResult{}, err
	}
	return out, nil
}

// revokeToken clears the stored dashboard token, disabling authentication.
func (c *apiClient) revokeToken(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/v1/security/token", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp, "revoke token")
	}
	return nil
}

// latestSync fetches the most recent sync run, or nil when no sync has run yet.
func (c *apiClient) latestSync(ctx context.Context) (*syncRun, error) {
	var out struct {
		Latest *syncRun `json:"latest"`
	}
	if err := c.get(ctx, "/api/v1/sync", nil, &out); err != nil {
		return nil, err
	}
	return out.Latest, nil
}

// setSimpleFINToken stores a SimpleFIN setup token or access URL and returns the
// resulting connection state.
func (c *apiClient) setSimpleFINToken(ctx context.Context, token string) (bool, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+"/api/v1/simplefin/credential", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return false, fmt.Errorf("%s", e.Error)
		}
		return false, fmt.Errorf("set credential: status %d", resp.StatusCode)
	}
	var out struct {
		Connected bool `json:"connected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.Connected, nil
}

// triggerSync starts a sync. The server runs it asynchronously and returns 202;
// progress is then observable via latestSync.
func (c *apiClient) triggerSync(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/sync", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("trigger sync: status %d", resp.StatusCode)
	}
	return nil
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
	resp, err := c.client(5 * time.Minute).Do(req)
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
