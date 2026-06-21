package poller

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/paulmeier/kasas/internal/source"
)

// SourceStatus summarizes one ingestion source for the API and dashboard: its
// self-describing metadata plus whether it is ready to sync and how its credential
// (if any) is set.
type SourceStatus struct {
	Type            string `json:"type"`
	Archetype       string `json:"archetype"`
	Title           string `json:"title"`
	Connected       bool   `json:"connected"`        // ready to sync (no credential needed, or one is stored)
	Credentialed    bool   `json:"credentialed"`     // accepts a pasted credential
	MultiCredential bool   `json:"multi_credential"` // holds several credentials (add/remove individually)
	OAuth           bool   `json:"oauth"`            // supports the browser OAuth connect flow
	// Egress lists the external hosts this source contacts, surfaced so its network
	// reach is visible to the operator (ADR 0006). Empty for most sources.
	Egress      []string                 `json:"egress,omitempty"`
	Credentials []source.CredentialField `json:"credentials,omitempty"`
	// CredentialEntries lists the masked, individually-removable credentials of a
	// multi-credential source (e.g. each Teller bank enrollment). Empty otherwise.
	CredentialEntries []source.CredentialEntry `json:"credential_entries,omitempty"`
}

// Engine coordinates one Poller per configured ingestion source. It is the seam
// the API talks to for multi-source operations — listing sources, per-source sync,
// and per-source credential/OAuth management — while each Poller still owns its
// own schedule and the shared transactional persist path. Syncing with no type
// (Sync) runs every source, satisfying the api.Syncer the single-source poller
// used to provide.
//
// Sources can be added and removed at runtime (AddPoller/RemovePoller) — a plugin
// that provides a source (ADR 0005) registers its poller on enable and removes it
// on disable/uninstall. The mutex guards the pollers map and order slice only;
// every operation snapshots the relevant poller(s) under the lock and releases it
// before doing work, so a slow sync never blocks a listing or another source (and
// SQLite's single-writer serialization is handled by each Poller's own mutex).
type Engine struct {
	mu      sync.RWMutex
	pollers map[string]*Poller // keyed by source type (descriptor Type)
	order   []string           // registration order, for stable iteration
}

// NewEngine builds an Engine over the given pollers, keyed by each source's type.
func NewEngine(pollers ...*Poller) *Engine {
	e := &Engine{pollers: make(map[string]*Poller, len(pollers))}
	for _, p := range pollers {
		typ := p.source.Descriptor().Type
		if _, dup := e.pollers[typ]; dup {
			continue // ignore a duplicate type; the first registered wins
		}
		e.pollers[typ] = p
		e.order = append(e.order, typ)
	}
	return e
}

// snapshot returns the pollers in registration order, copied under the read lock
// so callers can iterate (and call slow methods like Sync) without holding it.
func (e *Engine) snapshot() []*Poller {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]*Poller, 0, len(e.order))
	for _, typ := range e.order {
		out = append(out, e.pollers[typ])
	}
	return out
}

// get returns the poller for a type under the read lock.
func (e *Engine) get(typ string) (*Poller, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p, ok := e.pollers[typ]
	return p, ok
}

// AddPoller registers a poller at runtime and starts its schedule. If a poller of
// the same source type is already registered it is replaced (its schedule stopped),
// keeping its position in the iteration order — this supports a plugin source being
// reloaded. The schedule is started outside the lock so spinning up a scheduler
// never blocks an in-flight Sync or listing.
func (e *Engine) AddPoller(ctx context.Context, p *Poller) error {
	typ := p.source.Descriptor().Type
	e.mu.Lock()
	old, existed := e.pollers[typ]
	e.pollers[typ] = p
	if !existed {
		e.order = append(e.order, typ)
	}
	e.mu.Unlock()

	if existed && old != nil {
		_ = old.Stop(ctx) // best-effort: release the replaced poller's scheduler
	}
	return p.Start(ctx)
}

