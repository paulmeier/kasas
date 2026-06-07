package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

func TestManualAccountCRUD(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var created api.AccountDTO
	t.Run("create returns 201 with a manual account", func(t *testing.T) {
		code := postJSON(t, srv, "/api/v1/accounts", map[string]any{
			"name": "Cash", "currency": "USD", "balance": "100.00",
		}, &created)
		require.Equal(t, http.StatusCreated, code)
		assert.Equal(t, "manual", created.Source)
		assert.Equal(t, "Cash", created.Name)
		assert.Equal(t, "100.00", created.Balance)
		assert.True(t, strings.HasPrefix(created.ID, "man_acct_"), "id %q is namespaced", created.ID)
	})

	t.Run("appears in the accounts list as manual", func(t *testing.T) {
		var out struct {
			Accounts []api.AccountDTO `json:"accounts"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts", &out))
		var found *api.AccountDTO
		for i := range out.Accounts {
			if out.Accounts[i].ID == created.ID {
				found = &out.Accounts[i]
			}
		}
		require.NotNil(t, found, "the manual account is listed")
		assert.Equal(t, "manual", found.Source)
	})

	t.Run("edit updates name/balance", func(t *testing.T) {
		var updated api.AccountDTO
		code := putJSON(t, srv, "/api/v1/accounts/"+created.ID, map[string]any{
			"name": "Petty Cash", "currency": "USD", "balance": "80.00",
		}, &updated)
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, "Petty Cash", updated.Name)
		assert.Equal(t, "80.00", updated.Balance)
	})

	t.Run("can hold a manual transaction", func(t *testing.T) {
		var txn api.TransactionDTO
		require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
			"account_id": created.ID, "amount": "-5.00", "date": "2024-04-01",
		}, &txn))
		assert.Equal(t, created.ID, txn.AccountID)
	})

	t.Run("delete removes the account", func(t *testing.T) {
		require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/accounts/"+created.ID, nil))
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/accounts/"+created.ID, nil))
	})
}

func TestManualAccountGateAndValidation(t *testing.T) {
	srv, fx, _ := newTestServer(t)

	t.Run("editing a synced account is 409", func(t *testing.T) {
		assert.Equal(t, http.StatusConflict, putJSON(t, srv, "/api/v1/accounts/"+fx.CheckingID, map[string]any{
			"name": "Renamed", "currency": "USD",
		}, nil))
	})
	t.Run("deleting a synced account is 409", func(t *testing.T) {
		assert.Equal(t, http.StatusConflict, deleteJSON(t, srv, "/api/v1/accounts/"+fx.CheckingID, nil))
	})
	t.Run("missing name is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/accounts", map[string]any{
			"currency": "USD",
		}, nil))
	})
	t.Run("missing currency is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/accounts", map[string]any{
			"name": "Cash",
		}, nil))
	})
	t.Run("invalid balance is 400", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/accounts", map[string]any{
			"name": "Cash", "currency": "USD", "balance": "abc",
		}, nil))
	})
}

func TestManualAccountDeleteCascadeEvents(t *testing.T) {
	srv, _ := newEventsServer(t)

	var acct api.AccountDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/accounts", map[string]any{
		"name": "Wallet", "currency": "USD",
	}, &acct))

	var t1, t2 api.TransactionDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
		"account_id": acct.ID, "amount": "-1.00", "date": "2024-04-01",
	}, &t1))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions", map[string]any{
		"account_id": acct.ID, "amount": "-2.00", "date": "2024-04-02",
	}, &t2))

	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/accounts/"+acct.ID, nil))

	// Each child transaction emitted its own transaction.deleted (the FK cascade is
	// invisible to the stream), then the account emitted account.deleted.
	var td eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=transaction.deleted", &td))
	require.Len(t, td.Events, 2)

	var ad eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=account.deleted", &ad))
	require.Len(t, ad.Events, 1)
	assert.Equal(t, acct.ID, ad.Events[0].EntityID)

	// The cascaded transactions are gone.
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/"+t1.ID, nil))
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/"+t2.ID, nil))
}
