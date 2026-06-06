package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/webhooks"
)

// knownEventTypes is the taxonomy a webhook may subscribe to (plus the "*" wildcard,
// handled separately). It mirrors the constants in internal/events; an unknown type
// is rejected at create/update so a typo never silently subscribes to nothing.
var knownEventTypes = map[string]bool{
	events.TypeTransactionCreated: true,
	events.TypeTransactionUpdated: true,
	events.TypeTransactionDeleted: true,
	events.TypeAccountCreated:     true,
	events.TypeAccountUpdated:     true,
	events.TypeLabelApplied:       true,
	events.TypeLabelRemoved:       true,
	events.TypeRuleCreated:        true,
	events.TypeRuleUpdated:        true,
	events.TypeRuleDeleted:        true,
	events.TypeRuleExecuted:       true,
	events.TypeSyncCompleted:      true,
}

var errWebhookNotFound = errors.New("webhook not found")

// WebhookDTO is the JSON representation of a registered webhook endpoint plus the
// health of its most recent delivery. Secret is included only on create, get, update,
// and rotate (so the operator can configure the receiver); the list omits it.
// LastAttemptAt/LastSuccessAt are null until the first attempt/success.
type WebhookDTO struct {
	ID            int64      `json:"id"`
	URL           string     `json:"url"`
	EventTypes    []string   `json:"event_types"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	Secret        string     `json:"secret,omitempty"`
}

func toWebhookDTO(wh db.Webhook, includeSecret bool) WebhookDTO {
	dto := WebhookDTO{
		ID:         wh.ID,
		URL:        wh.Url,
		EventTypes: webhooks.DecodeEventTypes(wh.EventTypes),
		Enabled:    wh.Enabled != 0,
		CreatedAt:  unixTime(wh.CreatedAt),
		UpdatedAt:  unixTime(wh.UpdatedAt),
		LastStatus: int(wh.LastStatus),
		LastError:  wh.LastError,
	}
	if dto.EventTypes == nil {
		dto.EventTypes = []string{}
	}
	if wh.LastAttemptAt > 0 {
		t := unixTime(wh.LastAttemptAt)
		dto.LastAttemptAt = &t
	}
	if wh.LastSuccessAt > 0 {
		t := unixTime(wh.LastSuccessAt)
		dto.LastSuccessAt = &t
	}
	if includeSecret {
		dto.Secret = wh.Secret
	}
	return dto
}

func toWebhookDTOs(in []db.Webhook, includeSecret bool) []WebhookDTO {
	out := make([]WebhookDTO, len(in))
	for i, wh := range in {
		out[i] = toWebhookDTO(wh, includeSecret)
	}
	return out
}

// webhookInput is the create/update request body (and the MCP create tool input).
// Enabled is a pointer so an omitted field defaults to enabled.
type webhookInput struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	Enabled    *bool    `json:"enabled"`
}

// validateWebhook checks the create/update input: the URL must be an absolute http(s)
// URL, and every event type must be known (or "*"). It returns the trimmed URL and
// the JSON-encoded subscribed types (an empty list means "all types").
func validateWebhook(in webhookInput) (rawURL, encodedTypes string, err error) {
	rawURL = strings.TrimSpace(in.URL)
	if rawURL == "" {
		return "", "", errors.New("a webhook must have a url")
	}
	u, perr := url.Parse(rawURL)
	if perr != nil || !u.IsAbs() || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", errors.New("url must be an absolute http(s) URL")
	}
	types, terr := normalizeEventTypes(in.EventTypes)
	if terr != nil {
		return "", "", terr
	}
	encoded, eerr := webhooks.EncodeEventTypes(types)
	if eerr != nil {
		return "", "", eerr
	}
	return rawURL, encoded, nil
}

// normalizeEventTypes trims, validates, and dedupes the subscribed types. Unknown
// types (not "*" and not in the taxonomy) are an error; an empty result means "all".
func normalizeEventTypes(in []string) ([]string, error) {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if t != "*" && !knownEventTypes[t] {
			return nil, fmt.Errorf("unknown event type %q", t)
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// createWebhook validates the input, mints a signing secret, and stores the webhook.
// Validation failures are validationError so handlers map them to 400. Shared by REST
// and MCP.
func (s *Server) createWebhook(ctx context.Context, in webhookInput) (db.Webhook, error) {
	rawURL, encodedTypes, err := validateWebhook(in)
	if err != nil {
		return db.Webhook{}, validationError{err}
	}
	secret, err := webhooks.GenerateSecret()
	if err != nil {
		return db.Webhook{}, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := time.Now().Unix()
	return s.store.InsertWebhook(ctx, db.InsertWebhookParams{
		Url:        rawURL,
		Secret:     secret,
		EventTypes: encodedTypes,
		Enabled:    boolToInt64(enabled),
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}

// updateWebhook validates and replaces a webhook's editable fields (not its secret),
// returning the canonical stored row. errWebhookNotFound signals an unknown id.
func (s *Server) updateWebhook(ctx context.Context, id int64, in webhookInput) (db.Webhook, error) {
	rawURL, encodedTypes, err := validateWebhook(in)
	if err != nil {
		return db.Webhook{}, validationError{err}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	n, err := s.store.UpdateWebhook(ctx, db.UpdateWebhookParams{
		Url:        rawURL,
		EventTypes: encodedTypes,
		Enabled:    boolToInt64(enabled),
		UpdatedAt:  time.Now().Unix(),
		ID:         id,
	})
	if err != nil {
		return db.Webhook{}, err
	}
	if n == 0 {
		return db.Webhook{}, errWebhookNotFound
	}
	return s.store.GetWebhook(ctx, id)
}

// deleteWebhook deletes a webhook by id, reporting whether one was removed. Shared by
// REST and MCP.
func (s *Server) deleteWebhook(ctx context.Context, id int64) (bool, error) {
	n, err := s.store.DeleteWebhook(ctx, id)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// rotateWebhookSecret mints and stores a new signing secret, returning the updated
// row. errWebhookNotFound signals an unknown id.
func (s *Server) rotateWebhookSecret(ctx context.Context, id int64) (db.Webhook, error) {
	secret, err := webhooks.GenerateSecret()
	if err != nil {
		return db.Webhook{}, err
	}
	n, err := s.store.UpdateWebhookSecret(ctx, db.UpdateWebhookSecretParams{
		Secret:    secret,
		UpdatedAt: time.Now().Unix(),
		ID:        id,
	})
	if err != nil {
		return db.Webhook{}, err
	}
	if n == 0 {
		return db.Webhook{}, errWebhookNotFound
	}
	return s.store.GetWebhook(ctx, id)
}

// webhookTestResult is the outcome of a synchronous test delivery.
type webhookTestResult struct {
	Status    int    `json:"status"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

// testWebhook sends one synthetic webhook.test event to the endpoint and returns the
// result. errWebhookNotFound signals an unknown id. The delivery itself failing is
// not an error — it is reported in the result so the UI can show the status/message.
func (s *Server) testWebhook(ctx context.Context, id int64) (webhookTestResult, error) {
	wh, err := s.store.GetWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return webhookTestResult{}, errWebhookNotFound
	}
	if err != nil {
		return webhookTestResult{}, err
	}
	status, derr := webhooks.Deliver(ctx, s.webhookTestClient(), "kasas/"+s.version, wh, webhooks.NewTestEvent())
	res := webhookTestResult{Status: status, Delivered: derr == nil}
	if derr != nil {
		res.Error = derr.Error()
	}
	return res, nil
}

