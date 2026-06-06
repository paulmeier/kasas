package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sampleEvent() events.Event {
	return events.Event{
		Sequence:   5,
		EventID:    "evt-1",
		Type:       "transaction.created",
		EntityType: "transaction",
		EntityID:   "tx1",
		OccurredAt: time.Unix(1700000000, 0).UTC(),
		Data:       json.RawMessage(`{"id":"tx1","amount":"-4.50"}`),
	}
}

func TestSign(t *testing.T) {
	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("12345.body"))
	want := hex.EncodeToString(mac.Sum(nil))

	assert.Equal(t, want, Sign("secret", "12345", []byte("body")))
	assert.NotEqual(t, want, Sign("other", "12345", []byte("body")), "a different secret changes the signature")
}

func TestMatches(t *testing.T) {
	all := db.Webhook{EventTypes: `["*"]`}
	empty := db.Webhook{EventTypes: ``}
	emptyArr := db.Webhook{EventTypes: `[]`}
	specific := db.Webhook{EventTypes: `["transaction.created","label.applied"]`}

	assert.True(t, Matches(all, "rule.created"), "wildcard matches all")
	assert.True(t, Matches(empty, "rule.created"), "empty matches all")
	assert.True(t, Matches(emptyArr, "rule.created"), "empty array matches all")
	assert.True(t, Matches(specific, "transaction.created"))
	assert.True(t, Matches(specific, "label.applied"))
	assert.False(t, Matches(specific, "rule.created"), "unsubscribed type does not match")
}

func TestEncodeDecodeEventTypes(t *testing.T) {
	encoded, err := EncodeEventTypes([]string{"transaction.created", "label.applied"})
	require.NoError(t, err)
	assert.Equal(t, []string{"transaction.created", "label.applied"}, DecodeEventTypes(encoded))

	all, err := EncodeEventTypes(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", all)
	assert.Nil(t, DecodeEventTypes(""))
	assert.Nil(t, DecodeEventTypes("not json"))
}

// receivedReq captures what a test endpoint saw, so assertions run on the test
// goroutine rather than the server's.
type receivedReq struct {
	headers http.Header
	body    []byte
}

// recordingServer returns an httptest server that replies with status and reports
// each request it receives on the returned channel.
func recordingServer(t *testing.T, status int) (*httptest.Server, <-chan receivedReq) {
	t.Helper()
	ch := make(chan receivedReq, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- receivedReq{headers: r.Header.Clone(), body: body}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func TestDeliverSuccess(t *testing.T) {
	srv, ch := recordingServer(t, http.StatusOK)
	wh := db.Webhook{Url: srv.URL, Secret: "whsec_test"}
	ev := sampleEvent()

	status, err := Deliver(context.Background(), srv.Client(), "kasas/test", wh, ev)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)

	got := <-ch
	assert.Equal(t, "transaction.created", got.headers.Get(HeaderEvent))
	assert.Equal(t, "evt-1", got.headers.Get(HeaderEventID))
	assert.Equal(t, "kasas/test", got.headers.Get("User-Agent"))

	ts := got.headers.Get(HeaderTimestamp)
	require.NotEmpty(t, ts)
	assert.Equal(t, "sha256="+Sign(wh.Secret, ts, got.body), got.headers.Get(HeaderSignature),
		"signature must verify over <timestamp>.<body>")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(got.body, &payload))
	assert.EqualValues(t, 5, payload["sequence"])
	assert.Equal(t, "transaction.created", payload["type"])
	assert.Equal(t, map[string]any{"id": "tx1", "amount": "-4.50"}, payload["data"])
}

func TestDeliverNon2xx(t *testing.T) {
	srv, _ := recordingServer(t, http.StatusInternalServerError)
	status, err := Deliver(context.Background(), srv.Client(), "kasas/test", db.Webhook{Url: srv.URL, Secret: "s"}, sampleEvent())
	assert.Error(t, err, "a non-2xx status is an error so the dispatcher retries")
	assert.Equal(t, http.StatusInternalServerError, status)
}

// fakeStore is a concurrency-safe in-memory webhooks.Store for the dispatcher test.
type fakeStore struct {
	mu       sync.Mutex
	hooks    []db.Webhook
	events   []db.Event
	statuses []db.UpdateWebhookDeliveryStatusParams
}

func (f *fakeStore) ListEnabledWebhooks(context.Context) ([]db.Webhook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]db.Webhook(nil), f.hooks...), nil
}

func (f *fakeStore) ListEventsAfter(_ context.Context, arg db.ListEventsAfterParams) ([]db.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.Event
	for _, e := range f.events {
		if e.ID > arg.After {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeStore) ListRecentEvents(_ context.Context, _ int64) ([]db.Event, error) {
	return nil, nil // empty stream → head starts at 0
}

func (f *fakeStore) UpdateWebhookDeliveryStatus(_ context.Context, arg db.UpdateWebhookDeliveryStatusParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, arg)
	return nil
}

func (f *fakeStore) lastStatus() (db.UpdateWebhookDeliveryStatusParams, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return db.UpdateWebhookDeliveryStatusParams{}, false
	}
	return f.statuses[len(f.statuses)-1], true
}

func TestDispatcherDeliversAndRecords(t *testing.T) {
	srv, ch := recordingServer(t, http.StatusOK)
	store := &fakeStore{hooks: []db.Webhook{{
		ID: 1, Url: srv.URL, Secret: "whsec_test", EventTypes: `["*"]`, Enabled: 1,
	}}}
	bus := events.NewBus()
	t.Cleanup(bus.Close)

	d := NewDispatcher(store, bus, Options{RetryBaseDelay: time.Millisecond, Workers: 2, UserAgent: "kasas/test"}, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()

	// Publish until the dispatcher (now subscribed) delivers, or time out. Re-publishing
	// the same sequence is deduped by the dispatcher, so exactly one delivery lands.
	ev := sampleEvent()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var got receivedReq
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("webhook was not delivered within the timeout")
		case got = <-ch:
		case <-tick.C:
			bus.Publish(ev)
			continue
		}
		break
	}

	assert.Equal(t, "transaction.created", got.headers.Get(HeaderEvent))
	ts := got.headers.Get(HeaderTimestamp)
	assert.Equal(t, "sha256="+Sign("whsec_test", ts, got.body), got.headers.Get(HeaderSignature))

	require.Eventually(t, func() bool {
		st, ok := store.lastStatus()
		return ok && st.LastStatus == http.StatusOK && st.LastError == "" && st.LastSuccessAt > 0
	}, 2*time.Second, 10*time.Millisecond, "a successful delivery should be recorded on the webhook row")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not stop on context cancel")
	}
}

func TestDispatcherSkipsUnsubscribedType(t *testing.T) {
	srv, ch := recordingServer(t, http.StatusOK)
	store := &fakeStore{hooks: []db.Webhook{{
		ID: 1, Url: srv.URL, Secret: "s", EventTypes: `["label.applied"]`, Enabled: 1,
	}}}
	bus := events.NewBus()
	t.Cleanup(bus.Close)

	d := NewDispatcher(store, bus, Options{RetryBaseDelay: time.Millisecond, UserAgent: "kasas/test"}, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)

	// Publish a type the webhook is not subscribed to a handful of times; nothing
	// should arrive at the endpoint.
	ev := sampleEvent() // transaction.created
	for i := 0; i < 10; i++ {
		bus.Publish(ev)
		time.Sleep(15 * time.Millisecond)
	}
	select {
	case <-ch:
		t.Fatal("delivered an event the webhook was not subscribed to")
	case <-time.After(100 * time.Millisecond):
		// expected: no delivery
	}
}
