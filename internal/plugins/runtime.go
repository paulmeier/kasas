package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

// Runtime loads a plugin's code into a ready-to-invoke Instance. There is one
// Runtime per language, selected by Manifest.Runtime. Load is called once per
// plugin at startup/reload on the plugin's own goroutine, so a Runtime need not be
// safe for concurrent loads of the same plugin.
//
// This two-method interface is the seam that keeps the manager free of any VM
// dependency: adding JS (goja) or Go (wazero/WASM) later is a new file that
// implements Runtime/Instance — the manager, host facade, and capability model are
// untouched.
type Runtime interface {
	// Name is the manifest `runtime` value this implements ("lua", later "js"/"wasm").
	Name() string
	// Load compiles/initializes the plugin in dir (Manifest.Entrypoint resolves
	// within it) and binds host as its only path back into kasas. It must error if a
	// declared hook is not actually implemented, so the manifest's hook list stays
	// authoritative.
	Load(ctx context.Context, m Manifest, dir string, host Host) (Instance, error)
}

// Instance is one loaded plugin. The manager invokes it on exactly ONE goroutine
// (the plugin's worker), so an implementation need not be safe for concurrent
// Invoke calls — which is what makes a non-reentrant VM (gopher-lua's *lua.LState)
// safe to reuse across invocations.
type Instance interface {
	// Invoke runs the plugin's handler for hook with ev. It must honor ctx for the
	// per-hook timeout and must not panic out (it recovers internally and returns an
	// error). It returns ErrHookNotImpl if the plugin has no handler for hook.
	Invoke(ctx context.Context, hook Hook, ev HookEvent) error
	// Render runs a value-returning page hook (HookPageRender / HookPageAction)
	// with req and returns the page document the handler produced, as raw JSON.
	// The result is UNTRUSTED until ValidatePageDoc accepts it. Same contract as
	// Invoke otherwise: honor ctx, never panic out, ErrHookNotImpl when absent.
	Render(ctx context.Context, hook Hook, req PageRequest) (json.RawMessage, error)
	// Close releases the VM and any resources. It is called once.
	Close() error
}

var (
	// ErrHookNotImpl is returned by Invoke when no handler exists for a hook. The
	// loader rejects a plugin that declares a hook it does not implement, so this is
	// a defensive guard, not the normal path.
	ErrHookNotImpl = errors.New("plugins: hook handler not implemented")
	// ErrCapabilityDenied is returned by a host method the plugin's grant does not
	// cover. The DB is never touched when it is returned.
	ErrCapabilityDenied = errors.New("plugins: capability not granted")
	// ErrTxnNotFound is returned by host reads/writes for an unknown transaction id.
	ErrTxnNotFound = errors.New("plugins: transaction not found")
)

// Transaction is the plugin-facing view of a transaction, decoupled from
// db.Transaction so the host API stays stable across schema changes. The same
// shape is used both for a hook's triggering transaction and for Host.Search
// results. Amount is the raw decimal string (never a float) so cents are never
// lost; Extensions are decoded values (string/number/bool/...) for natural use in
// a script.
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

// SyncSummary is the payload handed to the OnSyncComplete hook.
type SyncSummary struct {
	Accounts            int    `json:"accounts"`
	NewTransactions     int    `json:"new_transactions"`
	UpdatedTransactions int    `json:"updated_transactions"`
	AutoLabeled         int    `json:"auto_labeled"`
	Duration            string `json:"duration"`
}

// HookEvent is the decoded event delivered to a plugin hook. The manager decodes
// each bus event into this once before fanning it out, so the runtimes share one
// representation. Transaction is set for the transaction hooks (nil for sync);
// Sync is set for OnSyncComplete (nil otherwise).
type HookEvent struct {
	Type        string       `json:"type"`
	Sequence    int64        `json:"sequence"`
	EntityID    string       `json:"entity_id"`
	OccurredAt  time.Time    `json:"occurred_at"`
	Transaction *Transaction `json:"transaction,omitempty"`
	Sync        *SyncSummary `json:"sync,omitempty"`
}

