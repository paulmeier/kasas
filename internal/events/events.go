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
	// TypeTransactionDeleted is reserved: kasas has no transaction-delete path
	// today (transactions are owned by the SimpleFIN bridge), so nothing emits it
	// yet. It is defined so consumers can code against a stable taxonomy.
	TypeTransactionDeleted = "transaction.deleted"

	TypeAccountCreated = "account.created"
	TypeAccountUpdated = "account.updated"

	TypeLabelApplied = "label.applied"
	TypeLabelRemoved = "label.removed"

	// Schema extensions: arbitrary, app-owned namespaced metadata set or removed on
	// a single transaction (via the REST API or the set_transaction_extensions MCP
	// tool). The event entity is the transaction.
	TypeExtensionSet     = "extension.set"
	TypeExtensionRemoved = "extension.removed"

	TypeRuleCreated  = "rule.created"
	TypeRuleUpdated  = "rule.updated"
	TypeRuleDeleted  = "rule.deleted"
	TypeRuleExecuted = "rule.executed"

	TypeSyncCompleted = "sync.completed"
)

// The kinds of subject an event's EntityType can name.
const (
	EntityTransaction = "transaction"
	EntityAccount     = "account"
	EntityLabel       = "label"
	EntityRule        = "rule"
	EntitySync        = "sync"
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
)
