// Package kasas is the guest-side SDK for writing kasas plugins in Go, compiled
// to WebAssembly. A plugin is an ordinary Go main package that registers hook
// handlers in an init function and is built as a WASI reactor:
//
//	package main
//
//	import kasas "github.com/paulmeier/kasas/pluginsdk/kasas"
//
//	func init() {
//		kasas.OnTransactionCreate(func(t *kasas.Transaction) error {
//			return kasas.ApplyLabels(t.ID, map[string]string{"category": "food"})
//		})
//	}
//
//	func main() {} // required by the build mode, never runs
//
// Build it with the standard Go toolchain (Go 1.24+):
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o main.wasm .
//
// Handlers are registered in init because the module is a reactor: kasas runs the
// module's initializers once at load, then calls exported hooks per event —
// main never runs. Each invocation runs to completion before the next; finish
// your work before returning (goroutines left blocked at return stay frozen
// until a later invocation).
//
// The host API (ApplyLabels, Search, ...) mirrors the Lua/JS `kasas` object and
// is enforced by the same capability grants. The package only functions inside a
// kasas plugin; calling the host API from a normal Go program panics.
package kasas

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// abiVersion is the kasas plugin ABI this SDK implements (checked by the host at
// load). sdkVersion is informational, reported in the describe handshake.
const (
	abiVersion = 1
	sdkVersion = "go/1"
)

// Hook export names; they double as the manifest `hooks` values.
const (
	hookTransactionCreate = "OnTransactionCreate"
	hookTransactionUpdate = "OnTransactionUpdate"
	hookTransactionDelete = "OnTransactionDelete"
	hookSyncComplete      = "OnSyncComplete"
	hookUninstall         = "OnUninstall"
	hookPageRender        = "OnPageRender"
	hookPageAction        = "OnPageAction"
	hookFetch             = "OnFetch"
)

// Transaction is the plugin-facing view of a transaction, identical to what Lua
// and JS plugins receive (kasas's canonical snake_case wire shape). Amount is the
// raw decimal string so cents are never lost to a float.
type Transaction struct {
	ID          string            `json:"id"`
	AccountID   string            `json:"account_id"`
	Amount      string            `json:"amount"`
	Pending     bool              `json:"pending"`
	Date        time.Time         `json:"date"`
	Description string            `json:"description"`
	Payee       string            `json:"payee"`
	Memo        string            `json:"memo"`
	Labels      map[string]string `json:"labels"`
	Extensions  map[string]any    `json:"extensions"`
}

// SyncSummary is the payload handed to an OnSyncComplete handler.
type SyncSummary struct {
	Accounts            int    `json:"accounts"`
	NewTransactions     int    `json:"new_transactions"`
	UpdatedTransactions int    `json:"updated_transactions"`
	AutoLabeled         int    `json:"auto_labeled"`
	Duration            string `json:"duration"`
}

// PageRequest is the payload handed to the page hooks. A render carries no
// Action; an action carries the id of the pressed button or submitted form plus
// its params (form field values arrive as strings, a toggle as "true"/"false").
type PageRequest struct {
	Plugin string            `json:"plugin"`
	Action string            `json:"action,omitempty"`
	Params map[string]string `json:"params,omitempty"`
}

// SourceRequest is the payload handed to an OnFetch handler (a source:provide
// plugin, ADR 0005). Since is the lookback boundary in unix seconds (0 means
// "everything"); Cursor is the opaque resume token returned by the prior fetch
// (empty on the first run).
type SourceRequest struct {
	Since  int64  `json:"since"`
	Cursor string `json:"cursor"`
}

// Batch is what an OnFetch handler returns: the accounts and transactions to
// ingest. The host stamps provenance plugin:<name> and namespaces every id, so the
// Source field below is a human label only and ids need not be globally unique.
type Batch struct {
	Source   string          `json:"source,omitempty"`
	Cursor   string          `json:"cursor,omitempty"`
	Accounts []ImportAccount `json:"accounts"`
}