// webhookTestClient is the HTTP client for one-off test deliveries, using the
// configured per-attempt timeout (with a 10s fallback when config is absent).
func (s *Server) webhookTestClient() *http.Client {
	timeout := 10 * time.Second
	if s.config != nil && s.config.Webhooks.Timeout > 0 {
		timeout = s.config.Webhooks.Timeout
	}
	return &http.Client{Timeout: timeout}
}

// --- REST handlers ---

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	hooks, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		s.serverError(w, "list webhooks", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"webhooks": toWebhookDTOs(hooks, false)})
}

func (s *Server) handleGetWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := webhookIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	wh, err := s.store.GetWebhook(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		s.serverError(w, "get webhook", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toWebhookDTO(wh, true))
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeWebhookInput(w, r)
	if !ok {
		return
	}
	wh, err := s.createWebhook(r.Context(), in)
	if err != nil {
		if isValidationError(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "create webhook", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toWebhookDTO(wh, true))
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := webhookIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	in, ok := s.decodeWebhookInput(w, r)
	if !ok {
		return
	}
	wh, err := s.updateWebhook(r.Context(), id, in)
	if err != nil {
		switch {
		case isValidationError(err):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errWebhookNotFound):
			s.writeError(w, http.StatusNotFound, "webhook not found")
		default:
			s.serverError(w, "update webhook", err)
		}
		return
	}
	s.writeJSON(w, http.StatusOK, toWebhookDTO(wh, true))
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := webhookIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	deleted, err := s.deleteWebhook(r.Context(), id)
	if err != nil {
		s.serverError(w, "delete webhook", err)
		return
	}
	if !deleted {
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	id, err := webhookIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	res, err := s.testWebhook(r.Context(), id)
	if errors.Is(err, errWebhookNotFound) {
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		s.serverError(w, "test webhook", err)
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	id, err := webhookIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid webhook id")
		return
	}
	wh, err := s.rotateWebhookSecret(r.Context(), id)
	if errors.Is(err, errWebhookNotFound) {
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return
	}
	if err != nil {
		s.serverError(w, "rotate webhook secret", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toWebhookDTO(wh, true))
}

func (s *Server) decodeWebhookInput(w http.ResponseWriter, r *http.Request) (webhookInput, bool) {
	var in webhookInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return webhookInput{}, false
	}
	return in, true
}

func webhookIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// --- MCP tool input/output types + handlers (registered in mcp.go) ---

type listWebhooksOutput struct {
	Webhooks []WebhookDTO `json:"webhooks"`
}

type createWebhookInput struct {
	URL        string   `json:"url" jsonschema:"the absolute http(s) endpoint to POST events to"`
	EventTypes []string `json:"event_types,omitempty" jsonschema:"event types to subscribe to (e.g. transaction.created, label.applied); use [\"*\"] or omit for all types"`
	Enabled    *bool    `json:"enabled,omitempty" jsonschema:"whether deliveries are active (default true)"`
}

type updateWebhookInput struct {
	ID         int64    `json:"id" jsonschema:"the id of the webhook to update"`
	URL        string   `json:"url" jsonschema:"the absolute http(s) endpoint to POST events to"`
	EventTypes []string `json:"event_types,omitempty" jsonschema:"event types to subscribe to; use [\"*\"] or omit for all types"`
	Enabled    *bool    `json:"enabled,omitempty" jsonschema:"whether deliveries are active (default true)"`
}

type deleteWebhookInput struct {
	ID int64 `json:"id" jsonschema:"the id of the webhook to delete"`
}

type deleteWebhookOutput struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

type testWebhookInput struct {
	ID int64 `json:"id" jsonschema:"the id of the webhook to send a test delivery to"`
}

func (s *Server) mcpListWebhooks(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listWebhooksOutput, error) {
	hooks, err := s.store.ListWebhooks(ctx)
	if err != nil {
		return nil, listWebhooksOutput{}, err
	}
	return &mcp.CallToolResult{}, listWebhooksOutput{Webhooks: toWebhookDTOs(hooks, false)}, nil
}

func (s *Server) mcpCreateWebhook(ctx context.Context, _ *mcp.CallToolRequest, in createWebhookInput) (*mcp.CallToolResult, WebhookDTO, error) {
	wh, err := s.createWebhook(ctx, webhookInput(in))
	if err != nil {
		return nil, WebhookDTO{}, err
	}
	return &mcp.CallToolResult{}, toWebhookDTO(wh, true), nil
}

func (s *Server) mcpUpdateWebhook(ctx context.Context, _ *mcp.CallToolRequest, in updateWebhookInput) (*mcp.CallToolResult, WebhookDTO, error) {
	wh, err := s.updateWebhook(ctx, in.ID, webhookInput{URL: in.URL, EventTypes: in.EventTypes, Enabled: in.Enabled})
	if errors.Is(err, errWebhookNotFound) {
		return nil, WebhookDTO{}, fmt.Errorf("webhook %d not found", in.ID)
	}
	if err != nil {
		return nil, WebhookDTO{}, err
	}
	return &mcp.CallToolResult{}, toWebhookDTO(wh, true), nil
}

func (s *Server) mcpDeleteWebhook(ctx context.Context, _ *mcp.CallToolRequest, in deleteWebhookInput) (*mcp.CallToolResult, deleteWebhookOutput, error) {
	deleted, err := s.deleteWebhook(ctx, in.ID)
	if err != nil {
		return nil, deleteWebhookOutput{}, err
	}
	if !deleted {
		return nil, deleteWebhookOutput{}, fmt.Errorf("webhook %d not found", in.ID)
	}
	return &mcp.CallToolResult{}, deleteWebhookOutput{ID: in.ID, Deleted: true}, nil
}

func (s *Server) mcpTestWebhook(ctx context.Context, _ *mcp.CallToolRequest, in testWebhookInput) (*mcp.CallToolResult, webhookTestResult, error) {
	res, err := s.testWebhook(ctx, in.ID)
	if errors.Is(err, errWebhookNotFound) {
		return nil, webhookTestResult{}, fmt.Errorf("webhook %d not found", in.ID)
	}
	if err != nil {
		return nil, webhookTestResult{}, err
	}
	return &mcp.CallToolResult{}, res, nil
}
