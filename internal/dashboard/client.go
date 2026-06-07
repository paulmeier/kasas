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

	"github.com/paulmeier/kasas/internal/relationships"
)

// These structs mirror the subset of the kasas REST DTOs the dashboard needs
// (see internal/api/dto.go). They are defined locally so the WASM build does
// not import internal/api (which pulls in sqlite, the MCP SDK, etc.).

type account struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
	BalanceDate time.Time `json:"balance_date"`
	// Source is the account's provenance ("simplefin" or "manual"); the dashboard
	// shows edit/delete affordances only for manual accounts.
	Source string `json:"source"`
}

type transaction struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Amount      string    `json:"amount"`
	Pending     bool      `json:"pending"`
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
	Payee       string    `json:"payee"`
	Memo        string    `json:"memo"`
	SyncedAt    time.Time `json:"synced_at"`
	// Source is the transaction's provenance ("simplefin" or "manual"); the
	// dashboard shows edit/delete affordances only for manual transactions.
	Source     string                     `json:"source"`
	Labels     map[string]string          `json:"labels"`
	Extensions map[string]json.RawMessage `json:"extensions"`
	// Relationships is this transaction's own OUTBOUND edges (the API inlines only
	// these per row). Used for the row indicator and to build the inbound index for
	// in-browser rel:/related: search; the full neighborhood is fetched on demand.
	Relationships []relationships.Relationship `json:"relationships"`
}