// ImportOrg is the institution an account belongs to.
type ImportOrg struct {
	ID     string `json:"id,omitempty"`
	Domain string `json:"domain,omitempty"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
}

// ImportAccount is one account in a Batch, with its transactions.
type ImportAccount struct {
	ExternalID   string              `json:"external_id"`
	Org          ImportOrg           `json:"org,omitempty"`
	Name         string              `json:"name,omitempty"`
	Currency     string              `json:"currency,omitempty"`
	Balance      string              `json:"balance,omitempty"`
	BalanceDate  int64               `json:"balance_date,omitempty"`
	Transactions []ImportTransaction `json:"transactions"`
}

// ImportTransaction is one transaction in a Batch. Amount is a decimal string (so
// cents are never lost); Date is unix seconds.
type ImportTransaction struct {
	ExternalID  string `json:"external_id"`
	Amount      string `json:"amount"`
	Date        int64  `json:"date"`
	Description string `json:"description,omitempty"`
	Payee       string `json:"payee,omitempty"`
	Memo        string `json:"memo,omitempty"`
	Pending     bool   `json:"pending,omitempty"`
}

// --- hook registration ---

var handlers = struct {
	txn       map[string]func(*Transaction) error
	sync      func(*SyncSummary) error
	uninstall func() error
	page      map[string]func(*PageRequest) (*Page, error)
	fetch     func(*SourceRequest) (*Batch, error)
}{
	txn:  map[string]func(*Transaction) error{},
	page: map[string]func(*PageRequest) (*Page, error){},
}

// OnTransactionCreate registers the handler for the OnTransactionCreate hook.
// Registering a hook again replaces the previous handler. The manifest must
// declare every hook the plugin registers (and vice versa — a declared hook
// without a registered handler fails the load).
func OnTransactionCreate(fn func(*Transaction) error) { handlers.txn[hookTransactionCreate] = fn }

// OnTransactionUpdate registers the handler for the OnTransactionUpdate hook.
func OnTransactionUpdate(fn func(*Transaction) error) { handlers.txn[hookTransactionUpdate] = fn }

// OnTransactionDelete registers the handler for the OnTransactionDelete hook. The
// handler receives the deleted transaction's last known state (id, labels,
// extensions, ...) — the row itself is already gone, so a follow-up GetTransaction
// for the same id returns nil and host writes against it fail; react from the
// snapshot the handler is given.
func OnTransactionDelete(fn func(*Transaction) error) { handlers.txn[hookTransactionDelete] = fn }

// OnSyncComplete registers the handler for the OnSyncComplete hook.
func OnSyncComplete(fn func(*SyncSummary) error) { handlers.sync = fn }

// OnUninstall registers the cleanup handler run once when the plugin is
// uninstalled, before its files are removed.
func OnUninstall(fn func() error) { handlers.uninstall = fn }

// OnPageRender registers the renderer for the plugin's dashboard page (the
// manifest's [ui] block). It returns the declarative page document to display.
func OnPageRender(fn func(*PageRequest) (*Page, error)) { handlers.page[hookPageRender] = fn }

// OnPageAction registers the handler for dashboard page actions (button presses
// and form submissions declared by a previous render). It returns the refreshed
// page.
func OnPageAction(fn func(*PageRequest) (*Page, error)) { handlers.page[hookPageAction] = fn }

// OnFetch registers the scheduled producer for a source:provide plugin (ADR 0005):
// the host calls it on the sync schedule with the since/cursor, and it returns the
// Batch to ingest. The plugin returns data; the host persists it (dedup, events,
// rules, history) and stamps plugin:<name> provenance — there is no host method
// that writes a row. Requires the source:provide capability and a [source] manifest
// block; pulling a remote provider also needs net:fetch.
func OnFetch(fn func(*SourceRequest) (*Batch, error)) { handlers.fetch = fn }

// registeredHooks lists every hook with a registered handler, sorted for a
// stable describe payload.
func registeredHooks() []string {
	var out []string
	for name, fn := range handlers.txn {
		if fn != nil {
			out = append(out, name)
		}
	}
	if handlers.sync != nil {
		out = append(out, hookSyncComplete)
	}
	if handlers.uninstall != nil {
		out = append(out, hookUninstall)
	}
	for name, fn := range handlers.page {
		if fn != nil {
			out = append(out, name)
		}
	}
	if handlers.fetch != nil {
		out = append(out, hookFetch)
	}
	sort.Strings(out)
	return out
}

// --- invocation plumbing (called from the wasmexport bridges) ---

// resultEnvelope is what every hook invocation hands back to the host through
// the output ABI function. The describe handshake reuses it.
type resultEnvelope struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Page  json.RawMessage `json:"page,omitempty"`
	Batch json.RawMessage `json:"batch,omitempty"` // OnFetch producer result (ADR 0005)
	ABI   int             `json:"abi,omitempty"`
	SDK   string          `json:"sdk,omitempty"`
	Hooks []string        `json:"hooks,omitempty"`
}

func emit(env resultEnvelope) {
	b, err := json.Marshal(env)
	if err != nil {
		b = []byte(`{"ok":false,"error":"kasas sdk: failed to encode result envelope"}`)
	}
	rawOutput(b)
}

// runHook executes one handler under the SDK safety envelope: a handler panic is
// recovered into an error envelope, so the module instance survives plugin bugs
// (a panic that escaped to the Go runtime would exit and reset the whole module).
// assign places the handler's raw result into the right envelope field (page vs
// batch); a nil assign is for hooks that return no value (transaction/sync/uninstall).
func runHook(fn func() (json.RawMessage, error), assign func(*resultEnvelope, json.RawMessage)) {
	var env resultEnvelope
	func() {
		defer func() {
			if r := recover(); r != nil {
				env = resultEnvelope{Error: fmt.Sprintf("panic: %v", r)}
			}
		}()
		raw, err := fn()
		if err != nil {
			env = resultEnvelope{Error: err.Error()}
			return
		}
		env = resultEnvelope{OK: true}
		if assign != nil {
			assign(&env, raw)
		}
	}()
	emit(env)
}

// envPage and envBatch route a hook result into the matching envelope field.
func envPage(e *resultEnvelope, raw json.RawMessage)  { e.Page = raw }
func envBatch(e *resultEnvelope, raw json.RawMessage) { e.Batch = raw }

func dispatchDescribe() {
	emit(resultEnvelope{OK: true, ABI: abiVersion, SDK: sdkVersion, Hooks: registeredHooks()})
}

func dispatchTransaction(name string, payloadLen uint32) {
	runHook(func() (json.RawMessage, error) {
		fn := handlers.txn[name]
		if fn == nil {
			return nil, fmt.Errorf("no handler registered for %s", name)
		}
		var t *Transaction
		if err := readJSONInput(payloadLen, &t); err != nil {
			return nil, err
		}
		return nil, fn(t)
	}, nil)
}

func dispatchSync(payloadLen uint32) {
	runHook(func() (json.RawMessage, error) {
		if handlers.sync == nil {
			return nil, fmt.Errorf("no handler registered for %s", hookSyncComplete)
		}
		var s *SyncSummary
		if err := readJSONInput(payloadLen, &s); err != nil {
			return nil, err
		}
		if s == nil {
			s = &SyncSummary{}
		}
		return nil, handlers.sync(s)
	}, nil)
}

func dispatchUninstall() {
	runHook(func() (json.RawMessage, error) {
		if handlers.uninstall == nil {
			return nil, fmt.Errorf("no handler registered for %s", hookUninstall)
		}
		return nil, handlers.uninstall()
	}, nil)
}

func dispatchPage(name string, payloadLen uint32) {
	runHook(func() (json.RawMessage, error) {
		fn := handlers.page[name]
		if fn == nil {
			return nil, fmt.Errorf("no handler registered for %s", name)
		}
		var req PageRequest
		if err := readJSONInput(payloadLen, &req); err != nil {
			return nil, err
		}
		page, err := fn(&req)
		if err != nil {
			return nil, err
		}
		if page == nil {
			return nil, errors.New("page handler returned a nil page")
		}
		raw, err := json.Marshal(page)
		if err != nil {
			return nil, fmt.Errorf("encode page: %w", err)
		}
		return raw, nil
	}, envPage)
}

func dispatchFetch(payloadLen uint32) {
	runHook(func() (json.RawMessage, error) {
		if handlers.fetch == nil {
			return nil, fmt.Errorf("no handler registered for %s", hookFetch)
		}
		var req SourceRequest
		if err := readJSONInput(payloadLen, &req); err != nil {
			return nil, err
		}
		batch, err := handlers.fetch(&req)
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return nil, errors.New("fetch handler returned a nil batch")
		}
		raw, err := json.Marshal(batch)
		if err != nil {
			return nil, fmt.Errorf("encode batch: %w", err)
		}
		return raw, nil
	}, envBatch)
}

func readJSONInput(payloadLen uint32, v any) error {
	b := rawInput(payloadLen)
	if len(b) == 0 {
		b = []byte("null")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode hook payload: %w", err)
	}
	return nil
}

// --- host API ---

// hostRequest is the host_call request envelope (one flat op-dispatched shape,
// mirroring the host side).
type hostRequest struct {
	Op      string            `json:"op"`
	ID      string            `json:"id,omitempty"`
	Query   string            `json:"query,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Keys    []string          `json:"keys,omitempty"`
	Key     string            `json:"key,omitempty"`
	Value   json.RawMessage   `json:"value,omitempty"`
	Level   string            `json:"level,omitempty"`
	Msg     string            `json:"msg,omitempty"`
	KV      map[string]any    `json:"kv,omitempty"`
	Changes map[string]any    `json:"changes,omitempty"`
	// net:fetch fields (op "fetch").
	URL       string            `json:"url,omitempty"`
	Method    string            `json:"method,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

type hostResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

func callHost(req hostRequest) (json.RawMessage, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("kasas: encode host request: %w", err)
	}
	raw := rawHostCall(b)
	if len(raw) == 0 {
		return nil, errors.New("kasas: empty host response")
	}
	var resp hostResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("kasas: decode host response: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unknown host error"
		}
		return nil, errors.New(resp.Error)
	}
	return resp.Data, nil
}

// GetTransaction fetches one transaction by id, or nil when it does not exist.
// Requires the transactions:read capability.
func GetTransaction(id string) (*Transaction, error) {
	data, err := callHost(hostRequest{Op: "get_transaction", ID: id})
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var t Transaction
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("kasas: decode transaction: %w", err)
	}
	return &t, nil
}

// Search evaluates a kasas search query (the same language as the dashboard and
// /transactions/search) and returns up to limit matches (limit <= 0 means the
// server cap). Requires the transactions:read capability.
func Search(query string, limit int) ([]Transaction, error) {
	data, err := callHost(hostRequest{Op: "search", Query: query, Limit: limit})
	if err != nil {
		return nil, err
	}
	var out []Transaction
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil {
			return nil, fmt.Errorf("kasas: decode search results: %w", err)
		}
	}
	return out, nil
}

// ApplyLabels merges labels into a transaction's label set. Requires the
// labels:write capability.
func ApplyLabels(txnID string, labels map[string]string) error {
	_, err := callHost(hostRequest{Op: "apply_labels", ID: txnID, Labels: labels})
	return err
}

// RemoveLabels drops the given label keys from a transaction. Requires the
// labels:write capability.
func RemoveLabels(txnID string, keys []string) error {
	_, err := callHost(hostRequest{Op: "remove_labels", ID: txnID, Keys: keys})
	return err
}

// SetExtension sets one namespaced schema-extension key on a transaction to any
// JSON-serializable value. Requires the extensions:write capability.
func SetExtension(txnID, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("kasas: encode extension value: %w", err)
	}
	_, err = callHost(hostRequest{Op: "set_extension", ID: txnID, Key: key, Value: raw})
	return err
}

// RemoveExtension drops one schema-extension key from a transaction. Requires
// the extensions:write capability.
func RemoveExtension(txnID, key string) error {
	_, err := callHost(hostRequest{Op: "remove_extension", ID: txnID, Key: key})
	return err
}

// FetchRequest is an outbound HTTP request for Fetch. TimeoutMS may only SHORTEN
// the host's configured per-request timeout, never exceed it.
type FetchRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method,omitempty"` // default GET
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body,omitempty"`
	TimeoutMS int               `json:"timeout_ms,omitempty"`
}

