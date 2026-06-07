package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/relationships"
)

var eventsEmitted = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_events_emitted_total",
	Help: "Total events appended to the stream, labelled by type.",
}, []string{"type"})

var versionsRecorded = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_transaction_versions_total",
	Help: "Total immutable transaction-history snapshots recorded, labelled by change kind.",
}, []string{"kind"})

// Emitter records events transactionally with the mutation that produced them and
// publishes them to the Bus after the transaction commits.
//
// A nil *Emitter is a valid no-op, which is how events.enabled=false fully
// disables the feature: Record still runs the work in a transaction, but no event
// rows are inserted and nothing is published.
type Emitter struct {
	bus   *Bus
	now   func() time.Time
	newID func() string
}

// NewEmitter constructs an Emitter publishing to bus.
func NewEmitter(bus *Bus) *Emitter {
	return &Emitter{
		bus:   bus,
		now:   func() time.Time { return time.Now().UTC() },
		newID: uuid.NewString,
	}
}

// Bus returns the underlying fan-out hub so the API server can subscribe for SSE.
// It returns nil for a nil/no-op emitter (events disabled).
func (e *Emitter) Bus() *Bus {
	if e == nil {
		return nil
	}
	return e.bus
}

// Record runs fn inside a database transaction, capturing every event fn emits via
// the Recorder, then publishes them to the bus once the transaction commits. If fn
// or the commit fails, nothing is published, so the stream never contains an event
// whose change was rolled back. A nil *Emitter still runs fn in a transaction but
// records and publishes nothing.
func (e *Emitter) Record(ctx context.Context, store db.Store, fn func(db.Querier, *Recorder) error) error {
	rec := &Recorder{emitter: e}
	if err := store.RunInTx(ctx, func(q db.Querier) error {
		return fn(q, rec)
	}); err != nil {
		return err
	}
	if e != nil && e.bus != nil {
		e.bus.Publish(rec.events...)
	}
	return nil
}

// Recorder buffers the events emitted during one Record call. It is not safe for
// concurrent use; a single Record's fn runs on one goroutine.
type Recorder struct {
	emitter *Emitter
	events  []Event
}

// Emit appends one event to the stream within the current transaction and buffers
// it for post-commit publication. data is JSON-encoded as the event payload. When
// the recorder belongs to a nil/no-op emitter, Emit does nothing, so callers need
// no events-enabled conditional at the call site.
func (r *Recorder) Emit(ctx context.Context, q db.Querier, eventType, entityType, entityID string, data any) error {
	if r.emitter == nil {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal %s event data: %w", eventType, err)
	}
	row, err := q.InsertEvent(ctx, db.InsertEventParams{
		EventID:    r.emitter.newID(),
		EventType:  eventType,
		EntityType: entityType,
		EntityID:   entityID,
		OccurredAt: r.emitter.now().Unix(),
		Data:       string(raw),
	})
	if err != nil {
		return err
	}
	eventsEmitted.WithLabelValues(eventType).Inc()
	r.events = append(r.events, Event{
		Sequence:   row.ID,
		EventID:    row.EventID,
		Type:       row.EventType,
		EntityType: row.EntityType,
		EntityID:   row.EntityID,
		OccurredAt: time.Unix(row.OccurredAt, 0).UTC(),
		Data:       raw,
	})
	return nil
}

// Version appends one immutable snapshot to a transaction's history within the
// current transaction. snap is the full transaction state at this version and kind
// is the cause (one of the Change* constants). Unlike Emit it does NOT publish to
// the bus — versions are a durable record, not a live stream. When the recorder
// belongs to a nil/no-op emitter (events.enabled=false), Version does nothing, so
// callers need no events-enabled conditional at the call site.
func (r *Recorder) Version(ctx context.Context, q db.Querier, txnID string, snap TransactionPayload, kind string) error {
	if r.emitter == nil {
		return nil
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("marshal %s transaction version: %w", kind, err)
	}
	if _, err := q.InsertTransactionVersion(ctx, db.InsertTransactionVersionParams{
		TransactionID: txnID,
		ChangeKind:    kind,
		OccurredAt:    r.emitter.now().Unix(),
		Data:          string(raw),
	}); err != nil {
		return err
	}
	versionsRecorded.WithLabelValues(kind).Inc()
	return nil
}