// relationshipEdge mirrors api.RelationshipDTO: one edge in a transaction's
// neighborhood (kind, direction outbound|inbound, and the other transaction's id).
type relationshipEdge struct {
	Kind               string `json:"kind"`
	Direction          string `json:"direction"`
	OtherTransactionID string `json:"other_transaction_id"`
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

// event mirrors api.EventDTO: one entry in the canonical event stream. Data is the
// raw JSON payload, shown verbatim (and pretty-printed) in the Events page.
type event struct {
	Sequence   int64           `json:"sequence"`
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// recentEvents fetches the most recent events (chronological order) plus the head
// sequence to resume forward polling from. Used for the Events page's initial load.
func (c *apiClient) recentEvents(ctx context.Context, limit int) ([]event, int64, error) {
	q := url.Values{}
	q.Set("newest", "1")
	q.Set("limit", strconv.Itoa(limit))
	return c.fetchEvents(ctx, q)
}

// events fetches the events after the given sequence cursor (the live forward
// tail), plus the new cursor.
func (c *apiClient) events(ctx context.Context, after int64, limit int) ([]event, int64, error) {
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	q.Set("limit", strconv.Itoa(limit))
	return c.fetchEvents(ctx, q)
}

func (c *apiClient) fetchEvents(ctx context.Context, q url.Values) ([]event, int64, error) {
	var out struct {
		Events []event `json:"events"`
		Next   int64   `json:"next"`
	}
	if err := c.get(ctx, "/api/v1/events", q, &out); err != nil {
		return nil, 0, err
	}
	return out.Events, out.Next, nil
}

// version mirrors api.VersionDTO: one entry in a transaction's immutable history.
// Transaction is the raw snapshot JSON, shown verbatim (pretty-printed) in an
// expandable block; Diff is the change from the previous version.
type version struct {
	Version     int             `json:"version"`
	ChangeKind  string          `json:"change_kind"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Transaction json.RawMessage `json:"transaction"`
	Diff        versionDiff     `json:"diff"`
}

// versionDiff mirrors api.VersionDiffDTO: the scalar field changes and label deltas
// from the previous version.
type versionDiff struct {
	Fields        []fieldChange          `json:"fields"`
	LabelsAdded   map[string]string      `json:"labels_added"`
	LabelsRemoved map[string]string      `json:"labels_removed"`
	LabelsChanged map[string]labelChange `json:"labels_changed"`
}

type fieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type labelChange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// transactionHistory fetches one transaction's full version history (oldest first).
func (c *apiClient) transactionHistory(ctx context.Context, id string) ([]version, error) {
	var out struct {
		Versions []version `json:"versions"`
	}
	if err := c.get(ctx, "/api/v1/transactions/"+url.PathEscape(id)+"/history", nil, &out); err != nil {
		return nil, err
	}
	return out.Versions, nil
}

// transactionRelationships fetches one transaction's full relationship
// neighborhood: its outbound edges plus the inbound edges of others targeting it.
func (c *apiClient) transactionRelationships(ctx context.Context, id string) ([]relationshipEdge, error) {
	var out struct {
		Relationships []relationshipEdge `json:"relationships"`
	}
	if err := c.get(ctx, "/api/v1/transactions/"+url.PathEscape(id)+"/relationships", nil, &out); err != nil {
		return nil, err
	}
	return out.Relationships, nil
}

// createTransactionRelationship asserts one outbound edge id --kind--> target.
func (c *apiClient) createTransactionRelationship(ctx context.Context, id, kind, target string) error {
	body, err := json.Marshal(map[string]string{"kind": kind, "target": target})
	if err != nil {
		return err
	}
	u := c.base + "/api/v1/transactions/" + url.PathEscape(id) + "/relationships"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return relationshipAPIError(resp, "create relationship")
	}
	return nil
}

// deleteTransactionRelationship removes the outbound edge id --kind--> target. To
// drop an inbound edge, call this on the transaction that owns it (its subject).
func (c *apiClient) deleteTransactionRelationship(ctx context.Context, id, kind, target string) error {
	u := c.base + "/api/v1/transactions/" + url.PathEscape(id) + "/relationships" +
		"?kind=" + url.QueryEscape(kind) + "&target=" + url.QueryEscape(target)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return relationshipAPIError(resp, "delete relationship")
	}
	return nil
}

// relationshipAPIError extracts the API's {"error":...} message from a failed
// relationship request, falling back to the status code.
func relationshipAPIError(resp *http.Response, op string) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("%s", e.Error)
	}
	return fmt.Errorf("%s: status %d", op, resp.StatusCode)
}

// transactionPayload is the manual create/update request body (mirrors
// api.transactionInput). Date is a string the server parses (YYYY-MM-DD here).
type transactionPayload struct {
	AccountID   string `json:"account_id"`
	Amount      string `json:"amount"`
	Date        string `json:"date"`
	Description string `json:"description"`
	Payee       string `json:"payee"`
	Memo        string `json:"memo"`
	Pending     bool   `json:"pending"`
}

func (c *apiClient) createTransaction(ctx context.Context, p transactionPayload) (transaction, error) {
	return c.sendTransaction(ctx, http.MethodPost, "/api/v1/transactions", p)
}

func (c *apiClient) updateTransaction(ctx context.Context, id string, p transactionPayload) (transaction, error) {
	return c.sendTransaction(ctx, http.MethodPut, "/api/v1/transactions/"+url.PathEscape(id), p)
}

// sendTransaction POSTs or PUTs a transaction payload and decodes the returned
// transaction, surfacing the server's error message (a bad amount, a read-only
// synced row, ...) on failure.
func (c *apiClient) sendTransaction(ctx context.Context, method, path string, p transactionPayload) (transaction, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return transaction{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return transaction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return transaction{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return transaction{}, decodeAPIError(resp, "save transaction")
	}
	var out transaction
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return transaction{}, err
	}
	return out, nil
}

func (c *apiClient) deleteTransaction(ctx context.Context, id string) error {
	return c.sendDelete(ctx, "/api/v1/transactions/"+url.PathEscape(id), "delete transaction")
}

// accountPayload is the manual account create/update request body (mirrors
// api.accountInput). Balance/BalanceDate are optional on update (omit to keep).
type accountPayload struct {
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	Balance     string `json:"balance"`
	BalanceDate string `json:"balance_date,omitempty"`
}

func (c *apiClient) createAccount(ctx context.Context, p accountPayload) (account, error) {
	return c.sendAccount(ctx, http.MethodPost, "/api/v1/accounts", p)
}

func (c *apiClient) updateAccount(ctx context.Context, id string, p accountPayload) (account, error) {
	return c.sendAccount(ctx, http.MethodPut, "/api/v1/accounts/"+url.PathEscape(id), p)
}

func (c *apiClient) sendAccount(ctx context.Context, method, path string, p accountPayload) (account, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return account{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return account{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return account{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return account{}, decodeAPIError(resp, "save account")
	}
	var out account
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return account{}, err
	}
	return out, nil
}

func (c *apiClient) deleteAccount(ctx context.Context, id string) error {
	return c.sendDelete(ctx, "/api/v1/accounts/"+url.PathEscape(id), "delete account")
}

// sendDelete issues a DELETE and surfaces the API's error message on a non-200.
func (c *apiClient) sendDelete(ctx context.Context, path, op string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp, op)
	}
	return nil
}

// provenance mirrors provenance.Provenance: a transaction's read-only lineage — where
// it came from (source, upstream id, account, institution), when it was first and
// last seen, and the ordered transformations it has undergone.
type provenance struct {
	TransactionID       string           `json:"transaction_id"`
	Source              string           `json:"source"`
	SourceTransactionID string           `json:"source_transaction_id"`
	AccountID           string           `json:"account_id"`
	Institution         string           `json:"institution"`
	ImportedAt          time.Time        `json:"imported_at"`
	LastSeen            time.Time        `json:"last_seen"`
	Transformations     []transformation `json:"transformations"`
}

// transformation mirrors provenance.Transformation: one change in a transaction's
// lineage with a compact human-readable summary.
type transformation struct {
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
	Summary    string    `json:"summary"`
}

// transactionProvenance fetches one transaction's provenance.
func (c *apiClient) transactionProvenance(ctx context.Context, id string) (provenance, error) {
	var out provenance
	if err := c.get(ctx, "/api/v1/transactions/"+url.PathEscape(id)+"/provenance", nil, &out); err != nil {
		return provenance{}, err
	}
	return out, nil
}

// webhook mirrors api.WebhookDTO: a registered delivery endpoint plus the health of
// its most recent delivery. Secret is populated only by create, get, and rotate.
type webhook struct {
	ID            int64      `json:"id"`
	URL           string     `json:"url"`
	EventTypes    []string   `json:"event_types"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Secret        string     `json:"secret"`
}

// plugin mirrors api.PluginDTO (locally so the WASM build doesn't import the
// server packages): an installed plugin's identity, declared hooks/capabilities,
// granted capabilities, enabled/loaded state, and last-run health.
type plugin struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	Runtime       string     `json:"runtime"`
	Version       string     `json:"version"`
	Description   string     `json:"description"`
	Enabled       bool       `json:"enabled"`
	Loaded        bool       `json:"loaded"`
	OnDisk        bool       `json:"on_disk"`
	State         string     `json:"state"`
	Hooks         []string   `json:"hooks"`
	Capabilities  []string   `json:"capabilities"`
	Granted       []string   `json:"granted_capabilities"`
	LastStatus    int64      `json:"last_status"`
	LastError     string     `json:"last_error"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
}

// webhookPayload is the create/update request body (mirrors api.webhookInput).
type webhookPayload struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	Enabled    bool     `json:"enabled"`
}

// webhookTestResult mirrors api.webhookTestResult: the outcome of a test delivery.
type webhookTestResult struct {
	Status    int    `json:"status"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error"`
}

func (c *apiClient) listWebhooks(ctx context.Context) ([]webhook, error) {
	var out struct {
		Webhooks []webhook `json:"webhooks"`
	}
	if err := c.get(ctx, "/api/v1/webhooks", nil, &out); err != nil {
		return nil, err
	}
	return out.Webhooks, nil
}

func (c *apiClient) getWebhook(ctx context.Context, id int64) (webhook, error) {
	var out webhook
	if err := c.get(ctx, "/api/v1/webhooks/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return webhook{}, err
	}
	return out, nil
}

func (c *apiClient) listPlugins(ctx context.Context) ([]plugin, error) {
	var out struct {
		Plugins []plugin `json:"plugins"`
	}
	if err := c.get(ctx, "/api/v1/plugins", nil, &out); err != nil {
		return nil, err
	}
	return out.Plugins, nil
}

func (c *apiClient) enablePlugin(ctx context.Context, id int64) (plugin, error) {
	return c.pluginAction(ctx, id, "enable")
}

func (c *apiClient) disablePlugin(ctx context.Context, id int64) (plugin, error) {
	return c.pluginAction(ctx, id, "disable")
}

func (c *apiClient) reloadPlugin(ctx context.Context, id int64) (plugin, error) {
	return c.pluginAction(ctx, id, "reload")
}

// pluginAction POSTs a plugin lifecycle action (enable/disable/reload) and decodes
// the updated plugin, surfacing the server's error message on failure.
func (c *apiClient) pluginAction(ctx context.Context, id int64, action string) (plugin, error) {
	path := "/api/v1/plugins/" + strconv.FormatInt(id, 10) + "/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return plugin{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return plugin{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return plugin{}, decodeAPIError(resp, action+" plugin")
	}
	var out plugin
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return plugin{}, err
	}
	return out, nil
}

