package api_test

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

// newEventsServer builds an httptest server whose API records events (the default
// test server has no emitter, so events are disabled there).
func newEventsServer(t *testing.T) (*httptest.Server, testutil.Fixtures) {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	fx := testutil.Seed(t, store)
	bus := events.NewBus()
	t.Cleanup(bus.Close)
	s := api.New(api.Options{
		Store:      store,
		Syncer:     &fakeSyncer{},
		Emitter:    events.NewEmitter(bus),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		MCPEnabled: true,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, fx
}

type eventsResponse struct {
	Events []api.EventDTO `json:"events"`
	Next   int64          `json:"next"`
}

// applyLabel is a small helper that drives a label edit (which emits label events).
func applyLabel(t *testing.T, srv *httptest.Server, txnID, key, value string) {
	t.Helper()
	code := putJSON(t, srv, "/api/v1/transactions/"+txnID+"/labels",
		map[string]any{"labels": map[string]string{key: value}}, nil)
	require.Equal(t, http.StatusOK, code)
}

func TestEventsListCursorAndFilter(t *testing.T) {
	srv, fx := newEventsServer(t)
	applyLabel(t, srv, fx.TxIDsByDateDesc[0], "a", "1")
	applyLabel(t, srv, fx.TxIDsByDateDesc[1], "b", "2")

	var out eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events", &out))
	require.Len(t, out.Events, 2)
	assert.Equal(t, "label.applied", out.Events[0].Type)
	assert.Equal(t, out.Events[1].Sequence, out.Next, "next is the last sequence on the page")

	// Cursor: after the first event returns only the second.
	var page eventsResponse
	getJSON(t, srv, "/api/v1/events?after="+strconv.FormatInt(out.Events[0].Sequence, 10), &page)
	require.Len(t, page.Events, 1)
	assert.Equal(t, out.Events[1].Sequence, page.Events[0].Sequence)

	// Filter by entity_id.
	var filtered eventsResponse
	getJSON(t, srv, "/api/v1/events?entity_id="+fx.TxIDsByDateDesc[0], &filtered)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, fx.TxIDsByDateDesc[0], filtered.Events[0].EntityID)
}

func TestEventGetBySequence(t *testing.T) {
	srv, fx := newEventsServer(t)
	applyLabel(t, srv, fx.TxIDsByDateDesc[0], "a", "1")

	var list eventsResponse
	getJSON(t, srv, "/api/v1/events", &list)
	require.NotEmpty(t, list.Events)
	seq := list.Events[0].Sequence

	var ev api.EventDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events/"+strconv.FormatInt(seq, 10), &ev))
	assert.Equal(t, seq, ev.Sequence)
	assert.Equal(t, "label.applied", ev.Type)

	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/events/99999", nil))
	assert.Equal(t, http.StatusBadRequest, getJSON(t, srv, "/api/v1/events/not-a-number", nil))
}

func TestEventsNewest(t *testing.T) {
	srv, fx := newEventsServer(t)
	for _, id := range fx.TxIDsByDateDesc {
		applyLabel(t, srv, id, "a", "1")
	}
	var out eventsResponse
	getJSON(t, srv, "/api/v1/events?newest=1&limit=2", &out)
	require.Len(t, out.Events, 2)
	assert.Less(t, out.Events[0].Sequence, out.Events[1].Sequence, "newest page is chronological")
	assert.Equal(t, out.Events[1].Sequence, out.Next, "next is the head sequence")
}

func TestLabelEditEmitsAppliedAndRemoved(t *testing.T) {
	srv, fx := newEventsServer(t)
	id := fx.TxIDsByDateDesc[0]
	applyLabel(t, srv, id, "category", "food")
	// PUT replaces the whole set, so this drops "category" and adds "person".
	applyLabel(t, srv, id, "person", "dad")

	var applied eventsResponse
	getJSON(t, srv, "/api/v1/events?type=label.applied", &applied)
	assert.Len(t, applied.Events, 2, "one per added/changed key across both edits")

	var removed eventsResponse
	getJSON(t, srv, "/api/v1/events?type=label.removed", &removed)
	require.Len(t, removed.Events, 1, "the dropped key emits label.removed")
	assert.Equal(t, "transaction", removed.Events[0].EntityType)
}

func TestLabelDeleteEmitsCoarseEvent(t *testing.T) {
	srv, fx := newEventsServer(t)
	applyLabel(t, srv, fx.TxIDsByDateDesc[0], "category", "food")

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/labels/category", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()

	var out eventsResponse
	getJSON(t, srv, "/api/v1/events?type=label.removed", &out)
	require.Len(t, out.Events, 1)
	assert.Equal(t, "label", out.Events[0].EntityType, "a vocabulary delete is one coarse event")
	assert.Equal(t, "category", out.Events[0].EntityID)
}

func TestRuleLifecycleEmitsEvents(t *testing.T) {
	srv, _ := newEventsServer(t)

	var created api.RuleDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules",
		map[string]any{"name": "coffee", "query": "description:Coffee", "labels": map[string]string{"cat": "coffee"}}, &created))

	// Run the rule over existing transactions -> label.applied + rule.executed.
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/rules/"+strconv.FormatInt(created.ID, 10)+"/run", nil, nil))

	for _, tc := range []struct {
		typ   string
		min   int
		first string
	}{
		{"rule.created", 1, "rule"},
		{"rule.executed", 1, "rule"},
		{"label.applied", 1, "transaction"},
	} {
		var out eventsResponse
		getJSON(t, srv, "/api/v1/events?type="+tc.typ, &out)
		require.GreaterOrEqual(t, len(out.Events), tc.min, "expected a %s event", tc.typ)
		assert.Equal(t, tc.first, out.Events[0].EntityType)
	}
}

// TestEventStreamSSE connects to the SSE endpoint, triggers a mutation, and
// asserts the live frame arrives (and that the long-lived route is not subject to
// the request timeout, i.e. no 504).
func TestEventStreamSSE(t *testing.T) {
	srv, fx := newEventsServer(t)

	resp, err := http.Get(srv.URL + "/api/v1/events/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// Headers received => the handler has subscribed; now trigger an event.
	frames := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data:") {
				frames <- strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				return
			}
		}
	}()

	applyLabel(t, srv, fx.TxIDsByDateDesc[0], "category", "food")

	select {
	case data := <-frames:
		var dto api.EventDTO
		require.NoError(t, json.Unmarshal([]byte(data), &dto))
		assert.Equal(t, "label.applied", dto.Type)
		assert.NotZero(t, dto.Sequence)
	case <-time.After(3 * time.Second):
		t.Fatal("no SSE frame received within 3s")
	}
}

func TestEventStreamDisabledWhenEventsOff(t *testing.T) {
	// The default test server has no emitter, so the stream reports unavailable.
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/events/stream")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
