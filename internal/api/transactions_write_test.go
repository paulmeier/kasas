package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

func TestManualTransactionCRUD(t *testing.T) {
	srv, fx, _ := newTestServer(t)

	var created api.TransactionDTO
	t.Run("create returns 201 with a manual transaction", func(t *testing.T) {
		code := postJSON(t, srv, "/api/v1/transactions", map[string]any{
			"account_id": fx.CheckingID, "amount": "-9.99", "date": "2024-03-15",
			"description": "Lunch", "payee": "Deli", "pending": true,
		}, &created)
		require.Equal(t, http.StatusCreated, code)
		assert.Equal(t, "manual", created.Source)
		assert.True(t, strings.HasPrefix(created.ID, "man_"), "id %q is namespaced", created.ID)
		assert.Equal(t, "-9.99", created.Amount)
		assert.Equal(t, fx.CheckingID, created.AccountID)
		assert.True(t, created.Pending)
		assert.Equal(t, "2024-03-15", created.Date.UTC().Format("2006-01-02"))
		assert.Equal(t, map[string]string{}, created.Labels)
	})

	t.Run("the manual transaction is fetchable and marked manual", func(t *testing.T) {
		var got api.TransactionDTO
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+created.ID, &got))
		assert.Equal(t, "manual", got.Source)
	})

	t.Run("edit replaces the core fields", func(t *testing.T) {
		var updated api.TransactionDTO
		code := putJSON(t, srv, "/api/v1/transactions/"+created.ID, map[string]any{
			"account_id": fx.SavingsID, "amount": "-12.00", "date": "2024-03-16",
			"description": "Lunch (corrected)",
		}, &updated)
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, "-12.00", updated.Amount)
		assert.Equal(t, fx.SavingsID, updated.AccountID)
		assert.Equal(t, "Lunch (corrected)", updated.Description)
		assert.False(t, updated.Pending, "omitted pending defaults to false")
	})

	t.Run("delete removes it", func(t *testing.T) {
		require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/transactions/"+created.ID, nil))
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/"+created.ID, nil))
	})
}

func TestManualTransactionGateAndValidation(t *testing.T) {
	srv, fx, _ := newTestServer(t)

	t.Run("editing a synced transaction is 409", func(t *testing.T) {
		assert.Equal(t, http.StatusConflict, putJSON(t, srv, "/api/v1/transactions/tx-1", map[string]any{
			"account_id": fx.CheckingID, "amount": "-1.00", "date": "2024-01-01",
		}, nil))
	})
	t.Run("deleting a synced transaction is 409", func(t *testing.T) {
		assert.Equal(t, http.StatusConflict, deleteJSON(t, srv, "/api/v1/transactions/tx-1", nil))
	})
	t.Run("unknown id is 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, putJSON(t, srv, "/api/v1/transactions/nope", map[string]any{
			"account_id": fx.CheckingID, "amount": "-1.00", "date": "2024-01-01",
		}, nil))
		assert.Equal(t, http.StatusNotFound, deleteJSON(t, srv, "/api/v1/transactions/nope", nil))
	})
	t.Run("invalid amount is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions", map[string]any{
			"account_id": fx.CheckingID, "amount": "12,30", "date": "2024-03-15",
		}, nil))
	})
	t.Run("missing date is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions", map[string]any{
			"account_id": fx.CheckingID, "amount": "1.00",
		}, nil))
	})
	t.Run("nonexistent account is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions", map[string]any{
			"account_id": "no-such-acct", "amount": "1.00", "date": "2024-03-15",
		}, nil))
	})
}