func (c *apiClient) createWebhook(ctx context.Context, p webhookPayload) (webhook, error) {
	return c.sendWebhook(ctx, http.MethodPost, "/api/v1/webhooks", p)
}

func (c *apiClient) updateWebhook(ctx context.Context, id int64, p webhookPayload) (webhook, error) {
	return c.sendWebhook(ctx, http.MethodPut, "/api/v1/webhooks/"+strconv.FormatInt(id, 10), p)
}

// sendWebhook POSTs or PUTs a webhook payload and decodes the returned webhook,
// surfacing the server's error message (e.g. an invalid URL) on failure.
func (c *apiClient) sendWebhook(ctx context.Context, method, path string, p webhookPayload) (webhook, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return webhook{}, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return webhook{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return webhook{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return webhook{}, decodeAPIError(resp, "save webhook")
	}
	var out webhook
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return webhook{}, err
	}
	return out, nil
}

func (c *apiClient) deleteWebhook(ctx context.Context, id int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/v1/webhooks/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp, "delete webhook")
	}
	return nil
}

func (c *apiClient) testWebhook(ctx context.Context, id int64) (webhookTestResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/webhooks/"+strconv.FormatInt(id, 10)+"/test", nil)
	if err != nil {
		return webhookTestResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return webhookTestResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return webhookTestResult{}, decodeAPIError(resp, "test webhook")
	}
	var out webhookTestResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return webhookTestResult{}, err
	}
	return out, nil
}

