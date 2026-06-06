package events

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func testEvent(seq int64) Event {
	return Event{
		Sequence:   seq,
		EventID:    "id",
		Type:       TypeTransactionCreated,
		EntityType: EntityTransaction,
		EntityID:   "tx",
		Data:       json.RawMessage(`{}`),
	}
}

func recv(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

func TestBusFanOut(t *testing.T) {
	b := NewBus()
	defer b.Close()
	sub1, cancel1 := b.Subscribe()
	defer cancel1()
	sub2, cancel2 := b.Subscribe()
	defer cancel2()

	b.Publish(testEvent(1))

	assert.Equal(t, int64(1), recv(t, sub1).Sequence)
	assert.Equal(t, int64(1), recv(t, sub2).Sequence)
}

func TestBusCancelUnsubscribes(t *testing.T) {
	b := NewBus()
	defer b.Close()
	sub, cancel := b.Subscribe()
	cancel()

	_, ok := <-sub
	assert.False(t, ok, "a cancelled subscriber's channel is closed")

	// Publishing after cancel reaches no one and does not panic.
	b.Publish(testEvent(1))
}

func TestBusDropsSlowSubscriber(t *testing.T) {
	b := NewBus()
	defer b.Close()
	sub, cancel := b.Subscribe()
	defer cancel()

	// Overflow the buffer without reading: the subscriber is dropped (closed).
	for i := 0; i < subscriberBuffer+10; i++ {
		b.Publish(testEvent(int64(i)))
	}

	closed := false
	for i := 0; i < subscriberBuffer+10; i++ {
		if _, ok := <-sub; !ok {
			closed = true
			break
		}
	}
	assert.True(t, closed, "a subscriber that overflows its buffer is dropped")
}

func TestBusCloseClosesSubscribers(t *testing.T) {
	b := NewBus()
	sub, _ := b.Subscribe()

	b.Close()
	_, ok := <-sub
	assert.False(t, ok, "Close closes existing subscribers")

	// Subscribe after Close yields an already-closed channel.
	sub2, cancel := b.Subscribe()
	defer cancel()
	_, ok = <-sub2
	assert.False(t, ok)

	b.Close() // idempotent
}

func TestBusConcurrentPublish(t *testing.T) {
	b := NewBus()
	defer b.Close()
	sub, cancel := b.Subscribe()
	defer cancel()

	const total = 50 // <= subscriberBuffer, so nothing is dropped
	done := make(chan struct{})
	go func() {
		n := 0
		for range sub {
			if n++; n == total {
				close(done)
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				b.Publish(testEvent(int64(base*10 + j)))
			}
		}(i)
	}
	wg.Wait()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive all concurrently-published events")
	}
}