// FetchResponse is the host's reply to Fetch. Body is the response body up to the
// host's size cap; Truncated reports that the body was cut at the cap.
type FetchResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      string            `json:"body"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Fetch performs a host-mediated outbound HTTP request. Requires the net:fetch
// capability AND that the request URL's host is declared in the manifest's
// [net].allow list — the host resolves the URL against that allowlist, applies the
// SSRF rule, and enforces the operator's timeout/size/rate/redirect caps. The
// plugin never opens a socket; this is the only sanctioned egress (ADR 0002).
func Fetch(req FetchRequest) (*FetchResponse, error) {
	data, err := callHost(hostRequest{
		Op: "fetch", URL: req.URL, Method: req.Method, Headers: req.Headers,
		Body: req.Body, TimeoutMS: req.TimeoutMS,
	})
	if err != nil {
		return nil, err
	}
	var resp FetchResponse
	if len(data) > 0 {
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("kasas: decode fetch response: %w", err)
		}
	}
	return &resp, nil
}

// Log writes a structured log line attributed to the plugin. level is debug,
// info, warn, or error (anything else logs as info). Always allowed. Plain
// fmt.Println output is also captured into the plugin log at info level (and
// stderr at error level), but Log keeps key/value structure.
func Log(level, msg string, kv map[string]any) {
	_, _ = callHost(hostRequest{Op: "log", Level: level, Msg: msg, KV: kv})
}

// config caches the effective config (manifest defaults overlaid with the
// operator's overrides). Invocations are serialized, so no locking is needed.
var config map[string]any

// Config returns the plugin's effective config. The first call fetches it from
// the host; a fetch failure returns an empty map (and is retried on the next
// call). Mutating the returned map does not persist anything — use SetConfig.
func Config() map[string]any {
	if config == nil {
		data, err := callHost(hostRequest{Op: "get_config"})
		if err != nil {
			return map[string]any{}
		}
		var m map[string]any
		if json.Unmarshal(data, &m) == nil && m != nil {
			config = m
		} else {
			config = map[string]any{}
		}
	}
	return config
}

// ConfigString returns one config value rendered as a string ("" when absent),
// converting numbers and booleans, so `keyword = "coffee"` and `limit = 10` are
// equally easy to consume.
func ConfigString(key string) string {
	switch v := Config()[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

// SetConfig persists config overrides (each key must have a default in the
// manifest's [config] block) and returns the new effective config, which also
// becomes what Config reports. Always allowed — a plugin only configures itself.
func SetConfig(changes map[string]any) (map[string]any, error) {
	data, err := callHost(hostRequest{Op: "set_config", Changes: changes})
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(data, &merged); err != nil {
		return nil, fmt.Errorf("kasas: decode merged config: %w", err)
	}
	config = merged
	return merged, nil
}
