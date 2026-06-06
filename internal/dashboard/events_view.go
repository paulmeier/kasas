package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

const (
	eventsInitialLimit = 150             // most-recent events fetched on mount
	eventsPollLimit    = 200             // events pulled per forward poll
	eventsPollInterval = 4 * time.Second // live-tail cadence
	eventsMaxStored    = 1000            // cap retained rows so the feed stays light
)

// eventsView is the Events page: a live feed of the canonical event stream. It
// loads the most recent events, then polls GET /api/v1/events?after= forward on an
// interval and appends new ones (newest shown first). Read-only — the dashboard
// polls rather than using SSE because a browser EventSource cannot send the
// dashboard token.
type eventsView struct {
	app.Compo
	chrome

	events  []event // chronological (oldest first); rendered newest-first
	lastSeq int64   // highest sequence seen, the forward poll cursor
	loading bool
	errMsg  string

	stop    chan struct{} // closed on dismount to end the poll loop
	stopped bool
}

func (v *eventsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.loading = true
	v.stop = make(chan struct{})
	v.loadRecent(ctx)
}

func (v *eventsView) OnDismount() {
	if v.stop != nil && !v.stopped {
		v.stopped = true
		close(v.stop)
	}
}

// loadRecent fetches the most recent events, then starts the live tail from the
// head sequence it reports.
func (v *eventsView) loadRecent(ctx app.Context) {
	ctx.Async(func() {
		evs, next, err := v.client.recentEvents(context.Background(), eventsInitialLimit)
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.events = evs
			v.lastSeq = next
			ctx.Update()
			v.startPolling(ctx, next)
		})
	})
}

// startPolling tails the stream forward from `after`, appending new events until
// the view is dismounted. The cursor stays local to the goroutine so view state is
// never read off the UI thread; the stop channel (closed on dismount) ends the loop.
func (v *eventsView) startPolling(ctx app.Context, after int64) {
	stop := v.stop
	ctx.Async(func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(eventsPollInterval):
			}
			evs, next, err := v.client.events(context.Background(), after, eventsPollLimit)
			if err != nil || len(evs) == 0 {
				continue
			}
			after = next
			batch := evs
			ctx.Dispatch(func(ctx app.Context) {
				v.appendEvents(batch, next)
				ctx.Update()
			})
		}
	})
}

// appendEvents adds a batch of newer events, advancing the cursor and capping the
// retained set so the feed stays light.
func (v *eventsView) appendEvents(batch []event, next int64) {
	v.events = append(v.events, batch...)
	if next > v.lastSeq {
		v.lastSeq = next
	}
	if len(v.events) > eventsMaxStored {
		v.events = v.events[len(v.events)-eventsMaxStored:]
	}
}

func (v *eventsView) Render() app.UI {
	return v.renderShell(navEvents,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Events"),
			app.Span().Class("page-subtitle").Text("The canonical event stream, live"),
		),
		v.renderError(),
		v.renderBody(),
	)
}

func (v *eventsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *eventsView) renderBody() app.UI {
	if v.loading {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.events) == 0 {
		return app.Div().Class("status").Text("No events yet. Trigger a sync or edit a label to see the stream fill in.")
	}
	return app.Table().Class("txns events-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Class("evt-seq").Text("#"),
				app.Th().Text("Time"),
				app.Th().Text("Type"),
				app.Th().Text("Entity"),
				app.Th().Text("Data"),
			),
		),
		app.TBody().Body(
			app.Range(v.events).Slice(func(i int) app.UI {
				// Render newest-first (the slice is chronological).
				return v.renderEventRow(v.events[len(v.events)-1-i])
			}),
		),
	)
}

func (v *eventsView) renderEventRow(e event) app.UI {
	entity := e.EntityType
	if e.EntityID != "" {
		entity += " " + e.EntityID
	}
	return app.Tr().Body(
		app.Td().Class("evt-seq").Text(strconv.FormatInt(e.Sequence, 10)),
		app.Td().Class("evt-time").Text(e.OccurredAt.Local().Format("2006-01-02 15:04:05")),
		app.Td().Body(app.Span().Class("badge evt-type "+eventTypeClass(e.Type)).Text(e.Type)),
		app.Td().Class("evt-entity").Text(entity),
		app.Td().Class("evt-data").Body(
			app.Details().Body(
				app.Summary().Text("view"),
				app.Pre().Class("evt-json").Text(prettyJSON(e.Data)),
			),
		),
	)
}

// eventTypeClass maps an event type to a CSS modifier class, colour-coding rows by
// entity family (the prefix before the dot).
func eventTypeClass(t string) string {
	switch {
	case strings.HasPrefix(t, "transaction."):
		return "evt-txn"
	case strings.HasPrefix(t, "account."):
		return "evt-acct"
	case strings.HasPrefix(t, "label."):
		return "evt-label"
	case strings.HasPrefix(t, "rule."):
		return "evt-rule"
	case strings.HasPrefix(t, "sync."):
		return "evt-sync"
	default:
		return ""
	}
}

// prettyJSON indents a raw JSON payload for display, falling back to the raw text
// (or "{}") when it cannot be parsed.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
