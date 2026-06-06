package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
)

var eventsEmitted = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "kasas_events_emitted_total",
	Help: "Total events appended to the stream, labelled by type.",
}, []string{"type"})

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
