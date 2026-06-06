package webhooks

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// Store is the slice of the data layer the Dispatcher needs: the enabled webhook
// set to fan an event out to, the cursor reads used to replay a gap after the bus
// drops it, and the per-webhook delivery-status write. *db.SQLiteStore / the
// Postgres store satisfy it; tests use a fake.
type Store interface {
	ListEnabledWebhooks(ctx context.Context) ([]db.Webhook, error)
	ListEventsAfter(ctx context.Context, arg db.ListEventsAfterParams) ([]db.Event, error)
	ListRecentEvents(ctx context.Context, limit int64) ([]db.Event, error)
	UpdateWebhookDeliveryStatus(ctx context.Context, arg db.UpdateWebhookDeliveryStatusParams) error
}

// Options configures a Dispatcher. Zero values fall back to sensible defaults, so
// callers only set what they care about.
type Options struct {
	Timeout        time.Duration // per-attempt HTTP timeout (default 10s)
	MaxAttempts    int           // total attempts per event before giving up (default 5)
	Workers        int           // concurrent delivery workers (default 8)
	QueueSize      int           // buffered delivery-job queue depth (default 256)
	RetryBaseDelay time.Duration // first backoff step, doubling each retry (default 500ms)
	UserAgent      string        // User-Agent header (default "kasas")
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	if o.Workers <= 0 {
		o.Workers = 8
	}
	if o.QueueSize <= 0 {
		o.QueueSize = 256
	}
	if o.RetryBaseDelay <= 0 {
		o.RetryBaseDelay = 500 * time.Millisecond
	}
	if o.UserAgent == "" {
		o.UserAgent = "kasas"
	}
	return o
}

// replayPage is how many events the gap-replay reads at a time. It matches the SSE
// replay page size.
const replayPage = 500

// maxErrorLen caps the stored last_error so a verbose endpoint can't bloat the row.
const maxErrorLen = 500

// Dispatcher subscribes to the event Bus and delivers each event to every enabled
// webhook subscribed to its type. A small worker pool performs the signed POSTs with
// retry/backoff so HTTP latency never blocks the bus reader (the Bus drops a slow
// subscriber). It tracks the last sequence it processed; if the Bus drops it under a
// burst (e.g. a sync that emits more events than the bus buffer holds), it replays
// the gap from the durable event log and resubscribes — at-least-once-ish delivery
// with no per-delivery table.
type Dispatcher struct {
	store  Store
	bus    *events.Bus
	client *http.Client
	logger *slog.Logger
	opts   Options
	jobs   chan job
}

type job struct {
	wh db.Webhook
	ev events.Event
}

// NewDispatcher constructs a Dispatcher. The HTTP client's per-request timeout is
// set from opts.Timeout.
func NewDispatcher(store Store, bus *events.Bus, opts Options, logger *slog.Logger) *Dispatcher {
	opts = opts.withDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		store:  store,
		bus:    bus,
		client: &http.Client{Timeout: opts.Timeout},
		logger: logger,
		opts:   opts,
		jobs:   make(chan job, opts.QueueSize),
	}
}

// Run delivers events until ctx is cancelled. It starts the worker pool, then loops
// subscribing to the bus and processing the live tail; if a subscription closes
// while ctx is still live (the bus dropped it for lagging), it replays the gap from
// the event log and resubscribes. It blocks; run it in a goroutine. On shutdown it
// stops accepting work and lets the workers drain (in-flight attempts are bounded by
// the HTTP timeout and cancelled with ctx, so shutdown is prompt; anything undelivered
// is reconciled by consumers via the /events cursor).
func (d *Dispatcher) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < d.opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.worker(ctx)
		}()
	}
	defer func() {
		close(d.jobs)
		wg.Wait()
	}()

	// Start at the current head so a restart does not re-deliver the entire backlog;
	// only events committed from now on are pushed.
	lastSeq := d.headSequence(ctx)

	for ctx.Err() == nil {
		sub, cancel := d.bus.Subscribe()
		// Subscribe before replaying so an event committed during the replay is not
		// missed (it arrives on the live channel and is deduped by sequence).
		d.replay(ctx, &lastSeq)
		d.consumeLive(ctx, sub, &lastSeq)
		cancel()
	}
}

// headSequence returns the sequence of the newest stored event, or 0 when the stream
// is empty.
func (d *Dispatcher) headSequence(ctx context.Context) int64 {
	rows, err := d.store.ListRecentEvents(ctx, 1)
	if err != nil || len(rows) == 0 {
		return 0
	}
	return rows[0].ID
}

// consumeLive forwards live events to the workers until ctx is cancelled or the
// subscription is closed (the bus dropped us for lagging — the caller then replays).
func (d *Dispatcher) consumeLive(ctx context.Context, sub <-chan events.Event, lastSeq *int64) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return // bus closed or this subscriber was dropped for lagging
			}
			if ev.Sequence <= *lastSeq {
				continue // already delivered during replay
			}
			d.dispatch(ctx, ev)
			*lastSeq = ev.Sequence
		}
	}
}

