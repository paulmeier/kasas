package api_test

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

// searchResult mirrors the GET /transactions/search response.
type searchResult struct {
	Query        string               `json:"query"`
	Total        int                  `json:"total"`
	Transactions []api.TransactionDTO `json:"transactions"`
}

func searchIDs(res searchResult) []string {
	out := make([]string, len(res.Transactions))
	for i, t := range res.Transactions {
		out[i] = t.ID
	}
	return out
}

func searchPath(q string) string {
	return "/api/v1/transactions/search?q=" + url.QueryEscape(q)
}

func TestSearchTransactions(t *testing.T) {
	srv, _, _ := newTestServer(t)

	cases := []struct {
		name string
		q    string
		want []string // expected ids, in response (date-desc) order
	}{
		{"empty matches all", "", []string{"tx-3", "tx-2", "tx-4", "tx-1"}},
		{"free text description", "Coffee", []string{"tx-1"}},
		{"free text memo", "gift", []string{"tx-2"}},
		{"amount positive", "amount:>0", []string{"tx-3", "tx-4"}},
		{"amount range", "amount:-100..-1", []string{"tx-2", "tx-1"}},
		{"pending", "pending:true", []string{"tx-2"}},
		{"account by name", "account:savings", []string{"tx-4"}},
		{"date month", "date:2024-12", []string{"tx-3"}},
		{"negation", "-account:checking", []string{"tx-4"}},
		{"boolean group", "(account:savings OR amount:>50) pending:false", []string{"tx-3", "tx-4"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var res searchResult
			require.Equal(t, http.StatusOK, getJSON(t, srv, searchPath(c.q), &res))
			assert.Equal(t, c.want, searchIDs(res), "query %q", c.q)
			assert.Equal(t, len(c.want), res.Total)
		})
	}
}

func TestSearchTransactionsPagination(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var page1 searchResult
	require.Equal(t, http.StatusOK, getJSON(t, srv, searchPath("")+"&limit=2", &page1))
	assert.Equal(t, 4, page1.Total)
	assert.Equal(t, []string{"tx-3", "tx-2"}, searchIDs(page1))

	var page2 searchResult
	require.Equal(t, http.StatusOK, getJSON(t, srv, searchPath("")+"&limit=2&offset=2", &page2))
	assert.Equal(t, 4, page2.Total)
	assert.Equal(t, []string{"tx-4", "tx-1"}, searchIDs(page2))
}

func TestSearchTransactionsLabelRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Labels are written via the existing endpoint; search must then find them
	// (exercises decodeLabels in the search adapter).
	var put map[string]any
	require.Equal(t, http.StatusOK, putJSON(t, srv,
		"/api/v1/transactions/tx-1/labels",
		map[string]map[string]string{"labels": {"category": "food"}}, &put))

	for _, q := range []string{"label:category=food", "category:food", "label:category"} {
		var res searchResult
		require.Equal(t, http.StatusOK, getJSON(t, srv, searchPath(q), &res))
		assert.Equal(t, []string{"tx-1"}, searchIDs(res), "query %q", q)
	}
}

func TestSearchTransactionsBadQuery(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var errBody map[string]string
	require.Equal(t, http.StatusBadRequest, getJSON(t, srv, searchPath("(unbalanced"), &errBody))
	assert.NotEmpty(t, errBody["error"])
}

func TestMCPSearchTransactions(t *testing.T) {
	session, _, _ := connectMCP(t)

	var out searchResult
	res := callTool(t, session, "search_transactions", map[string]any{"q": "amount:>0"}, &out)
	require.False(t, res.IsError)
	assert.Equal(t, 2, out.Total)
	assert.Equal(t, []string{"tx-3", "tx-4"}, searchIDs(out))

	// A malformed query surfaces as a tool error.
	bad := callTool(t, session, "search_transactions", map[string]any{"q": "amount:abc"}, nil)
	assert.True(t, bad.IsError)
}
