package api_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/sources/webhook"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
	"github.com/paulmeier/kasas/internal/webhooks"
)

// newWebhookServer wires a real ingestion engine over the actual inbound-webhook
// source (file-backed vault, in-memory event bus) so the ingest endpoint is exercised
// end-to-end: HTTP in -> HMAC verify -> persist -> queryable over REST.
func newWebhookServer(t *testing.T) (*httptest.Server, db.Store) {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	src, err := webhook.New(webhook.Options{Secrets: secrets})
	require.NoError(t, err)

	bus := events.NewBus()
	eng := poller.NewEngine(poller.New(poller.Options{
		Store:   store,
		Source:  src,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Emitter: events.NewEmitter(bus),
	}))
	s := api.New(api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Config:  &config.Config{},
		Sources: eng,
		Emitter: events.NewEmitter(bus),
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, store
}

// rotateSecret mints a signing secret via the admin endpoint and returns it.
func rotateSecret(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	var out struct {
		Secret     string `json:"secret"`
		IngestPath string `json:"ingest_path"`
	}
	status := postJSON(t, srv, "/api/v1/sources/webhook/secret/rotate", nil, &out)
	require.Equal(t, http.StatusOK, status)
	require.NotEmpty(t, out.Secret)
	require.Equal(t, "/api/v1/sources/webhook/ingest", out.IngestPath)
	return out.Secret
}

// postSigned posts body to the ingest endpoint with a valid (or, if tamper, broken)
// HMAC signature under secret, and returns the response status.
func postSigned(t *testing.T, srv *httptest.Server, secret string, body string) *http.Response {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sources/webhook/ingest", bytes.NewReader([]byte(body)))
	require.NoError(t, err)
	req.Header.Set(webhooks.HeaderTimestamp, ts)
	req.Header.Set(webhooks.HeaderSignature, "sha256="+webhooks.Sign(secret, ts, []byte(body)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

const ingestPayload = `{
  "accounts": [{
    "external_id": "wallet",
    "name": "Pushed wallet",
    "currency": "USD",
    "balance": "42.00",
    "org": {"name": "Acme"},
    "transactions": [
      {"external_id": "tx-100", "amount": "-9.99", "date": 1750000000, "payee": "Coffee", "description": "Latte"}
    ]
  }]
}`

func TestWebhookIngestEndToEnd(t *testing.T) {
	srv, store := newWebhookServer(t)
	secret := rotateSecret(t, srv)

	resp := postSigned(t, srv, secret, ingestPayload)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	// The persisted transaction is queryable over REST (HTTP in -> HTTP out), with
	// the webhook source's namespaced id and provenance stamp.
	var txns struct {
		Transactions []struct {
			ID     string `json:"id"`
			Payee  string `json:"payee"`
			Source string `json:"source"`
		} `json:"transactions"`
	}
	status := getJSON(t, srv, "/api/v1/transactions", &txns)
	require.Equal(t, http.StatusOK, status)
	require.Len(t, txns.Transactions, 1)
	assert.Equal(t, "webhook:tx-100", txns.Transactions[0].ID)
	assert.Equal(t, "Coffee", txns.Transactions[0].Payee)
	assert.Equal(t, "webhook", txns.Transactions[0].Source)

	// A transaction.created event fired, and the run shows in sync history.
	evs, err := store.ListEventsAfter(context.Background(), db.ListEventsAfterParams{After: 0, RowLimit: 100})
	require.NoError(t, err)
	assert.True(t, hasEventType(evs, events.TypeTransactionCreated), "transaction.created event must fire")

	latest, err := store.LatestSyncLog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "success", latest.Status)
}

func TestWebhookIngestReDeliveryIsIdempotent(t *testing.T) {
	srv, _ := newWebhookServer(t)
	secret := rotateSecret(t, srv)

	r1 := postSigned(t, srv, secret, ingestPayload)
	r1.Body.Close()
	r2 := postSigned(t, srv, secret, ingestPayload)
	r2.Body.Close()
	require.Equal(t, http.StatusAccepted, r2.StatusCode)

	var txns struct {
		Transactions []struct {
			ID string `json:"id"`
		} `json:"transactions"`
	}
	getJSON(t, srv, "/api/v1/transactions", &txns)
	assert.Len(t, txns.Transactions, 1, "re-delivering the same batch dedupes by (source, id)")
}

func TestWebhookIngestRejectsTamperedBody(t *testing.T) {
	srv, _ := newWebhookServer(t)
	secret := rotateSecret(t, srv)

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/sources/webhook/ingest", bytes.NewReader([]byte(ingestPayload+" ")))
	require.NoError(t, err)
	req.Header.Set(webhooks.HeaderTimestamp, ts)
	// Signature is computed over the original body, but a trailing byte was appended.
	req.Header.Set(webhooks.HeaderSignature, "sha256="+webhooks.Sign(secret, ts, []byte(ingestPayload)))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWebhookIngestRejectsBeforeSecretGenerated(t *testing.T) {
	srv, _ := newWebhookServer(t)
	// No rotate: the source has no secret yet, so even a "signed" request is rejected.
	resp := postSigned(t, srv, "whsec_guess", ingestPayload)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestWebhookIngestRejectsOversizedBody(t *testing.T) {
	srv, _ := newWebhookServer(t)
	secret := rotateSecret(t, srv)

	big := string(bytes.Repeat([]byte("a"), (1<<20)+1)) // just over the 1 MiB cap
	resp := postSigned(t, srv, secret, big)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}

func TestWebhookSecretRevealAndRotate(t *testing.T) {
	srv, _ := newWebhookServer(t)

	// Before any rotate, reveal returns an empty secret (the "generate" state).
	var before struct {
		Secret     string `json:"secret"`
		IngestPath string `json:"ingest_path"`
	}
	status := getJSON(t, srv, "/api/v1/sources/webhook/secret", &before)
	require.Equal(t, http.StatusOK, status)
	assert.Empty(t, before.Secret)
	assert.Equal(t, "/api/v1/sources/webhook/ingest", before.IngestPath)

	minted := rotateSecret(t, srv)

	var after struct {
		Secret string `json:"secret"`
	}
	status = getJSON(t, srv, "/api/v1/sources/webhook/secret", &after)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, minted, after.Secret, "reveal returns the rotated secret")

	// A second rotate invalidates the first secret.
	second := rotateSecret(t, srv)
	assert.NotEqual(t, minted, second)
	resp := postSigned(t, srv, minted, ingestPayload)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func hasEventType(evs []db.Event, typ string) bool {
	for _, e := range evs {
		if e.EventType == typ {
			return true
		}
	}
	return false
}