// Host is the kasas surface a plugin instance may call. Every method is
// capability-gated by the implementation (host.go); a call lacking the required
// grant returns ErrCapabilityDenied without touching the DB. All writes route
// through the event emitter, so a plugin's label/extension edits produce the same
// label.*/extension.* events and history as a REST or rules-engine edit.
type Host interface {
	GetTransaction(ctx context.Context, id string) (*Transaction, error)              // transactions:read
	Search(ctx context.Context, query string, limit int) ([]Transaction, error)       // transactions:read
	ApplyLabels(ctx context.Context, txnID string, labels map[string]string) error    // labels:write (merge)
	RemoveLabels(ctx context.Context, txnID string, keys []string) error              // labels:write
	SetExtension(ctx context.Context, txnID, key string, value json.RawMessage) error // extensions:write
	RemoveExtension(ctx context.Context, txnID, key string) error                     // extensions:write
	Log(level, msg string, kv map[string]any)                                         // always allowed
	// Fetch performs a host-mediated outbound HTTP request (ADR 0002). net:fetch.
	// The host checks the capability, resolves the URL host against the manifest's
	// [net].allow list, applies the SSRF rule, and enforces timeout/size/rate/
	// redirect caps — a plugin never opens a socket itself.
	Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error) // net:fetch
	// SetConfig validates changes against the manifest's [config] defaults (the
	// schema of what is configurable), persists them to the plugin's user config
	// file (<plugins.dir>/<name>.config.toml), and returns the new effective
	// config. Always allowed: a plugin can only configure ITSELF, so no
	// capability gates it (like Log).
	SetConfig(ctx context.Context, changes map[string]any) (map[string]any, error)
}

// capSet is a plugin's granted capability set (the intersection of what the
// manifest requested and what the operator/DB granted).
type capSet map[Capability]bool

func newCapSet(caps []Capability) capSet {
	s := make(capSet, len(caps))
	for _, c := range caps {
		s[c] = true
	}
	return s
}

func (c capSet) has(cap Capability) bool { return c[cap] }

// intersectCaps returns the capabilities present in both requested (manifest) and
// granted (DB). The DB grant is authoritative, so a plugin never runs with more
// than was granted even if its manifest asks for more.
func intersectCaps(requested, granted []Capability) capSet {
	g := newCapSet(granted)
	out := make(capSet)
	for _, c := range requested {
		if g[c] {
			out[c] = true
		}
	}
	return out
}

// encodeCapList marshals a capability list to the JSON array stored in the DB,
// sorted so the stored form is stable (cheap change-detection on reconcile).
func encodeCapList(caps []Capability) string {
	ss := make([]string, len(caps))
	for i, c := range caps {
		ss[i] = string(c)
	}
	sort.Strings(ss)
	b, err := json.Marshal(ss)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeCapList parses the DB's JSON capability array.
func decodeCapList(stored string) []Capability {
	if stored == "" {
		return nil
	}
	var ss []string
	if err := json.Unmarshal([]byte(stored), &ss); err != nil {
		return nil
	}
	out := make([]Capability, 0, len(ss))
	for _, s := range ss {
		out = append(out, Capability(s))
	}
	return out
}

// encodeStringList marshals a string list to the JSON array stored in the DB
// (e.g. plugins.net_grants), sorted so the stored form is stable. A nil/empty list
// stores "[]".
func encodeStringList(ss []string) string {
	sorted := make([]string, len(ss))
	copy(sorted, ss)
	sort.Strings(sorted)
	b, err := json.Marshal(sorted)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeStringList parses a DB JSON string array (e.g. plugins.net_grants).
func decodeStringList(stored string) []string {
	if stored == "" {
		return nil
	}
	var ss []string
	if err := json.Unmarshal([]byte(stored), &ss); err != nil {
		return nil
	}
	return ss
}