// RemovePoller deregisters a source by type and stops its schedule. Removing an
// unknown type is an error. The schedule is stopped outside the lock.
func (e *Engine) RemovePoller(ctx context.Context, typ string) error {
	e.mu.Lock()
	p, ok := e.pollers[typ]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("unknown source %q", typ)
	}
	delete(e.pollers, typ)
	for i, t := range e.order {
		if t == typ {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	e.mu.Unlock()

	return p.Stop(ctx)
}

// Start schedules every source's recurring sync.
func (e *Engine) Start(ctx context.Context) error {
	for _, p := range e.snapshot() {
		if err := p.Start(ctx); err != nil {
			return fmt.Errorf("start %s poller: %w", p.source.Descriptor().Type, err)
		}
	}
	return nil
}

// Stop halts every source's scheduler.
func (e *Engine) Stop(ctx context.Context) error {
	var errs []error
	for _, p := range e.snapshot() {
		if err := p.Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sync runs every source sequentially (SQLite has a single writer) and returns the
// aggregate result. It satisfies api.Syncer, so the global /sync trigger and the
// trigger_sync MCP tool sync all sources — except on-demand cache sources (the
// market read-through cache), which warm on access, not on a bulk sync, so a
// "Sync all" never eagerly pulls data nothing is displaying. A source that fails is
// recorded; the others still run, and the joined error is returned (nil when all
// succeed).
func (e *Engine) Sync(ctx context.Context) (SyncResult, error) {
	var agg SyncResult
	var errs []error
	for _, p := range e.snapshot() {
		if p.onDemandCache() {
			continue // read-through cache: warmed on access or via an explicit per-source sync
		}
		res, err := p.Sync(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.source.Descriptor().Type, err))
			continue
		}
		agg.Accounts += res.Accounts
		agg.NewTransactions += res.NewTransactions
		agg.UpdatedTransactions += res.UpdatedTransactions
		agg.AutoLabeled += res.AutoLabeled
		agg.Duration += res.Duration
	}
	if len(errs) > 0 {
		return agg, errors.Join(errs...)
	}
	return agg, nil
}

// SyncSource runs a single source by type.
func (e *Engine) SyncSource(ctx context.Context, typ string) (SyncResult, error) {
	p, ok := e.get(typ)
	if !ok {
		return SyncResult{}, fmt.Errorf("unknown source %q", typ)
	}
	return p.Sync(ctx)
}

// Sources lists every configured source with its readiness and credential shape.
// Reading a source's credential status is best-effort: an error leaves that source
// reported as not connected rather than failing the whole listing.
func (e *Engine) Sources(ctx context.Context) ([]SourceStatus, error) {
	pollers := e.snapshot()
	out := make([]SourceStatus, 0, len(pollers))
	for _, p := range pollers {
		desc := p.source.Descriptor()
		connected := true
		if p.cred != nil {
			ok, err := p.cred.CredentialConfigured(ctx)
			if err != nil {
				p.logger.Warn("read source credential status", "source", desc.Type, "error", err)
				ok = false
			}
			connected = ok
		}
		oauth := false
		if oc, ok := p.source.(source.OAuthCredentialed); ok {
			oauth = oc.OAuthConfigured()
		}
		multi := false
		var entries []source.CredentialEntry
		if mc, ok := p.source.(source.MultiCredentialed); ok {
			multi = true
			es, err := mc.ListCredentials(ctx)
			if err != nil {
				p.logger.Warn("list source credentials", "source", desc.Type, "error", err)
			} else {
				entries = es
			}
		}
		out = append(out, SourceStatus{
			Type:              desc.Type,
			Archetype:         string(desc.Archetype),
			Title:             desc.Title,
			Connected:         connected,
			Credentialed:      len(desc.Credentials) > 0,
			MultiCredential:   multi,
			OAuth:             oauth,
			Egress:            desc.Egress,
			Credentials:       desc.Credentials,
			CredentialEntries: entries,
		})
	}
	return out, nil
}

// CredentialConfigured reports whether a source is ready to sync. A source with no
// runtime credential is always ready.
func (e *Engine) CredentialConfigured(ctx context.Context, typ string) (bool, error) {
	p, ok := e.get(typ)
	if !ok {
		return false, fmt.Errorf("unknown source %q", typ)
	}
	if p.cred == nil {
		return true, nil
	}
	return p.cred.CredentialConfigured(ctx)
}

// SetCredential stores a pasted credential for a source.
func (e *Engine) SetCredential(ctx context.Context, typ, input string) error {
	p, ok := e.get(typ)
	if !ok {
		return fmt.Errorf("unknown source %q", typ)
	}
	if p.cred == nil {
		return fmt.Errorf("source %q does not support runtime credentials", typ)
	}
	return p.cred.SetCredential(ctx, input)
}

// RemoveSourceCredential removes one credential (by id) from a multi-credential
// source — e.g. disconnecting a single Teller bank enrollment.
func (e *Engine) RemoveSourceCredential(ctx context.Context, typ, id string) error {
	p, ok := e.get(typ)
	if !ok {
		return fmt.Errorf("unknown source %q", typ)
	}
	mc, ok := p.source.(source.MultiCredentialed)
	if !ok {
		return fmt.Errorf("source %q does not support removing individual credentials", typ)
	}
	return mc.RemoveCredential(ctx, id)
}

// OAuthStart returns the provider consent URL for a source that supports the
// browser OAuth flow, or an error when it does not.
func (e *Engine) OAuthStart(typ, state string) (string, error) {
	p, ok := e.get(typ)
	if !ok {
		return "", fmt.Errorf("unknown source %q", typ)
	}
	oc, ok := p.source.(source.OAuthCredentialed)
	if !ok || !oc.OAuthConfigured() {
		return "", fmt.Errorf("source %q does not support OAuth sign-in", typ)
	}
	return oc.AuthCodeURL(state), nil
}

// OAuthExchange completes the browser OAuth flow for a source, exchanging the
// callback's authorization code for a stored credential.
func (e *Engine) OAuthExchange(ctx context.Context, typ, code string) error {
	p, ok := e.get(typ)
	if !ok {
		return fmt.Errorf("unknown source %q", typ)
	}
	oc, ok := p.source.(source.OAuthCredentialed)
	if !ok {
		return fmt.Errorf("source %q does not support OAuth sign-in", typ)
	}
	return oc.ExchangeCode(ctx, code)
}
