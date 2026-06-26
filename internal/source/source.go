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
//   - [Receiver]     accept an inbound, pushed request (webhook archetype)
//   - [Warmer]       warm a read-through cache (reference archetype: market data)
//   - [Credentialed] manage a runtime-settable credential (optional)
//
// File-import and enrichment capabilities will be added as further interfaces
// here as those archetypes are built; existing sources are unaffected because
// each capability is independent.
//
// A [Receiver] inverts the [Puller] direction: instead of the engine fetching on
// a schedule, an external system POSTs a delivery to the source's ingest endpoint
// and the engine routes it to Receive, which authenticates + parses it into the
// same [ImportBatch] the persist path already consumes. The shared signing secret
// such a source authenticates against is managed via [WebhookSecret].
//
// A reference source (archetype "reference") does not produce ledger
// transactions at all. Instead of [Puller] it implements [Warmer], which the
// engine drives instead of the transactional persist path — so it can be a
// first-class source (listed in /api/v1/sources, with runtime credentials and a
// generic per-source sync) without ever touching accounts or transactions.
package source

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// ErrUnauthorizedDelivery is returned by [Receiver.Receive] when an inbound
// delivery fails authentication — a missing, malformed, or invalid signature, or
// a timestamp outside the accepted freshness window. The API maps it to HTTP 401;
// any other Receive error maps to 400 (a well-authenticated but malformed body).
// It is deliberately coarse so a handler never leaks which check failed.
var ErrUnauthorizedDelivery = errors.New("source: unauthorized webhook delivery")

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

// Receiver is implemented by inbound-webhook sources (archetype "webhook"). It
// inverts the [Puller] direction: rather than the engine fetching on a schedule,
// an external system POSTs a [Delivery] to POST /api/v1/sources/{type}/ingest and
// the engine routes it here. The source authenticates the delivery (e.g. HMAC
// signature verification against its [WebhookSecret]) and parses it into a neutral
// batch; the engine then persists that batch through the same path as a pulled one.
//
// Receive must:
//   - return [ErrUnauthorizedDelivery] when authentication fails (→ HTTP 401),
//   - return any other error for a well-authenticated but unusable body (→ 400),
//   - return (nil, nil) for an authenticated delivery with nothing to persist
//     (e.g. a provider verification ping),
//   - be idempotent: re-delivery is expected and deduplicated by (source, id), so
//     a source synthesizes a stable id for any transaction the sender did not key.
//
// It must not write to the database; it only returns data.
type Receiver interface {
	Receive(ctx context.Context, delivery Delivery) (*ImportBatch, error)
}

// Delivery is one inbound webhook request, already read and size-limited by the
// engine so a [Receiver] verifies and parses it without touching the HTTP layer
// (which also keeps sources unit-testable without a server). Body is the exact raw
// bytes — signature verification must run over these, not a re-encoding.
type Delivery struct {
	Header http.Header
	Body   []byte
}

// WebhookSecret is implemented by inbound-webhook sources whose shared signing
// secret kasas mints for the operator to copy into the sender. Unlike a write-only
// [Credentialed] secret, this one must be revealable (to configure the sender) and
// rotatable. Both operations are admin-gated by the API. A source that also
// implements [Credentialed] lets a power user paste a specific secret instead.
type WebhookSecret interface {
	// RevealSecret returns the current shared signing secret, or "" when none has
	// been generated yet (the source is then inert and rejects every delivery).
	RevealSecret(ctx context.Context) (string, error)
	// RotateSecret mints a new secret, stores it, and returns it. The previous
	// secret stops verifying immediately.
	RotateSecret(ctx context.Context) (string, error)
}

