package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/extensions"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/relationships"
	"github.com/paulmeier/kasas/internal/search"
)

// hostFacade is the capability-checked implementation of Host bound to one
// plugin's granted capability set. Centralizing the capability check here means
// every runtime inherits enforcement for free, and every write goes through the
// shared emitter seam so it emits the same events + history as a REST or
// rules-engine edit.
type hostFacade struct {
	store       db.Store
	emitter     *events.Emitter
	caps        capSet
	name        string
	searchLimit int
	logger      *slog.Logger
	// cfg backs SetConfig: where the plugin's user config file lives and the
	// manifest defaults that act as its schema. Nil when no plugins directory is
	// configured, making SetConfig a clean error instead of writing somewhere odd.
	cfg *configStore
	// net is the plugin's egress gate (ADR 0002). Nil unless the plugin was granted
	// net:fetch and declared a [net].allow list, so Fetch is a clean error otherwise.
	net *netGate
}

// configStore is the per-plugin user-config persistence handed to a host.
// mu serializes read-modify-write cycles on the override file: the plugin's own
// worker is single-goroutine, but an uninstall-hook instance or a reload can
// briefly coexist with it.
type configStore struct {
	dir      string
	name     string
	defaults map[string]any
	mu       sync.Mutex
}

func newConfigStore(dir, name string, defaults map[string]any) *configStore {
	if dir == "" {
		return nil
	}
	return &configStore{dir: dir, name: name, defaults: defaults}
}

func newHost(store db.Store, emitter *events.Emitter, caps capSet, name string, searchLimit int, logger *slog.Logger, cfg *configStore, net *netGate) *hostFacade {
	if searchLimit <= 0 {
		searchLimit = defaultSearchLimit
	}
	return &hostFacade{store: store, emitter: emitter, caps: caps, name: name, searchLimit: searchLimit, logger: logger, cfg: cfg, net: net}
}

const defaultSearchLimit = 1000

var _ Host = (*hostFacade)(nil)

// GetTransaction returns the plugin-facing view of one transaction. transactions:read.
func (h *hostFacade) GetTransaction(ctx context.Context, id string) (*Transaction, error) {
	if !h.caps.has(CapTransactionsRead) {
		return nil, ErrCapabilityDenied
	}
	t, err := h.store.GetTransaction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTxnNotFound
	}
	if err != nil {
		return nil, err
	}
	tx := pluginTxnFromDB(t)
	return &tx, nil
}

// Search evaluates a kasas search query over every transaction and returns up to
// limit matches (capped server-side). transactions:read. It filters in Go, like
// the REST/MCP search, so it supports the full query language.
func (h *hostFacade) Search(ctx context.Context, q string, limit int) ([]Transaction, error) {
	if !h.caps.has(CapTransactionsRead) {
		return nil, ErrCapabilityDenied
	}
	query, err := search.Parse(q)
	if err != nil {
		return nil, fmt.Errorf("invalid query: %w", err)
	}
	if limit <= 0 || limit > h.searchLimit {
		limit = h.searchLimit
	}

	accounts, err := h.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(accounts))
	for _, a := range accounts {
		names[a.ID] = a.Name
	}

	const batch = 1000
	out := make([]Transaction, 0)
	for offset := int64(0); ; offset += batch {
		rows, err := h.store.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: batch, RowOffset: offset})
		if err != nil {
			return nil, err
		}
		for _, t := range rows {
			if query.Match(searchRecordFromTxn(t, names[t.AccountID])) {
				out = append(out, pluginTxnFromDB(t))
				if len(out) >= limit {
					return out, nil
				}
			}
		}
		if int64(len(rows)) < batch {
			break
		}
	}
	return out, nil
}

// ApplyLabels merges labels into a transaction (overlay over the existing set),
// normalizing the result. labels:write. The write, the per-key label.* diff, and
// the labeled history version all happen in one transaction via the emitter.
func (h *hostFacade) ApplyLabels(ctx context.Context, txnID string, in map[string]string) error {
	if !h.caps.has(CapLabelsWrite) {
		return ErrCapabilityDenied
	}
	add := labels.Normalize(in)
	if len(add) == 0 {
		return nil
	}
	return h.emitter.Record(ctx, h.store, func(q db.Querier, rec *events.Recorder) error {
		t, err := q.GetTransaction(ctx, txnID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTxnNotFound
		}
		if err != nil {
			return err
		}
		old := labels.Decode(t.Labels)
		merged := make(map[string]string, len(old)+len(add))
		for k, v := range old {
			merged[k] = v
		}
		for k, v := range add {
			merged[k] = v
		}
		merged = labels.Normalize(merged)
		return h.writeLabels(ctx, q, rec, t, old, merged)
	})
}