func (c *apiClient) rotateWebhookSecret(ctx context.Context, id int64) (webhook, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/webhooks/"+strconv.FormatInt(id, 10)+"/rotate-secret", nil)
	if err != nil {
		return webhook{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return webhook{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return webhook{}, decodeAPIError(resp, "rotate webhook secret")
	}
	var out webhook
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return webhook{}, err
	}
	return out, nil
}

// apiKey mirrors api.ApiKeyDTO: a provisioned credential. Key (the full secret) is
// populated only in the create response; lists return metadata only.
type apiKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Key        string     `json:"key"`
}

func (c *apiClient) listApiKeys(ctx context.Context) ([]apiKey, error) {
	var out struct {
		APIKeys []apiKey `json:"api_keys"`
	}
	if err := c.get(ctx, "/api/v1/security/api-keys", nil, &out); err != nil {
		return nil, err
	}
	return out.APIKeys, nil
}

func (c *apiClient) createApiKey(ctx context.Context, name, scope string) (apiKey, error) {
	body, err := json.Marshal(map[string]string{"name": name, "scope": scope})
	if err != nil {
		return apiKey{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/security/api-keys", bytes.NewReader(body))
	if err != nil {
		return apiKey{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return apiKey{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return apiKey{}, decodeAPIError(resp, "create api key")
	}
	var out apiKey
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return apiKey{}, err
	}
	return out, nil
}

func (c *apiClient) revokeApiKey(ctx context.Context, id int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/v1/security/api-keys/"+strconv.FormatInt(id, 10), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decodeAPIError(resp, "revoke api key")
	}
	return nil
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
	Events    eventsConfig    `json:"events"`
	Webhooks  webhooksConfig  `json:"webhooks"`
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
type eventsConfig struct {
	Enabled              bool `json:"enabled"`
	RetentionDays        int  `json:"retention_days"`
	HistoryRetentionDays int  `json:"history_retention_days"`
}
type webhooksConfig struct {
	Enabled     bool   `json:"enabled"`
	Timeout     string `json:"timeout"`
	MaxAttempts int    `json:"max_attempts"`
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
