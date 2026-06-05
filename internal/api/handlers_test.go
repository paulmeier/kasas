package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/poller"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeSyncer records calls without touching the network or database.
type fakeSyncer struct {
	mu     sync.Mutex
	calls  int
	result poller.SyncResult
	err    error
}

func (f *fakeSyncer) Sync(context.Context) (poller.SyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func (f *fakeSyncer) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newAPIServer(t *testing.T) (*api.Server, testutil.Fixtures, *fakeSyncer) {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	fx := testutil.Seed(t, store)
	fs := &fakeSyncer{result: poller.SyncResult{Accounts: 2, NewTransactions: 3, Duration: time.Second}}
	s := api.New(api.Options{
		Store:      store,
		Syncer:     fs,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		MCPEnabled: true,
	})
	return s, fx, fs
}

func newTestServer(t *testing.T) (*httptest.Server, testutil.Fixtures, *fakeSyncer) {
	t.Helper()
	s, fx, fs := newAPIServer(t)
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)
	return srv, fx, fs
}

func getJSON(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if out != nil && len(body) > 0 {
		require.NoError(t, json.Unmarshal(body, out), "body: %s", body)
	}
	return resp.StatusCode
}

func putJSON(t *testing.T, srv *httptest.Server, path string, body, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if out != nil && len(respBody) > 0 {
		require.NoError(t, json.Unmarshal(respBody, out), "body: %s", respBody)
	}
	return resp.StatusCode
}

func deleteJSON(t *testing.T, srv *httptest.Server, path string, out any) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if out != nil && len(respBody) > 0 {
		require.NoError(t, json.Unmarshal(respBody, out), "body: %s", respBody)
	}
	return resp.StatusCode
}

func TestHealthAndReady(t *testing.T) {
	srv, _, _ := newTestServer(t)

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))

	var ready map[string]string
	assert.Equal(t, http.StatusOK, getJSON(t, srv, "/readyz", &ready))
	assert.Equal(t, "ready", ready["status"])
}

func TestMetricsEndpoint(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/metrics")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	// A plain counter is always exported; a *Vec only appears once a labelled
	// series has been observed, so assert on the former.
	assert.Contains(t, string(body), "kasas_transactions_inserted_total")
}