// RemoveLabels drops the given keys from a transaction's label set. labels:write.
func (h *hostFacade) RemoveLabels(ctx context.Context, txnID string, keys []string) error {
	if !h.caps.has(CapLabelsWrite) {
		return ErrCapabilityDenied
	}
	if len(keys) == 0 {
		return nil
	}
	return h.emitter.Record(ctx, h.store, func(q db.Querier, rec *events.Recorder) error {
		t, err := q.GetTransaction(ctx, txnID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTxnNotFound
		}
		if err != nil {
			return err
		}
		old := labels.Decode(t.Labels)
		merged := make(map[string]string, len(old))
		for k, v := range old {
			merged[k] = v
		}
		for _, k := range keys {
			delete(merged, labels.NormalizeKey(k))
		}
		return h.writeLabels(ctx, q, rec, t, old, merged)
	})
}

// writeLabels persists merged labels and emits the diff + a labeled version. It
// is a no-op (no write, no event) when nothing changed.
func (h *hostFacade) writeLabels(ctx context.Context, q db.Querier, rec *events.Recorder, t db.Transaction, old, merged map[string]string) error {
	encoded, err := labels.Encode(merged)
	if err != nil {
		return err
	}
	if encoded == t.Labels {
		return nil
	}
	if _, err := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: t.ID, Labels: encoded}); err != nil {
		return err
	}
	if err := rec.EmitLabelDiff(ctx, q, t.ID, old, merged); err != nil {
		return err
	}
	next := t
	next.Labels = encoded
	return rec.VersionChange(ctx, q, t.ID, events.TransactionSnapshot(t), events.TransactionSnapshot(next), events.ChangeLabeled)
}

// SetExtension sets a single namespaced extension key to a JSON value, leaving
// other keys untouched (so plugins don't clobber each other). extensions:write.
func (h *hostFacade) SetExtension(ctx context.Context, txnID, key string, value json.RawMessage) error {
	if !h.caps.has(CapExtensionsWrite) {
		return ErrCapabilityDenied
	}
	nk := extensions.NormalizeKey(key)
	if nk == "" {
		return fmt.Errorf("invalid extension key %q", key)
	}
	if !json.Valid(value) {
		return fmt.Errorf("invalid JSON value for extension %q", key)
	}
	return h.emitter.Record(ctx, h.store, func(q db.Querier, rec *events.Recorder) error {
		t, err := q.GetTransaction(ctx, txnID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTxnNotFound
		}
		if err != nil {
			return err
		}
		old := extensions.Decode(t.Extensions)
		merged := make(map[string]json.RawMessage, len(old)+1)
		for k, v := range old {
			merged[k] = v
		}
		merged[nk] = value
		return h.writeExtensions(ctx, q, rec, t, old, extensions.Normalize(merged))
	})
}

// RemoveExtension drops a single extension key. extensions:write.
func (h *hostFacade) RemoveExtension(ctx context.Context, txnID, key string) error {
	if !h.caps.has(CapExtensionsWrite) {
		return ErrCapabilityDenied
	}
	nk := extensions.NormalizeKey(key)
	return h.emitter.Record(ctx, h.store, func(q db.Querier, rec *events.Recorder) error {
		t, err := q.GetTransaction(ctx, txnID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTxnNotFound
		}
		if err != nil {
			return err
		}
		old := extensions.Decode(t.Extensions)
		merged := make(map[string]json.RawMessage, len(old))
		for k, v := range old {
			merged[k] = v
		}
		delete(merged, nk)
		return h.writeExtensions(ctx, q, rec, t, old, extensions.Normalize(merged))
	})
}

// writeExtensions persists the new extension set and emits the diff + an extended
// version. No-op when nothing changed.
func (h *hostFacade) writeExtensions(ctx context.Context, q db.Querier, rec *events.Recorder, t db.Transaction, old, merged map[string]json.RawMessage) error {
	encoded, err := extensions.Encode(merged)
	if err != nil {
		return err
	}
	if encoded == t.Extensions {
		return nil
	}
	if _, err := q.UpdateTransactionExtensions(ctx, db.UpdateTransactionExtensionsParams{ID: t.ID, Extensions: encoded}); err != nil {
		return err
	}
	if err := rec.EmitExtensionDiff(ctx, q, t.ID, old, merged); err != nil {
		return err
	}
	next := t
	next.Extensions = encoded
	return rec.VersionChange(ctx, q, t.ID, events.TransactionSnapshot(t), events.TransactionSnapshot(next), events.ChangeExtended)
}