func TestManualTransactionEventsAndHistory(t *testing.T) {
	srv, fx := newEventsServer(t)

	var created api.TransactionDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
		"account_id": fx.CheckingID, "amount": "42.00", "date": "2024-05-01", "description": "Manual",
	}, &created))

	// transaction.created fired for the new row (seeding does not emit events).
	var createdEv eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=transaction.created", &createdEv))
	require.Len(t, createdEv.Events, 1)
	assert.Equal(t, created.ID, createdEv.Events[0].EntityID)

	// History v1 is the imported baseline; provenance reads "imported from manual".
	var h api.HistoryDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+created.ID+"/history", &h))
	require.Len(t, h.Versions, 1)
	assert.Equal(t, "imported", h.Versions[0].ChangeKind)

	var prov struct {
		Source          string `json:"source"`
		Transformations []struct {
			Kind    string `json:"kind"`
			Summary string `json:"summary"`
		} `json:"transformations"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+created.ID+"/provenance", &prov))
	assert.Equal(t, "manual", prov.Source)
	require.NotEmpty(t, prov.Transformations)
	assert.Equal(t, "imported from manual", prov.Transformations[0].Summary)

	// An edit appends an "edited" version and emits transaction.updated.
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/"+created.ID, map[string]any{
		"account_id": fx.CheckingID, "amount": "43.00", "date": "2024-05-01", "description": "Manual edited",
	}, nil))
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+created.ID+"/history", &h))
	require.Len(t, h.Versions, 2)
	assert.Equal(t, "edited", h.Versions[1].ChangeKind)

	var updatedEv eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=transaction.updated", &updatedEv))
	require.Len(t, updatedEv.Events, 1)

	// Delete emits transaction.deleted and clears the row's history.
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/transactions/"+created.ID, nil))
	var deletedEv eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=transaction.deleted", &deletedEv))
	require.Len(t, deletedEv.Events, 1)
	assert.Equal(t, created.ID, deletedEv.Events[0].EntityID)
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/"+created.ID+"/history", nil))
}

func TestManualTransactionDeleteStripsInboundEdges(t *testing.T) {
	srv, fx := newEventsServer(t)

	// Two manual transactions; B asserts an edge at A.
	var a, b api.TransactionDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
		"account_id": fx.CheckingID, "amount": "-1.00", "date": "2024-07-01",
	}, &a))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
		"account_id": fx.CheckingID, "amount": "1.00", "date": "2024-07-02",
	}, &b))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/"+b.ID+"/relationships",
		map[string]any{"kind": "refund_of", "target": a.ID}, nil))

	// Deleting A strips B's now-dangling inbound edge and emits relationship.removed.
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/transactions/"+a.ID, nil))

	var rels struct {
		Relationships []api.RelationshipDTO `json:"relationships"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+b.ID+"/relationships", &rels))
	assert.Empty(t, rels.Relationships, "B's edge to the deleted A was stripped")

	var removed eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=relationship.removed", &removed))
	require.Len(t, removed.Events, 1)
	assert.Equal(t, b.ID, removed.Events[0].EntityID)
}

func TestMCPManualTransactionAndAccount(t *testing.T) {
	session, fx, _ := connectMCP(t)

	var acct api.AccountDTO
	res := callTool(t, session, "create_account", map[string]any{"name": "Cash", "currency": "USD", "balance": "50.00"}, &acct)
	require.False(t, res.IsError)
	assert.Equal(t, "manual", acct.Source)

	var txn api.TransactionDTO
	res = callTool(t, session, "create_transaction", map[string]any{
		"account_id": acct.ID, "amount": "-7.50", "date": "2024-06-01", "description": "Snack",
	}, &txn)
	require.False(t, res.IsError)
	assert.Equal(t, "manual", txn.Source)
	assert.Equal(t, "-7.50", txn.Amount)

	res = callTool(t, session, "update_transaction", map[string]any{
		"id": txn.ID, "account_id": acct.ID, "amount": "-8.00", "date": "2024-06-01",
	}, &txn)
	require.False(t, res.IsError)
	assert.Equal(t, "-8.00", txn.Amount)

	// Editing a synced transaction surfaces as a tool error (manual-only gate).
	res = callTool(t, session, "update_transaction", map[string]any{
		"id": "tx-1", "account_id": fx.CheckingID, "amount": "-1.00", "date": "2024-01-01",
	}, nil)
	assert.True(t, res.IsError)

	var del struct {
		Deleted bool `json:"deleted"`
	}
	res = callTool(t, session, "delete_transaction", map[string]any{"id": txn.ID}, &del)
	require.False(t, res.IsError)
	assert.True(t, del.Deleted)

	res = callTool(t, session, "delete_account", map[string]any{"id": acct.ID}, nil)
	require.False(t, res.IsError)
}
