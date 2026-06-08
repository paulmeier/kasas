package poller

import (
	"context"
	"errors"
	"fmt"

	"github.com/paulmeier/kasas/internal/source"
)

// SourceStatus summarizes one ingestion source for the API and dashboard: its
// self-describing metadata plus whether it is ready to sync and how its credential
// (if any) is set.
type SourceStatus struct {
	Type         string                   `json:"type"`
	Archetype    string                   `json:"archetype"`
	Title        string                   `json:"title"`
	Connected    bool                     `json:"connected"`    // ready to sync (no credential needed, or one is stored)
	Credentialed bool                     `json:"credentialed"` // accepts a pasted credential
	OAuth        bool                     `json:"oauth"`        // supports the browser OAuth connect flow
	Credentials  []source.CredentialField `json:"credentials,omitempty"`
}

// Engine coordinates one Poller per configured ingestion source. It is the seam
// the API talks to for multi-source operations — listing sources, per-source sync,
// and per-source credential/OAuth management — while each Poller still owns its
// own schedule and the shared transactional persist path. Syncing with no type
// (Sync) runs every source, satisfying the api.Syncer the single-source poller
// used to provide.
type Engine struct {
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

// Start schedules every source's recurring sync.
func (e *Engine) Start(ctx context.Context) error {
	for _, typ := range e.order {
		if err := e.pollers[typ].Start(ctx); err != nil {
			return fmt.Errorf("start %s poller: %w", typ, err)
		}
	}
	return nil
}

// Stop halts every source's scheduler.
func (e *Engine) Stop(ctx context.Context) error {
	var errs []error
	for _, typ := range e.order {
		if err := e.pollers[typ].Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sync runs every source sequentially (SQLite has a single writer) and returns the
// aggregate result. It satisfies api.Syncer, so the global /sync trigger and the
// trigger_sync MCP tool sync all sources. A source that fails is recorded; the
// others still run, and the joined error is returned (nil when all succeed).
func (e *Engine) Sync(ctx context.Context) (SyncResult, error) {
	var agg SyncResult
	var errs []error
	for _, typ := range e.order {
		res, err := e.pollers[typ].Sync(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", typ, err))
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
	p, ok := e.pollers[typ]
	if !ok {
		return SyncResult{}, fmt.Errorf("unknown source %q", typ)
	}
	return p.Sync(ctx)
}

// Sources lists every configured source with its readiness and credential shape.
// Reading a source's credential status is best-effort: an error leaves that source
// reported as not connected rather than failing the whole listing.
func (e *Engine) Sources(ctx context.Context) ([]SourceStatus, error) {
	out := make([]SourceStatus, 0, len(e.order))
	for _, typ := range e.order {
		p := e.pollers[typ]
		desc := p.source.Descriptor()
		connected := true
		if p.cred != nil {
			ok, err := p.cred.CredentialConfigured(ctx)
			if err != nil {
				p.logger.Warn("read source credential status", "source", typ, "error", err)
				ok = false
			}
			connected = ok
		}
		oauth := false
		if oc, ok := p.source.(source.OAuthCredentialed); ok {
			oauth = oc.OAuthConfigured()
		}
		out = append(out, SourceStatus{
			Type:         desc.Type,
			Archetype:    string(desc.Archetype),
			Title:        desc.Title,
			Connected:    connected,
			Credentialed: len(desc.Credentials) > 0,
			OAuth:        oauth,
			Credentials:  desc.Credentials,
		})
	}
	return out, nil
}

// CredentialConfigured reports whether a source is ready to sync. A source with no
// runtime credential is always ready.
func (e *Engine) CredentialConfigured(ctx context.Context, typ string) (bool, error) {
	p, ok := e.pollers[typ]
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
	p, ok := e.pollers[typ]
	if !ok {
		return fmt.Errorf("unknown source %q", typ)
	}
	if p.cred == nil {
		return fmt.Errorf("source %q does not support runtime credentials", typ)
	}
	return p.cred.SetCredential(ctx, input)
}

// OAuthStart returns the provider consent URL for a source that supports the
// browser OAuth flow, or an error when it does not.
func (e *Engine) OAuthStart(typ, state string) (string, error) {
	p, ok := e.pollers[typ]
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
	p, ok := e.pollers[typ]
	if !ok {
		return fmt.Errorf("unknown source %q", typ)
	}
	oc, ok := p.source.(source.OAuthCredentialed)
	if !ok {
		return fmt.Errorf("source %q does not support OAuth sign-in", typ)
	}
	return oc.ExchangeCode(ctx, code)
}