// SetConfig merges changes into the plugin's persisted config overrides and
// OVERWRITES the user config file, returning the new effective config (manifest
// defaults + all overrides). Each key must have a default in the manifest's
// [config] block, and each value is coerced to that default's scalar type — so
// string params from a dashboard form land typed. Always allowed (a plugin only
// configures itself); the write never touches the DB or emits events, it is
// plain operator state on disk.
func (h *hostFacade) SetConfig(_ context.Context, changes map[string]any) (map[string]any, error) {
	if h.cfg == nil {
		return nil, fmt.Errorf("plugins: no plugins directory configured, config cannot be persisted")
	}
	h.cfg.mu.Lock()
	defer h.cfg.mu.Unlock()

	overrides, err := loadUserOverrides(h.cfg.dir, h.cfg.name)
	if err != nil {
		return nil, err
	}
	for k, v := range changes {
		def, ok := h.cfg.defaults[k]
		if !ok {
			return nil, fmt.Errorf("unknown config key %q (configurable keys need a default in the manifest's [config] block)", k)
		}
		cv, cerr := coerceConfigValue(k, def, v)
		if cerr != nil {
			return nil, cerr
		}
		overrides[k] = cv
	}
	merged, err := mergeConfig(h.cfg.defaults, overrides)
	if err != nil {
		return nil, err
	}
	if len(changes) > 0 {
		if err := saveUserOverrides(h.cfg.dir, h.cfg.name, overrides); err != nil {
			return nil, fmt.Errorf("save plugin config: %w", err)
		}
	}
	return merged, nil
}

// Log writes a structured log line attributed to the plugin. Always allowed.
func (h *hostFacade) Log(level, msg string, kv map[string]any) {
	args := make([]any, 0, len(kv)*2+2)
	args = append(args, "plugin", h.name)
	for k, v := range kv {
		args = append(args, k, v)
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		h.logger.Debug(msg, args...)
	case "warn", "warning":
		h.logger.Warn(msg, args...)
	case "error":
		h.logger.Error(msg, args...)
	default:
		h.logger.Info(msg, args...)
	}
}

// Fetch performs a host-mediated outbound HTTP request. net:fetch. The capability
// is checked here, the single facade chokepoint; the actual request — allowlist
// resolution, the SSRF rule, DNS pinning, redirect re-validation, and the
// timeout/size/rate caps — is performed by the plugin's egress gate, exactly as a
// label write routes through the emitter. A plugin without the gate (no [net]
// block) gets a clean error rather than an open socket.
func (h *hostFacade) Fetch(ctx context.Context, req FetchRequest) (FetchResponse, error) {
	if !h.caps.has(CapNetFetch) {
		return FetchResponse{}, ErrCapabilityDenied
	}
	if h.net == nil {
		return FetchResponse{}, ErrNetUnconfigured
	}
	return h.net.do(ctx, req)
}

// --- adapters (kept local so internal/search stays free of db/labels deps) ---

func pluginTxnFromDB(t db.Transaction) Transaction {
	return Transaction{
		ID:          t.ID,
		AccountID:   t.AccountID,
		Amount:      t.Amount,
		Pending:     t.Pending != 0,
		Date:        time.Unix(t.Date, 0).UTC(),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      labels.Decode(t.Labels),
		Extensions:  extensions.Values(t.Extensions),
	}
}

func searchRecordFromTxn(t db.Transaction, accountName string) search.Record {
	return search.Record{
		ID:          t.ID,
		AccountID:   t.AccountID,
		AccountName: accountName,
		Amount:      parseAmount(t.Amount),
		AmountRaw:   t.Amount,
		Pending:     t.Pending != 0,
		Date:        time.Unix(t.Date, 0).UTC(),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      labels.Decode(t.Labels),
		Extensions:  searchExtensions(t.Extensions),
		// Only the transaction's OWN outbound edges; the inbound side would require
		// scanning every transaction, which this single-row builder does not do. So
		// rel:<kind> works for plugins, while related:<id> sees only this
		// transaction's own targets.
		Relationships: searchRelationshipsOutbound(t.Relationships),
		SyncedAt:      time.Unix(t.SyncedAt, 0).UTC(),
	}
}

// searchRelationshipsOutbound builds the OUTBOUND relationship edges of a
// transaction for the search matcher (see the note at the call site about inbound).
func searchRelationshipsOutbound(stored string) []search.Relationship {
	rels := relationships.Decode(stored)
	out := make([]search.Relationship, 0, len(rels))
	for _, e := range rels {
		out = append(out, search.Relationship{Kind: e.Kind, Target: e.Target, Direction: relationships.DirectionOutbound})
	}
	return out
}

func searchExtensions(stored string) map[string]string {
	raw := extensions.Decode(stored)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		out[strings.ToLower(k)] = extensions.StringifyValue(v)
	}
	return out
}

func parseAmount(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}