// Warmer is implemented by reference sources (archetype "reference") that maintain
// a server-side read-through cache rather than ingest transactions — e.g. the
// market-data source backed by an external provider. The engine drives Warm in
// place of the [Puller] persist path: a scheduled run (when an interval is set) or
// the generic POST /api/v1/sources/{type}/sync both call it, which here means
// "refresh the configured series whose cache is cold or stale." Warm must be
// idempotent and must not block indefinitely; it owns its own storage and event
// emission (the engine only logs the run to sync_log).
type Warmer interface {
	Warm(ctx context.Context) error
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

// OAuthCredentialed is implemented by sources whose runtime credential is obtained
// through a browser OAuth 2.0 authorization-code flow (e.g. Google Drive). The
// engine exposes two endpoints per such source: one that begins the flow
// (redirecting the user to the provider's consent screen) and one the provider
// redirects back to with an authorization code, which the source exchanges for a
// long-lived token and stores. It composes with [Credentialed]: both ultimately
// persist the same secret, so a user can either click through the flow or paste a
// pre-obtained token directly.
type OAuthCredentialed interface {
	// OAuthConfigured reports whether the OAuth client is fully set up (client id,
	// secret, and the registered redirect URL), i.e. whether the browser flow can be
	// offered. When false, only a pasted credential (Credentialed.SetCredential) is
	// possible. The source owns its redirect URL (it must match what is registered
	// with the provider), so the engine does not supply one.
	OAuthConfigured() bool
	// AuthCodeURL builds the provider consent URL to redirect the user to. state is
	// an opaque anti-CSRF value the engine generated and verifies on callback.
	AuthCodeURL(state string) string
	// ExchangeCode exchanges an authorization code (delivered to the callback) for a
	// token and persists it for future syncs.
	ExchangeCode(ctx context.Context, code string) error
}

// MultiCredentialed is implemented by sources that hold several independent
// credentials at once — e.g. Teller, where each access token is one bank
// enrollment and a household links several. It composes with [Credentialed]:
// SetCredential adds a credential to the set (rather than replacing it), while
// these methods list the configured credentials (masked) and remove one by id, so
// the dashboard and API can manage the whole set. The engine detects it by type
// assertion and surfaces a per-entry list with remove controls.
type MultiCredentialed interface {
	// ListCredentials returns the configured credentials, masked — never the secret
	// itself. Each entry carries a stable id (for removal) and a display label.
	ListCredentials(ctx context.Context) ([]CredentialEntry, error)
	// RemoveCredential removes the credential with the given id. Removing one that
	// is absent (or not removable, e.g. declared in static config) is an error.
	RemoveCredential(ctx context.Context, id string) error
}

// CredentialEntry describes one configured credential of a [MultiCredentialed]
// source without revealing the secret.
type CredentialEntry struct {
	ID        string `json:"id"`        // stable identifier, used to remove this entry
	Label     string `json:"label"`     // masked display label (e.g. "••••cd34")
	Removable bool   `json:"removable"` // false for credentials declared in static config
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
	ArchetypeReference  Archetype = "reference"  // warms a read-through cache (market data)
)

// Descriptor is a source's static, self-describing metadata.
type Descriptor struct {
	Type        string            `json:"type"`      // stable identifier, e.g. "simplefin"
	Archetype   Archetype         `json:"archetype"` // how it delivers data
	Title       string            `json:"title"`     // human-readable name
	Credentials []CredentialField `json:"credentials,omitempty"`
	Config      []ConfigField     `json:"config,omitempty"`
	// Egress lists the external hostnames this source contacts (e.g. a market
	// provider's API host). It is surfaced to the operator so a source's network
	// reach is as visible as a plugin's net:fetch allowlist, never silent (ADR
	// 0006). Empty for sources that talk only to the bank the user already
	// configured (their host is the credential itself).
	Egress []string `json:"egress,omitempty"`
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
	// ExternalID is the source's own stable id for the transaction; the engine
	// deduplicates by (source, external id) across re-fetches. When the upstream has
	// no stable id (e.g. a CSV row), the source synthesizes a deterministic one from
	// the row's content (a content hash) so re-importing the same row is idempotent.
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
