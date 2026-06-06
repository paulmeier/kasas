package events

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// subscriberBuffer bounds each subscriber's channel. A subscriber that falls this
// far behind is dropped (its channel closed) so it reconnects and replays from its
// last cursor rather than silently missing events.
const subscriberBuffer = 64

var eventsDropped = promauto.NewCounter(prometheus.CounterOpts{
	Name: "kasas_events_dropped_total",
	Help: "Total live event subscribers dropped for falling behind the bus buffer.",
})

// Bus is an in-process fan-out hub: the Emitter publishes committed events to it,
// and SSE handlers subscribe for the live tail. It is safe for concurrent use.
type Bus struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	nextID int
	closed bool
}

// NewBus constructs an empty Bus.
func NewBus() *Bus {
	return &Bus{subs: make(map[int]chan Event)}
}

// Subscribe registers a new subscriber, returning its event channel and a cancel
// func that unsubscribes and closes the channel. The channel is closed (so a
// range over it exits) when the subscriber cancels, the bus closes, or the
// subscriber falls behind and is dropped. On an already-closed bus it returns an
// already-closed channel and a no-op cancel.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subscriberBuffer)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		close(ch)
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	b.subs[id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}

// Publish fans events out to every current subscriber. The send is non-blocking so
// one slow subscriber cannot stall the publisher; a subscriber whose buffer is
// full is dropped (channel closed) so it reconnects and replays from its cursor
// rather than receiving a silently-truncated stream. Publishing nothing is a
// no-op.
func (b *Bus) Publish(evs ...Event) {
	if len(evs) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ev := range evs {
		for id, ch := range b.subs {
			select {
			case ch <- ev:
			default:
				delete(b.subs, id)
				close(ch)
				eventsDropped.Inc()
			}
		}
	}
}

// Close unsubscribes and closes every subscriber channel; afterwards Subscribe
// returns an already-closed channel. Idempotent.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, ch := range b.subs {
		delete(b.subs, id)
		close(ch)
	}
}