// VersionChange records a version for a change to an existing transaction, writing
// a synthesized "imported" baseline first if the transaction has no history yet.
// The baseline is what makes history work for transactions that predate this
// feature: the first time one changes, its prior state becomes v1 and the change
// becomes v2. For a transaction that already has versions, only the change is
// recorded. No-op for a nil/no-op emitter.
func (r *Recorder) VersionChange(ctx context.Context, q db.Querier, txnID string, prior, next TransactionPayload, kind string) error {
	if r.emitter == nil {
		return nil
	}
	n, err := q.CountTransactionVersions(ctx, txnID)
	if err != nil {
		return err
	}
	if n == 0 {
		if err := r.Version(ctx, q, txnID, prior, ChangeImported); err != nil {
			return err
		}
	}
	return r.Version(ctx, q, txnID, next, kind)
}

// EmitLabelDiff records a label.applied event for every key that was added or
// changed and a label.removed for every key that was dropped between a
// transaction's old and new label sets. The event entity is the transaction. This
// is the shared label-change emission for every mutation seam — the REST handlers,
// the rules engine, and plugin host writes — so they all derive the per-key diff
// identically. The caller is responsible for writing the labels column in the same
// transaction; this only emits the change events.
func (r *Recorder) EmitLabelDiff(ctx context.Context, q db.Querier, txnID string, oldLabels, newLabels map[string]string) error {
	for k, v := range newLabels {
		if oldLabels[k] != v {
			if err := r.Emit(ctx, q, TypeLabelApplied, EntityTransaction, txnID, LabelPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	for k, v := range oldLabels {
		if _, ok := newLabels[k]; !ok {
			if err := r.Emit(ctx, q, TypeLabelRemoved, EntityTransaction, txnID, LabelPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	return nil
}

// EmitExtensionDiff records an extension.set event for every key that was added or
// changed and an extension.removed for every key that was dropped between a
// transaction's old and new schema-extension sets. Values are compared as raw JSON
// bytes (already compacted by normalization). The event entity is the transaction.
// Like EmitLabelDiff, the caller writes the extensions column in the same
// transaction; this only emits the change events.
func (r *Recorder) EmitExtensionDiff(ctx context.Context, q db.Querier, txnID string, oldExt, newExt map[string]json.RawMessage) error {
	for k, v := range newExt {
		if old, ok := oldExt[k]; !ok || !bytes.Equal(old, v) {
			if err := r.Emit(ctx, q, TypeExtensionSet, EntityTransaction, txnID, ExtensionPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	for k, v := range oldExt {
		if _, ok := newExt[k]; !ok {
			if err := r.Emit(ctx, q, TypeExtensionRemoved, EntityTransaction, txnID, ExtensionPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	return nil
}

// EmitRelationshipDiff records a relationship.created event for every outbound edge
// added and a relationship.removed for every edge dropped between a transaction's
// old and new relationship sets. Edges are identified by (kind, target). The event
// entity is the subject transaction. Unlike EmitLabelDiff / EmitExtensionDiff, a
// relationship change records NO transaction version — an edge is not a field of
// the transaction's own state. The caller writes the relationships column in the
// same transaction; this only emits the change events.
func (r *Recorder) EmitRelationshipDiff(ctx context.Context, q db.Querier, txnID string, oldRels, newRels []relationships.Relationship) error {
	oldKeys := make(map[string]struct{}, len(oldRels))
	for _, e := range oldRels {
		oldKeys[e.Key()] = struct{}{}
	}
	newKeys := make(map[string]struct{}, len(newRels))
	for _, e := range newRels {
		newKeys[e.Key()] = struct{}{}
	}
	for _, e := range newRels {
		if _, ok := oldKeys[e.Key()]; !ok {
			if err := r.Emit(ctx, q, TypeRelationshipCreated, EntityTransaction, txnID, RelationshipPayload{TransactionID: txnID, Kind: e.Kind, Target: e.Target}); err != nil {
				return err
			}
		}
	}
	for _, e := range oldRels {
		if _, ok := newKeys[e.Key()]; !ok {
			if err := r.Emit(ctx, q, TypeRelationshipRemoved, EntityTransaction, txnID, RelationshipPayload{TransactionID: txnID, Kind: e.Kind, Target: e.Target}); err != nil {
				return err
			}
		}
	}
	return nil
}