func TestListOrganizations(t *testing.T) {
	srv, fx, _ := newTestServer(t)
	var out struct {
		Organizations []api.OrganizationDTO `json:"organizations"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/organizations", &out))
	require.Len(t, out.Organizations, 1)
	assert.Equal(t, fx.OrgID, out.Organizations[0].ID)
	assert.Equal(t, "Acme Bank", out.Organizations[0].Name)
}

func TestListAccounts(t *testing.T) {
	srv, fx, _ := newTestServer(t)

	var out struct {
		Accounts []api.AccountDTO `json:"accounts"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts", &out))
	require.Len(t, out.Accounts, 2)
	// Ordered by name: Checking, Savings.
	assert.Equal(t, "Checking", out.Accounts[0].Name)
	assert.Equal(t, "Savings", out.Accounts[1].Name)
	assert.Equal(t, "1000.00", out.Accounts[0].Balance)

	out.Accounts = nil
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts?org_id="+fx.OrgID, &out))
	assert.Len(t, out.Accounts, 2)

	out.Accounts = nil
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts?org_id=missing", &out))
	assert.Empty(t, out.Accounts)
}

func TestGetAccount(t *testing.T) {
	srv, fx, _ := newTestServer(t)

	var acct api.AccountDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts/"+fx.CheckingID, &acct))
	assert.Equal(t, fx.CheckingID, acct.ID)
	assert.Equal(t, "USD", acct.Currency)
	assert.Equal(t, testutil.Date2024Dec, acct.BalanceDate.Unix())

	var errBody map[string]string
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/accounts/nope", &errBody))
	assert.NotEmpty(t, errBody["error"])
}

func TestAccountTransactions(t *testing.T) {
	srv, fx, _ := newTestServer(t)
	var out struct {
		Transactions []api.TransactionDTO `json:"transactions"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/accounts/"+fx.CheckingID+"/transactions", &out))
	assert.Equal(t, []string{"tx-3", "tx-2", "tx-1"}, txnIDs(out.Transactions))
}

func TestListTransactions(t *testing.T) {
	srv, fx, _ := newTestServer(t)
	type resp struct {
		Transactions []api.TransactionDTO `json:"transactions"`
	}

	t.Run("all ordered date desc", func(t *testing.T) {
		var out resp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions", &out))
		assert.Equal(t, fx.TxIDsByDateDesc, txnIDs(out.Transactions))
	})

	t.Run("limit", func(t *testing.T) {
		var out resp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions?limit=2", &out))
		assert.Equal(t, []string{"tx-3", "tx-2"}, txnIDs(out.Transactions))
	})

	t.Run("account filter", func(t *testing.T) {
		var out resp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions?account_id="+fx.SavingsID, &out))
		assert.Equal(t, []string{"tx-4"}, txnIDs(out.Transactions))
	})

	t.Run("date window via YYYY-MM-DD", func(t *testing.T) {
		var out resp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions?since=2024-06-01&until=2024-06-01", &out))
		assert.ElementsMatch(t, []string{"tx-2", "tx-4"}, txnIDs(out.Transactions))
	})
}

func TestGetTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var txn api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-2", &txn))
	assert.Equal(t, "-56.78", txn.Amount)
	assert.True(t, txn.Pending, "pending integer is mapped to a bool")
	assert.Equal(t, "gift", txn.Memo)

	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/nope", nil))
}

func TestTransactionTags(t *testing.T) {
	srv, _, _ := newTestServer(t)

	t.Run("put normalizes and echoes back", func(t *testing.T) {
		var out struct {
			ID   string   `json:"id"`
			Tags []string `json:"tags"`
		}
		// Whitespace, an empty entry, and a case-insensitive duplicate.
		body := map[string][]string{"tags": {" Groceries ", "groceries", "food", ""}}
		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/tags", body, &out))
		assert.Equal(t, "tx-2", out.ID)
		assert.Equal(t, []string{"Groceries", "food"}, out.Tags)
	})

	t.Run("tags surface on get and list", func(t *testing.T) {
		var txn api.TransactionDTO
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-2", &txn))
		assert.Equal(t, []string{"Groceries", "food"}, txn.Tags)

		var list struct {
			Transactions []api.TransactionDTO `json:"transactions"`
		}
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions", &list))
		for _, tr := range list.Transactions {
			if tr.ID == "tx-2" {
				assert.Equal(t, []string{"Groceries", "food"}, tr.Tags)
			} else {
				// Untagged transactions are [] (never null).
				assert.NotNil(t, tr.Tags)
				assert.Empty(t, tr.Tags)
			}
		}
	})

	t.Run("put replaces the whole set and can clear", func(t *testing.T) {
		var out struct {
			Tags []string `json:"tags"`
		}
		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/tags",
			map[string][]string{"tags": {"rent"}}, &out))
		assert.Equal(t, []string{"rent"}, out.Tags)

		require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-2/tags",
			map[string][]string{"tags": {}}, &out))
		assert.Empty(t, out.Tags)
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, putJSON(t, srv, "/api/v1/transactions/nope/tags",
			map[string][]string{"tags": {"x"}}, nil))
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/transactions/tx-1/tags",
			strings.NewReader("{not json"))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	})
}

func TestListTags(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var out struct {
		Tags []api.TagDTO `json:"tags"`
	}
	// No tags yet -> empty (but non-null) list.
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/tags", &out))
	assert.NotNil(t, out.Tags)
	assert.Empty(t, out.Tags)

	// Tag two transactions; the vocabulary is the sorted union with per-tag counts.
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-1/tags",
		map[string][]string{"tags": {"coffee", "food"}}, nil))
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-3/tags",
		map[string][]string{"tags": {"coffee", "rent"}}, nil))

	out.Tags = nil
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/tags", &out))
	assert.Equal(t, []api.TagDTO{
		{Name: "coffee", TransactionCount: 2},
		{Name: "food", TransactionCount: 1},
		{Name: "rent", TransactionCount: 1},
	}, out.Tags)
}

func TestDeleteTag(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Tag two transactions; "coffee" is shared, with a different spelling on tx-3.
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-1/tags",
		map[string][]string{"tags": {"coffee", "food"}}, nil))
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-3/tags",
		map[string][]string{"tags": {"Coffee", "rent"}}, nil))

	// Delete "coffee" — case-insensitively matches "Coffee" too, so both rows.
	var del struct {
		Name        string `json:"name"`
		RemovedFrom int    `json:"removed_from"`
	}
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/tags/coffee", &del))
	assert.Equal(t, "coffee", del.Name)
	assert.Equal(t, 2, del.RemovedFrom)

	// Gone from the vocabulary; the others remain with correct counts.
	var list struct {
		Tags []api.TagDTO `json:"tags"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/tags", &list))
	assert.Equal(t, []api.TagDTO{
		{Name: "food", TransactionCount: 1},
		{Name: "rent", TransactionCount: 1},
	}, list.Tags)

	// The affected transactions kept their other tags.
	var tx1 api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-1", &tx1))
	assert.Equal(t, []string{"food"}, tx1.Tags)

	// Idempotent: deleting again removes from 0 rows and still returns 200.
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, "/api/v1/tags/coffee", &del))
	assert.Equal(t, 0, del.RemovedFrom)

	// A blank name (here a single encoded space) is rejected.
	assert.Equal(t, http.StatusBadRequest, deleteJSON(t, srv, "/api/v1/tags/%20", nil))
}

func TestSyncStatusAndHistory(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var status struct {
		Latest *api.SyncDTO `json:"latest"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/sync", &status))
	require.NotNil(t, status.Latest)
	assert.Equal(t, "success", status.Latest.Status)
	require.NotNil(t, status.Latest.CompletedAt)

	var history struct {
		History []api.SyncDTO `json:"history"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/sync/history", &history))
	assert.Len(t, history.History, 1)
}

func TestTriggerSync(t *testing.T) {
	srv, _, fs := newTestServer(t)

	resp, err := http.Post(srv.URL+"/api/v1/sync", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	// The sync runs asynchronously; it should fire shortly.
	require.Eventually(t, func() bool { return fs.Calls() == 1 }, time.Second, 5*time.Millisecond)
}

func TestUnknownRoute(t *testing.T) {
	srv, _, _ := newTestServer(t)
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/does-not-exist", nil))
}

func txnIDs(txns []api.TransactionDTO) []string {
	ids := make([]string, len(txns))
	for i, tx := range txns {
		ids[i] = tx.ID
	}
	return ids
}
