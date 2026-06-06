package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// EventDTO is the JSON representation of one event in the canonical stream. It is
// also the body of each SSE `data:` frame, so a consumer parsing the stream gets
// the full envelope. See internal/events for the field semantics; `sequence` is
// the cursor (strictly increasing, may have gaps) and `event_id` is the dedupe key.
//
// Data is the decoded payload object. It is `any` rather than json.RawMessage so
// the MCP tool's generated output schema describes it as a free-form value (a raw
// byte slice would be schematized as an array and rejected by the MCP validator).
type EventDTO struct {
	Sequence   int64     `json:"sequence"`
	EventID    string    `json:"event_id"`
	Type       string    `json:"type"`
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	OccurredAt time.Time `json:"occurred_at"`
	Data       any       `json:"data"`
}

func toEventDTO(e db.Event) EventDTO {
	return EventDTO{
		Sequence:   e.ID,
		EventID:    e.EventID,
		Type:       e.EventType,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		OccurredAt: unixTime(e.OccurredAt),
		Data:       decodeData([]byte(e.Data)),
	}
}

func toEventDTOs(in []db.Event) []EventDTO {
	out := make([]EventDTO, len(in))
	for i, e := range in {
		out[i] = toEventDTO(e)
	}
	return out
}

// eventDTOFromStream converts a live event off the bus into the wire DTO.
func eventDTOFromStream(e events.Event) EventDTO {
	return EventDTO{
		Sequence:   e.Sequence,
		EventID:    e.EventID,
		Type:       e.Type,
		EntityType: e.EntityType,
		EntityID:   e.EntityID,
		OccurredAt: e.OccurredAt,
		Data:       decodeData(e.Data),
	}
}

// decodeData parses a stored JSON payload into a value the DTO can re-marshal,
// defaulting to an empty object when the payload is missing or unparseable.
func decodeData(raw []byte) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]any{}
	}
	return v
}

// handleListEvents is the cursor read: it returns events with sequence greater
// than `after`, in stream order, optionally filtered by `type`, `entity_type`, and
// `entity_id`. The response carries a `next` cursor (the sequence to pass as
// `after` next time); when `next` equals the requested `after` and `events` is
// empty, the caller is caught up.
func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var limit int64
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			limit = n
		}
	}

	// `?newest` returns the tail of the stream (most recent events, in chronological
	// order) for "what just happened" views; otherwise it is a forward cursor read.
	var (
		rows []db.Event
		next int64
		err  error
	)
	if q.Has("newest") {
		rows, next, err = s.recentEvents(r.Context(), limit)
	} else {
		rows, next, err = s.listEvents(r.Context(), parseAfter(q.Get("after")), limit, q.Get("type"), q.Get("entity_type"), q.Get("entity_id"))
	}
	if err != nil {
		s.serverError(w, "list events", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"events": toEventDTOs(rows),
		"next":   next,
	})
}

// recentEvents returns up to limit of the most recent events in chronological
// (ascending) order, plus the head sequence as the `next` cursor so a caller can
// switch to forward polling. It backs GET /api/v1/events?newest.
func (s *Server) recentEvents(ctx context.Context, limit int64) ([]db.Event, int64, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	rows, err := s.store.ListRecentEvents(ctx, limit)
	if err != nil {
		return nil, 0, err
	}
	var next int64
	if len(rows) > 0 {
		next = rows[0].ID // newest-first, so the first row is the head
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i] // reverse to chronological order
	}
	return rows, next, nil
}

// listEvents is the shared cursor read behind GET /api/v1/events and the
// list_events MCP tool. It normalizes the limit, runs the filtered query, and
// returns the page plus the `next` cursor (the last sequence returned, or the
// input `after` when the page is empty).
func (s *Server) listEvents(ctx context.Context, after, limit int64, eventType, entityType, entityID string) ([]db.Event, int64, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if after < 0 {
		after = 0
	}
	rows, err := s.store.ListEventsAfter(ctx, db.ListEventsAfterParams{
		After:      after,
		EventType:  eventType,
		EntityType: entityType,
		EntityID:   entityID,
		RowLimit:   limit,
	})
	if err != nil {
		return nil, after, err
	}
	next := after
	if len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	return rows, next, nil
}

func (s *Server) handleGetEvent(w http.ResponseWriter, r *http.Request) {
	seq, err := strconv.ParseInt(chi.URLParam(r, "sequence"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid event sequence")
		return
	}
	row, err := s.store.GetEventBySequence(r.Context(), seq)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "event not found")
		return
	}
	if err != nil {
		s.serverError(w, "get event", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toEventDTO(row))
}

// handleEventStream serves the live event tail over Server-Sent Events. Pass
// ?after=<sequence> to replay the backlog from that cursor first and then follow
// live (a brief replay/live overlap is deduped here; consumers should also dedupe
// on event_id). Without ?after it streams only events published from now on.
//
// This route is registered outside the request-timeout middleware (see Router), so
// it stays open until the client disconnects or the server shuts down. A consumer
// that falls far behind is dropped by the bus and should reconnect with the last
// sequence it saw as ?after to replay the gap.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	bus := s.emitter.Bus()
	if bus == nil {
		s.writeError(w, http.StatusServiceUnavailable, "event stream is disabled")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	// Subscribe before replaying so an event committed during replay is not missed
	// (it may arrive on both paths; the lastSent cursor below drops the duplicate).
	sub, cancel := bus.Subscribe()
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (e.g. nginx)
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lastSent := parseAfter(r.URL.Query().Get("after"))
	if r.URL.Query().Has("after") {
		var err error
		lastSent, err = s.replayEvents(r.Context(), w, flusher, lastSent)
		if err != nil {
			return // context cancelled or the client went away
		}
	}

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub:
			if !ok {
				return // bus closed, or this subscriber was dropped for lagging
			}
			if ev.Sequence <= lastSent {
				continue // already delivered during replay
			}
			if err := writeSSEEvent(w, eventDTOFromStream(ev)); err != nil {
				return
			}
			lastSent = ev.Sequence
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// replayEvents streams the stored backlog after the given cursor, paging until
// caught up, and returns the last sequence written.
func (s *Server) replayEvents(ctx context.Context, w io.Writer, flusher http.Flusher, after int64) (int64, error) {
	const page = 500
	for {
		rows, err := s.store.ListEventsAfter(ctx, db.ListEventsAfterParams{After: after, RowLimit: page})
		if err != nil {
			return after, err
		}
		for _, row := range rows {
			if err := writeSSEEvent(w, toEventDTO(row)); err != nil {
				return after, err
			}
			after = row.ID
		}
		flusher.Flush()
		if int64(len(rows)) < page {
			return after, nil
		}
		select {
		case <-ctx.Done():
			return after, ctx.Err()
		default:
		}
	}
}

// writeSSEEvent writes one event as an SSE frame: the sequence as the SSE id (so a
// reconnecting client can resume via Last-Event-ID/?after), the type as the SSE
// event name, and the full EventDTO as a single-line JSON data payload.
func writeSSEEvent(w io.Writer, dto EventDTO) error {
	payload, err := json.Marshal(dto)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", dto.Sequence, dto.Type, payload)
	return err
}

// parseAfter parses the cursor query parameter; empty, invalid, or negative input
// means "from the beginning" (0).
func parseAfter(v string) int64 {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