// replay re-reads the durable event log from *lastSeq and dispatches the gap, paging
// until caught up. It runs after a drop (and harmlessly does nothing on the first
// pass, when *lastSeq is already the head).
func (d *Dispatcher) replay(ctx context.Context, lastSeq *int64) {
	for ctx.Err() == nil {
		rows, err := d.store.ListEventsAfter(ctx, db.ListEventsAfterParams{After: *lastSeq, RowLimit: replayPage})
		if err != nil {
			d.logger.Error("webhook replay read failed", "after", *lastSeq, "error", err)
			return
		}
		for _, row := range rows {
			d.dispatch(ctx, eventFromRow(row))
			*lastSeq = row.ID
		}
		if len(rows) < replayPage {
			return
		}
	}
}

// dispatch loads the enabled webhooks and enqueues a delivery for each one subscribed
// to this event's type. Enqueue is non-blocking: if the queue is full the delivery is
// dropped (and metered) rather than stalling the bus reader, since consumers can
// reconcile via the /events cursor.
func (d *Dispatcher) dispatch(ctx context.Context, ev events.Event) {
	hooks, err := d.store.ListEnabledWebhooks(ctx)
	if err != nil {
		d.logger.Error("load webhooks for delivery failed", "event", ev.Type, "error", err)
		return
	}
	for _, wh := range hooks {
		if !Matches(wh, ev.Type) {
			continue
		}
		select {
		case d.jobs <- job{wh: wh, ev: ev}:
		default:
			webhookDropped.Inc()
			d.logger.Warn("webhook delivery dropped: queue full",
				"webhook_id", wh.ID, "event", ev.Type, "sequence", ev.Sequence)
		}
	}
}

func (d *Dispatcher) worker(ctx context.Context) {
	for j := range d.jobs {
		d.deliverWithRetry(ctx, j.wh, j.ev)
	}
}

// deliverWithRetry attempts a delivery up to MaxAttempts times with exponential
// backoff, then records the outcome on the webhook row. It abandons the event (without
// recording a spurious failure) when ctx is cancelled, e.g. at shutdown.
func (d *Dispatcher) deliverWithRetry(ctx context.Context, wh db.Webhook, ev events.Event) {
	var (
		status  int
		lastErr error
	)
	for attempt := 1; attempt <= d.opts.MaxAttempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, d.opts.Timeout)
		status, lastErr = Deliver(attemptCtx, d.client, d.opts.UserAgent, wh, ev)
		cancel()
		webhookAttempts.Inc()
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			return // shutting down; not a real delivery failure
		}
		if attempt < d.opts.MaxAttempts {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.backoff(attempt)):
			}
		}
	}

	result := "success"
	if lastErr != nil {
		result = "failed"
		d.logger.Warn("webhook delivery failed",
			"webhook_id", wh.ID, "url", wh.Url, "event", ev.Type,
			"attempts", d.opts.MaxAttempts, "status", status, "error", lastErr)
	}
	webhookDeliveries.WithLabelValues(result).Inc()
	d.recordStatus(ctx, wh, status, lastErr)
}

// backoff is the delay before the n-th retry: RetryBaseDelay doubled per attempt,
// capped at 30s.
func (d *Dispatcher) backoff(attempt int) time.Duration {
	delay := d.opts.RetryBaseDelay << (attempt - 1)
	if max := 30 * time.Second; delay > max || delay <= 0 {
		delay = max
	}
	return delay
}

// recordStatus persists the outcome of the latest attempt. last_success_at advances
// only on success (otherwise the webhook's existing value is preserved). It uses a
// cancellation-detached context so the write still lands during shutdown.
func (d *Dispatcher) recordStatus(ctx context.Context, wh db.Webhook, status int, derr error) {
	now := time.Now().Unix()
	success := wh.LastSuccessAt
	errMsg := ""
	if derr != nil {
		errMsg = truncate(derr.Error(), maxErrorLen)
	} else {
		success = now
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := d.store.UpdateWebhookDeliveryStatus(wctx, db.UpdateWebhookDeliveryStatusParams{
		LastStatus:    int64(status),
		LastError:     errMsg,
		LastAttemptAt: now,
		LastSuccessAt: success,
		ID:            wh.ID,
	}); err != nil {
		d.logger.Warn("record webhook delivery status failed", "webhook_id", wh.ID, "error", err)
	}
}

// eventFromRow adapts a stored event row into the in-memory event the delivery path
// consumes (mirrors the api layer's toEventDTO field mapping).
func eventFromRow(row db.Event) events.Event {
	return events.Event{
		Sequence:   row.ID,
		EventID:    row.EventID,
		Type:       row.EventType,
		EntityType: row.EntityType,
		EntityID:   row.EntityID,
		OccurredAt: time.Unix(row.OccurredAt, 0).UTC(),
		Data:       []byte(row.Data),
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
