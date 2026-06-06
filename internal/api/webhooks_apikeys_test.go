package api_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/webhooks"
)

const adminToken = "admin-token-1234567890"

// createKey provisions an API key via the admin token and returns the DTO (including
// the one-time secret in Key).
func createKey(t *testing.T, srv *httptest.Server, name, scope string) api.ApiKeyDTO {
	t.Helper()
	body := strings.NewReader(`{"name":"` + name + `","scope":"` + scope + `"}`)
	resp := doReq(t, srv, http.MethodPost, "/api/v1/security/api-keys", adminToken, body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var dto api.ApiKeyDTO
	require.NoError(t, decodeJSON(resp, &dto))
	require.NotEmpty(t, dto.Key, "create must return the full secret once")
	return dto
}

func TestApiKeyCreateListRevoke(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)

	key := createKey(t, srv, "Budget app", "read")
	assert.Equal(t, "read", key.Scope)
	assert.True(t, strings.HasPrefix(key.Key, "kasas_"))
	assert.True(t, strings.HasPrefix(key.Key, key.Prefix), "prefix is a fragment of the key")

	// List returns metadata but never the secret.
	resp := doReq(t, srv, http.MethodGet, "/api/v1/security/api-keys", adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		APIKeys []api.ApiKeyDTO `json:"api_keys"`
	}
	require.NoError(t, decodeJSON(resp, &list))
	require.Len(t, list.APIKeys, 1)
	assert.Equal(t, key.ID, list.APIKeys[0].ID)
	assert.Empty(t, list.APIKeys[0].Key, "list must not return the secret")

	// Revoke, then the list is empty.
	resp = doReq(t, srv, http.MethodDelete, "/api/v1/security/api-keys/"+strconv.FormatInt(key.ID, 10), adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doReq(t, srv, http.MethodGet, "/api/v1/security/api-keys", adminToken, nil)
	require.NoError(t, decodeJSON(resp, &list))
	assert.Empty(t, list.APIKeys)

	// Revoking an unknown id is a 404.
	resp = doReq(t, srv, http.MethodDelete, "/api/v1/security/api-keys/9999", adminToken, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestApiKeyInvalidScope(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)
	resp := doReq(t, srv, http.MethodPost, "/api/v1/security/api-keys", adminToken, strings.NewReader(`{"scope":"admin"}`))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestApiKeyScopeEnforcement(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)
	readKey := createKey(t, srv, "reader", "read")
	writeKey := createKey(t, srv, "writer", "read_write")

	rule := func() io.Reader { return strings.NewReader(`{"query":"description:coffee","labels":{"cat":"coffee"}}`) }

	cases := []struct {
		name   string
		method string
		path   string
		body   func() io.Reader
		token  string
		want   int
	}{
		{"no creds -> 401", http.MethodGet, "/api/v1/accounts", nil, "", http.StatusUnauthorized},
		{"admin token reads", http.MethodGet, "/api/v1/accounts", nil, adminToken, http.StatusOK},
		{"read key reads", http.MethodGet, "/api/v1/accounts", nil, readKey.Key, http.StatusOK},
		{"write key reads", http.MethodGet, "/api/v1/accounts", nil, writeKey.Key, http.StatusOK},
		{"read key cannot write", http.MethodPost, "/api/v1/rules", rule, readKey.Key, http.StatusForbidden},
		{"write key writes", http.MethodPost, "/api/v1/rules", rule, writeKey.Key, http.StatusCreated},
		{"admin token writes", http.MethodPost, "/api/v1/rules", rule, adminToken, http.StatusCreated},
		// API keys are never accepted on admin/provisioning routes.
		{"write key cannot manage keys", http.MethodGet, "/api/v1/security/api-keys", nil, writeKey.Key, http.StatusUnauthorized},
		{"write key cannot manage webhooks", http.MethodGet, "/api/v1/webhooks", nil, writeKey.Key, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != nil {
				body = tc.body()
			}
			resp := doReq(t, srv, tc.method, tc.path, tc.token, body)
			assert.Equal(t, tc.want, resp.StatusCode)
		})
	}
}

func TestWebhookCRUD(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)

	// Create.
	resp := doReq(t, srv, http.MethodPost, "/api/v1/webhooks", adminToken,
		strings.NewReader(`{"url":"https://example.com/hook","event_types":["transaction.created"]}`))
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var wh api.WebhookDTO
	require.NoError(t, decodeJSON(resp, &wh))
	assert.Equal(t, "https://example.com/hook", wh.URL)
	assert.Equal(t, []string{"transaction.created"}, wh.EventTypes)
	assert.True(t, wh.Enabled)
	require.NotEmpty(t, wh.Secret, "create returns the signing secret")

	// List omits the secret.
	resp = doReq(t, srv, http.MethodGet, "/api/v1/webhooks", adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list struct {
		Webhooks []api.WebhookDTO `json:"webhooks"`
	}
	require.NoError(t, decodeJSON(resp, &list))
	require.Len(t, list.Webhooks, 1)
	assert.Empty(t, list.Webhooks[0].Secret, "list must not return secrets")

	// Get includes the secret.
	id := strconv.FormatInt(wh.ID, 10)
	resp = doReq(t, srv, http.MethodGet, "/api/v1/webhooks/"+id, adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got api.WebhookDTO
	require.NoError(t, decodeJSON(resp, &got))
	assert.Equal(t, wh.Secret, got.Secret)

	// Update url, types, and enabled.
	resp = doReq(t, srv, http.MethodPut, "/api/v1/webhooks/"+id, adminToken,
		strings.NewReader(`{"url":"https://example.com/v2","event_types":["*"],"enabled":false}`))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NoError(t, decodeJSON(resp, &got))
	assert.Equal(t, "https://example.com/v2", got.URL)
	assert.Equal(t, []string{"*"}, got.EventTypes)
	assert.False(t, got.Enabled)

	// Rotate the secret.
	resp = doReq(t, srv, http.MethodPost, "/api/v1/webhooks/"+id+"/rotate-secret", adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var rotated api.WebhookDTO
	require.NoError(t, decodeJSON(resp, &rotated))
	assert.NotEqual(t, wh.Secret, rotated.Secret, "rotate mints a new secret")

	// Delete.
	resp = doReq(t, srv, http.MethodDelete, "/api/v1/webhooks/"+id, adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp = doReq(t, srv, http.MethodGet, "/api/v1/webhooks/"+id, adminToken, nil)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestWebhookValidation(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)
	cases := []string{
		`{"url":""}`,                  // missing url
		`{"url":"not-a-url"}`,         // not absolute http(s)
		`{"url":"ftp://example.com"}`, // wrong scheme
		`{"url":"https://x.com","event_types":["bogus.x"]}`, // unknown event type
	}
	for _, body := range cases {
		resp := doReq(t, srv, http.MethodPost, "/api/v1/webhooks", adminToken, strings.NewReader(body))
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, body)
	}
}

func TestWebhookTestDelivery(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)

	// A throwaway receiver that captures the delivery.
	received := make(chan http.Header, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer endpoint.Close()

	resp := doReq(t, srv, http.MethodPost, "/api/v1/webhooks", adminToken,
		strings.NewReader(`{"url":"`+endpoint.URL+`","event_types":["*"]}`))
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var wh api.WebhookDTO
	require.NoError(t, decodeJSON(resp, &wh))

	resp = doReq(t, srv, http.MethodPost, "/api/v1/webhooks/"+strconv.FormatInt(wh.ID, 10)+"/test", adminToken, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var res struct {
		Status    int    `json:"status"`
		Delivered bool   `json:"delivered"`
		Error     string `json:"error"`
	}
	require.NoError(t, decodeJSON(resp, &res))
	assert.True(t, res.Delivered)
	assert.Equal(t, http.StatusOK, res.Status)

	select {
	case h := <-received:
		assert.Equal(t, "webhook.test", h.Get(webhooks.HeaderEvent))
		assert.NotEmpty(t, h.Get(webhooks.HeaderSignature))
	default:
		t.Fatal("test endpoint did not receive the delivery")
	}
}

func TestWebhooksRequireToken(t *testing.T) {
	srv, _ := newSecuredServer(t, adminToken)
	resp := doReq(t, srv, http.MethodGet, "/api/v1/webhooks", "", nil)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
