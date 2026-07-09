// Package events defines kasas's canonical event stream: an append-only, ordered,
// replayable log of meaningful state changes (a transaction synced, a label
// applied, a rule run, ...). It is the substrate external consumers use to build
// sync engines, notifications, automations, and CQRS / event-sourcing.
//
// The package is deliberately decoupled from the HTTP and MCP surfaces. It owns
// the Event value, the in-process Bus that fans live events out to subscribers,
// and the Emitter that records events transactionally with the state change that
// produced them and publishes them only after the transaction commits. It imports
// internal/db to persist events but nothing above it, so the data layer never
// depends on the bus and there is no import cycle.
package events

import (
	"encoding/json"
	"time"
)

// Event is one immutable entry in the stream.
//
//   - Sequence is the monotonic cursor consumers page by. It is assigned by the
//     database on insert (0 before persistence) and is strictly increasing but MAY
//     have gaps, so treat it as a cursor, not a contiguous count.
//   - EventID is a globally-unique UUID for idempotent dedupe across consumers.
//   - Type is a dotted entity.action (one of the Type* constants below).
//   - EntityType + EntityID point at the subject (so a consumer can fetch every
//     event for one transaction with entity_type=transaction&entity_id=<id>).
//   - OccurredAt is when the change happened.
//   - Data is the self-contained JSON snapshot / details payload, so a consumer
//     (or a *.deleted event whose entity is already gone) needs no follow-up query.
type Event struct {
	Sequence   int64
	EventID    string
	Type       string
	EntityType string
	EntityID   string
	OccurredAt time.Time
	Data       json.RawMessage
}

// The canonical event taxonomy. Adding a new mutation point means adding a
// constant here and emitting it at the call site; consumers match on these
// strings.
const (
	TypeTransactionCreated = "transaction.created"
	TypeTransactionUpdated = "transaction.updated"
	// TypeTransactionDeleted fires when a manually-created transaction is deleted
	// through the API, or when a transaction is removed as part of deleting its
	// manual account. Synced transactions are owned by the SimpleFIN bridge and
	// cannot be deleted (they would reappear on the next sync), so only manual rows
	// emit this. Its payload carries the transaction's last-known snapshot.
	TypeTransactionDeleted = "transaction.deleted"

	TypeAccountCreated = "account.created"
	TypeAccountUpdated = "account.updated"
	// TypeAccountDeleted fires when a manually-created account is deleted through the
	// API. Synced accounts are owned by the bridge and cannot be deleted, so only
	// manual accounts emit this. Its payload carries the account's last-known state;
	// each of the account's transactions emits its own transaction.deleted first
	// (the FK cascade is invisible to the stream, so the API emits them explicitly).
	TypeAccountDeleted = "account.deleted"

	TypeLabelApplied = "label.applied"
	TypeLabelRemoved = "label.removed"

	// Schema extensions: arbitrary, app-owned namespaced metadata set or removed on
	// a single transaction (via the REST API or the set_transaction_extensions MCP
	// tool). The event entity is the transaction.
	TypeExtensionSet     = "extension.set"
	TypeExtensionRemoved = "extension.removed"

	// Transaction relationships: a directed edge from one transaction to another
	// created or removed (via the REST API or the create/delete_transaction_relationship
	// MCP tools). The event entity is the SUBJECT transaction (the edge's "from"
	// side) and EntityID is its id; the payload carries the target and kind.
	// Relationships do NOT record transaction history versions (an edge is not a
	// field of one transaction's own state), so there is no Change* kind for them.
	TypeRelationshipCreated = "relationship.created"
	TypeRelationshipRemoved = "relationship.removed"

	TypeRuleCreated  = "rule.created"
	TypeRuleUpdated  = "rule.updated"
	TypeRuleDeleted  = "rule.deleted"
	TypeRuleExecuted = "rule.executed"
	// TypeRuleReverted fires when a rule is unapplied: the labels and extensions it
	// applied are removed from the transactions it currently matches (the inverse of
	// rule.executed). Its payload reports how many transactions matched and how many
	// a label or extension was removed from.
	TypeRuleReverted = "rule.reverted"

	TypeSyncCompleted = "sync.completed"

	// TypeMarketUpdated fires when a market series' cached points are refreshed
	// from the provider (a background stale-while-revalidate refresh, or a warm).
	// EntityID is the series id; clients subscribed to the stream refetch the
	// series' points. World data, not a ledger change (ADR 0006).
	TypeMarketUpdated = "market.updated"
)

// The kinds of subject an event's EntityType can name.
const (
	EntityTransaction  = "transaction"
	EntityAccount      = "account"
	EntityLabel        = "label"
	EntityRule         = "rule"
	EntitySync         = "sync"
	EntityMarketSeries = "market_series"
)

// The change kinds stamped on each immutable transaction version (the
// transaction_versions table). They name the *cause* of a version, coarsely:
// the per-field detail is the diff between consecutive snapshots, and finer
// provenance (which rule, which label) lives in the event stream. There is one
// per transaction mutation seam.
const (
	// ChangeImported is the first version of a transaction: the poller inserted it
	// (folding in any birth labels a rule applied), or it is the synthesized v1
	// baseline written the first time a pre-existing transaction changes.
	ChangeImported = "imported"
	// ChangeSynced is a re-sync that changed a bridge-owned field (a pending charge
	// that posted, or a corrected amount/merchant).
	ChangeSynced = "synced"
	// ChangeLabeled is a change to the transaction's labels (via the REST API or the
	// rules engine).
	ChangeLabeled = "labeled"
	// ChangeExtended is a change to the transaction's schema extensions (via the
	// REST API or the set_transaction_extensions MCP tool).
	ChangeExtended = "extended"
	// ChangeEdited is a manual edit of a transaction's core fields (amount, date,
	// description, account, ...) through the API or dashboard. It applies only to
	// manually-created transactions; synced transactions' core fields are
	// bridge-owned and change only via ChangeSynced.
	ChangeEdited = "edited"
)
