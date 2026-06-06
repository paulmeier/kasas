// Package webhooks pushes the canonical event stream outward: kasas POSTs each
// matching event to a registered HTTP endpoint, HMAC-signed, so external apps
// (budgeting, accounting, tax, fraud detection, notifications, ...) can react to
// changes without polling. It is the outbound counterpart to the pull stream
// (the REST cursor and SSE tail in internal/api).
//
// Delivery is deliberately best-effort: a Dispatcher rides the in-process
// events.Bus and retries with backoff, but there is no durable per-delivery queue.
// A consumer that misses a delivery (its endpoint was down, or kasas restarted)
// reconciles by replaying from its last cursor via GET /api/v1/events?after=, which
// is exactly what the durable event stream exists for. The webhook row records only
// the health of the most recent attempt, for the dashboard.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// Signing/identification headers set on every delivery. The signature is an
// HMAC-SHA256 over "<timestamp>.<body>" (Stripe-style), so a receiver recomputes it
// with the shared secret and can reject deliveries whose timestamp is too old to
// thwart replay. The event id is the idempotency/dedupe key.
const (
	HeaderEvent     = "X-Kasas-Event"
	HeaderEventID   = "X-Kasas-Event-Id"
	HeaderTimestamp = "X-Kasas-Timestamp"
	HeaderSignature = "X-Kasas-Signature"
)

// wildcard, as a sole/any entry in a webhook's event_types, subscribes to every type.
const wildcard = "*"

const (
	// secretPrefix namespaces a webhook signing secret so it is recognizable.
	secretPrefix = "whsec_"
	// secretBytes is the entropy of a generated signing secret.
	secretBytes = 32
)

// GenerateSecret mints a webhook signing secret. Unlike an API key it is stored in
// plaintext (the dispatcher needs it to sign, and the operator must be able to copy
// it into the receiver to verify signatures).
func GenerateSecret() (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

var (
	webhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kasas_webhook_deliveries_total",
		Help: "Total webhook deliveries by result (success|failed), counted once per event after retries.",
	}, []string{"result"})
	webhookAttempts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kasas_webhook_delivery_attempts_total",
		Help: "Total individual webhook HTTP POST attempts, including retries.",
	})
	webhookDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kasas_webhook_deliveries_dropped_total",
		Help: "Total webhook deliveries dropped because the in-process delivery queue was full (consumers reconcile via the /events cursor).",
	})
)

// eventPayload is the JSON body POSTed to a webhook. It mirrors the api.EventDTO
// wire shape (and the SSE data frame), so a consumer parses the same envelope on
// every surface. Data is the raw event payload, embedded verbatim.
type eventPayload struct {
	Sequence   int64           `json:"sequence"`
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// Sign returns the hex-encoded HMAC-SHA256 of "<timestamp>.<body>" under secret —
// the value carried (with a "sha256=" prefix) in the X-Kasas-Signature header.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Deliver sends one event to a webhook as a single signed POST and returns the HTTP
// status code (0 if the request never completed) and any error (a non-2xx status is
// returned as an error). It does NOT retry: the Dispatcher's worker wraps it in a
// retry loop, and the REST test endpoint calls it once for instant feedback. The
// signature timestamp is the send time, so each retry re-signs afresh.
func Deliver(ctx context.Context, client *http.Client, userAgent string, wh db.Webhook, ev events.Event) (int, error) {
	body, err := marshalEvent(ev)
	if err != nil {
		return 0, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.Url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set(HeaderEvent, ev.Type)
	req.Header.Set(HeaderEventID, ev.EventID)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderSignature, "sha256="+Sign(wh.Secret, ts, body))

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused, but don't read an unbounded
	// response from an arbitrary endpoint.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("endpoint returned status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// marshalEvent renders an event into its webhook JSON body, defaulting a missing
// payload to an empty object.
func marshalEvent(ev events.Event) ([]byte, error) {
	data := ev.Data
	if len(data) == 0 {
		data = json.RawMessage("{}")
	}
	return json.Marshal(eventPayload{
		Sequence:   ev.Sequence,
		EventID:    ev.EventID,
		Type:       ev.Type,
		EntityType: ev.EntityType,
		EntityID:   ev.EntityID,
		OccurredAt: ev.OccurredAt,
		Data:       data,
	})
}

// Matches reports whether wh is subscribed to an event of the given type. A webhook
// with no subscribed types, or one whose list contains "*", receives every type.
func Matches(wh db.Webhook, eventType string) bool {
	return matchesTypes(DecodeEventTypes(wh.EventTypes), eventType)
}

func matchesTypes(subscribed []string, eventType string) bool {
	if len(subscribed) == 0 {
		return true
	}
	for _, s := range subscribed {
		if s == wildcard || s == eventType {
			return true
		}
	}
	return false
}

// DecodeEventTypes parses a webhook's stored event_types (a JSON array string). An
// empty or unparseable value yields nil, which Matches treats as "all types".
func DecodeEventTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// EncodeEventTypes renders a subscribed-types list as the JSON array string stored
// in the webhooks table. A nil/empty list encodes as "[]" (all types).
func EncodeEventTypes(types []string) (string, error) {
	if len(types) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(types)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NewTestEvent builds a synthetic webhook.test event for the "send a test delivery"
// action, so an operator can verify connectivity and signature handling from the
// dashboard without waiting for a real change.
func NewTestEvent() events.Event {
	return events.Event{
		EventID:    uuid.NewString(),
		Type:       "webhook.test",
		EntityType: "webhook",
		OccurredAt: time.Now().UTC(),
		Data:       json.RawMessage(`{"message":"This is a test event from kasas."}`),
	}
}
