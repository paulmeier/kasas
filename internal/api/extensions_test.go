package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

func TestTransactionExtensions(t *testing.T) {
	srv, _, _ := newTestServer(t)

	t.Run("put stores arbitrary JSON values and echoes back", func(t *testing.T) {
		var out struct {
			ID         string         `json:"id"`
			Extensions map[string]any `json:"extensions"`
		}
		body := map[string]any{"extensions": map[string]any{
			"tax.category":       "meal",                 // string
			"forecast.recurring": true,                   // boolean
			"custom.myapp.score": 88,                     // number
			"custom.myapp.tags":  []any{"a", "b"},        // array
			"custom.myapp.meta":  map[string]any{"x": 1}, // nested object
			" MyApp.Cased ":      "kept",                 // key trimmed, case preserved
			"":                   "dropped",              // empty key dropped
		}}
		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/extensions", body, &out))
		assert.Equal(t, "tx-2", out.ID)
		assert.Equal(t, "meal", out.Extensions["tax.category"])
		assert.Equal(t, true, out.Extensions["forecast.recurring"])
		assert.EqualValues(t, 88, out.Extensions["custom.myapp.score"])
		assert.Equal(t, []any{"a", "b"}, out.Extensions["custom.myapp.tags"])
		assert.Equal(t, map[string]any{"x": float64(1)}, out.Extensions["custom.myapp.meta"])
		assert.Equal(t, "kept", out.Extensions["MyApp.Cased"], "key trimmed, case preserved")
		_, hasEmpty := out.Extensions[""]
		assert.False(t, hasEmpty, "empty key dropped")
	})

	t.Run("extensions surface on get and list", func(t *testing.T) {
		var txn api.TransactionDTO
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-2", &txn))
		assert.Equal(t, "meal", txn.Extensions["tax.category"])

		var list struct {
			Transactions []api.TransactionDTO `json:"transactions"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions", &list))
		for _, tr := range list.Transactions {
			// Extensions are always non-null ({} when none), like labels.
			assert.NotNil(t, tr.Extensions)
			if tr.ID != "tx-2" {
				assert.Empty(t, tr.Extensions)
			}
		}
	})

	t.Run("put replaces the whole set and can clear", func(t *testing.T) {
		var out struct {
			Extensions map[string]any `json:"extensions"`
		}
		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/extensions",
			map[string]any{"extensions": map[string]any{"only.this": "one"}}, &out))
		assert.Equal(t, map[string]any{"only.this": "one"}, out.Extensions)

		var cleared struct {
			Extensions map[string]any `json:"extensions"`
		}
		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/extensions",
			map[string]any{"extensions": map[string]any{}}, &cleared))
		assert.Empty(t, cleared.Extensions)
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, putJSON(t, srv, "/api/v1/transactions/nope/extensions",
			map[string]any{"extensions": map[string]any{"a.b": 1}}, nil))
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/transactions/tx-1/extensions",
			strings.NewReader("{not json"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestListExtensions(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var out struct {
		Extensions []api.ExtensionDTO `json:"extensions"`
	}
	// None yet -> empty (but non-null) list.
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/extensions", &out))
	assert.NotNil(t, out.Extensions)
	assert.Empty(t, out.Extensions)

	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-1/extensions",
		map[string]any{"extensions": map[string]any{"tax.category": "meal", "forecast.recurring": true}}, nil))
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-3/extensions",
		map[string]any{"extensions": map[string]any{"tax.category": "travel"}}, nil))

	out.Extensions = nil
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/extensions", &out))
	// Sorted by key: forecast.recurring (1 txn), tax.category (2 txns).
	require.Len(t, out.Extensions, 2)
	assert.Equal(t, api.ExtensionDTO{Namespace: "forecast", Key: "forecast.recurring", TransactionCount: 1}, out.Extensions[0])
	assert.Equal(t, api.ExtensionDTO{Namespace: "tax", Key: "tax.category", TransactionCount: 2}, out.Extensions[1])
}

func TestTransactionExtensionsEventsAndHistory(t *testing.T) {
	srv, fx := newEventsServer(t)
	id := fx.TxIDsByDateDesc[0]

	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/"+id+"/extensions",
		map[string]any{"extensions": map[string]any{"tax.category": "meal", "custom.myapp.score": 88}}, nil))

	// Two extension.set events, each on the transaction entity.
	var ev eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=extension.set", &ev))
	require.Len(t, ev.Events, 2)
	for _, e := range ev.Events {
		assert.Equal(t, "extension.set", e.Type)
		assert.Equal(t, "transaction", e.EntityType)
		assert.Equal(t, id, e.EntityID)
	}

	// History: a synthesized v1 "imported" baseline, then v2 "extended" with both keys added.
	var h api.HistoryDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/history", &h))
	require.Len(t, h.Versions, 2)
	v2 := h.Versions[1]
	assert.Equal(t, "extended", v2.ChangeKind)
	assert.Empty(t, v2.Diff.Fields, "an extension edit changes no scalar fields")
	assert.Equal(t, map[string]string{"tax.category": "meal", "custom.myapp.score": "88"}, v2.Diff.ExtensionsAdded)
	assert.Equal(t, "meal", v2.Transaction.Extensions["tax.category"])

	// Replacing the set (drop one key) records a v3 "extended" with an extensions_removed delta.
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/"+id+"/extensions",
		map[string]any{"extensions": map[string]any{"tax.category": "meal"}}, nil))
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/history", &h))
	require.Len(t, h.Versions, 3)
	assert.Equal(t, "extended", h.Versions[2].ChangeKind)
	assert.Equal(t, map[string]string{"custom.myapp.score": "88"}, h.Versions[2].Diff.ExtensionsRemoved)
}

func TestMCPSetAndListExtensions(t *testing.T) {
	session, fx, _ := connectMCP(t)
	id := fx.TxIDsByDateDesc[0]

	var txn api.TransactionDTO
	callTool(t, session, "set_transaction_extensions", map[string]any{
		"transaction_id": id,
		"extensions": map[string]any{
			"tax.category":       "meal",
			"forecast.recurring": true,
			"custom.myapp.score": 88,
		},
	}, &txn)
	assert.Equal(t, id, txn.ID)
	assert.Equal(t, "meal", txn.Extensions["tax.category"])
	assert.Equal(t, true, txn.Extensions["forecast.recurring"])
	assert.EqualValues(t, 88, txn.Extensions["custom.myapp.score"])

	var vocab struct {
		Extensions []api.ExtensionDTO `json:"extensions"`
	}
	callTool(t, session, "list_extensions", map[string]any{}, &vocab)
	require.Len(t, vocab.Extensions, 3)
	assert.Equal(t, "custom.myapp.score", vocab.Extensions[0].Key)
	assert.Equal(t, "custom", vocab.Extensions[0].Namespace)

	// An unknown transaction id surfaces as a tool error.
	res := callTool(t, session, "set_transaction_extensions", map[string]any{
		"transaction_id": "nope",
		"extensions":     map[string]any{"a.b": 1},
	}, nil)
	assert.True(t, res.IsError)
}
