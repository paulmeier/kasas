// Package source defines the contract every kasas ingestion source implements,
// plus a registry that lets a source register itself by importing its package.
//
// The design separates a source (which knows how to talk to one provider and
// shape its data) from the ingestion engine (which owns scheduling, the
// transactional persist, dedup, events, rules, and history). A source produces
// a neutral [ImportBatch]; the engine writes it. A source never touches the
// database, so a buggy source can at worst return a batch the engine rejects.
//
// Capabilities are small, composable interfaces keyed to the ingestion
// archetypes. A source implements whichever fit and the engine detects them by
// type assertion:
//
//   - [Puller]       pull/poll on a schedule (SimpleFIN, Teller, on-chain)
//   - [Credentialed] manage a runtime-settable credential (optional)
//
// File-import, inbound-webhook, and enrichment capabilities will be added as
// further interfaces here as those archetypes are built; existing sources are
// unaffected because each capability is independent.
package source

import (
	"context"
	"encoding/json"
	"time"
)

// Source is the minimal contract: every source describes itself. All ingestion
// behaviour lives in the capability interfaces below, which a source implements
// in addition to this.
type Source interface {
	// Descriptor returns the source's static metadata: its type, archetype, and
	// the credential/config fields it needs (used to render setup UI and to list
	// available sources across REST/MCP/dashboard).
	Descriptor() Descriptor
}

// Puller is implemented by sources the engine fetches on a schedule (archetype
// "pull"). Fetch returns a normalized batch covering transactions on or after
// since (zero means "everything the source will give"), resuming from the opaque
// cursor the source returned last time (empty on the first run). The returned
// batch's Cursor, if any, is persisted by the engine and passed back next time.
//
// A Puller must be idempotent: re-fetching overlapping data is expected and the
// engine deduplicates by (source, external id). It must not write to the
// database; it only returns data.
type Puller interface {
	Fetch(ctx context.Context, since time.Time, cursor string) (*ImportBatch, error)
}

// Credentialed is implemented by sources whose connection credential can be set
// at runtime (e.g. from the dashboard Settings page) rather than only via static
// config. The engine exposes these to the API's credential-management endpoints.
type Credentialed interface {
	// CredentialConfigured reports whether the source currently has a usable
	// credential stored (i.e. a sync can run).
	CredentialConfigured(ctx context.Context) (bool, error)
	// SetCredential stores a credential for future syncs, no restart required.
	// The accepted form is source-specific (e.g. an access URL or a setup token).
	SetCredential(ctx context.Context, input string) error
}

// Archetype classifies how a source delivers data, which determines how the
// engine triggers it. See the package doc for the full set.
type Archetype string

const (
	ArchetypePull       Archetype = "pull"       // engine fetches on a schedule
	ArchetypeFile       Archetype = "file"       // a file is parsed on upload/arrival
	ArchetypeWebhook    Archetype = "webhook"    // an inbound request is received
	ArchetypeManual     Archetype = "manual"     // a human/agent writes directly
	ArchetypeEnrichment Archetype = "enrichment" // annotates existing transactions
)

// Descriptor is a source's static, self-describing metadata.
type Descriptor struct {
	Type        string            `json:"type"`      // stable identifier, e.g. "simplefin"
	Archetype   Archetype         `json:"archetype"` // how it delivers data
	Title       string            `json:"title"`     // human-readable name
	Credentials []CredentialField `json:"credentials,omitempty"`
	Config      []ConfigField     `json:"config,omitempty"`
}

// CredentialField declares one secret a source needs to connect. The engine
// renders these as a setup form and stores their values out of band; they are
// never echoed back.
type CredentialField struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	Help  string `json:"help,omitempty"`
}

// ConfigField declares one non-secret knob a source accepts (e.g. a chain id, or
// a CSV column-mapping profile name).
type ConfigField struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Help     string `json:"help,omitempty"`
	Required bool   `json:"required,omitempty"`
}

// ImportBatch is the neutral, source-agnostic result of a fetch or parse. The
// engine consumes only this type, so every source — first-party Go or a future
// plugin — looks identical downstream.
type ImportBatch struct {
	// Source is the provenance stamp written on every transaction in the batch
	// (the descriptor Type). It identifies which ingestion path produced a row.
	Source string `json:"source"`
	// Accounts are the accounts observed, each with its transactions.
	Accounts []ImportAccount `json:"accounts"`
	// Cursor is an opaque resume token the engine persists and passes back on the
	// next pull. Empty for sources that re-fetch a window (like SimpleFIN).
	Cursor string `json:"cursor,omitempty"`
}

// ImportOrg is the financial institution that owns an account, normalized.
type ImportOrg struct {
	// ID is a stable identifier for the institution. The source is responsible
	// for deriving a stable value (e.g. falling back from an id to a domain).
	ID     string `json:"id"`
	Domain string `json:"domain,omitempty"`
	Name   string `json:"name,omitempty"`
	URL    string `json:"url,omitempty"`
}

// ImportAccount is one account with its transactions, normalized.
type ImportAccount struct {
	// ExternalID is the source's own id for the account. The engine namespaces it
	// by source for storage; the source supplies it verbatim.
	ExternalID   string      `json:"external_id"`
	Org          ImportOrg   `json:"org"`
	Name         string      `json:"name"`
	Currency     string      `json:"currency"`
	Balance      string      `json:"balance"`
	BalanceDate  int64       `json:"balance_date"` // unix seconds
	Transactions []ImportTxn `json:"transactions"`
}

// ImportTxn is one transaction, normalized to kasas's universal core fields.
// Anything source-specific (gas, symbol, fee, line items) belongs in Extensions.
type ImportTxn struct {
	// ExternalID is the source's own id for the transaction. When a source has no
	// stable id (e.g. a CSV row), the engine will synthesize a content hash; in
	// that case the source may leave this empty.
	ExternalID  string `json:"external_id"`
	Amount      string `json:"amount"`
	Date        int64  `json:"date"` // unix seconds, already resolved by the source
	Description string `json:"description"`
	Payee       string `json:"payee"`
	Memo        string `json:"memo"`
	Pending     bool   `json:"pending"`
	// Extensions carries variable, source-specific richness as namespaced JSON.
	// Reserved for richer sources; not yet persisted by the engine.
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}
