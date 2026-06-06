package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

// runResp mirrors the rules run response.
type runResp struct {
	Matched int `json:"matched"`
	Updated int `json:"updated"`
}

type rulesList struct {
	Rules []api.RuleDTO `json:"rules"`
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, rdr)
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

func TestRuleCRUD(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Create: the key is lowercased, the value's case preserved (label normalization).
	var created api.RuleDTO
	code := postJSON(t, srv, "/api/v1/rules", map[string]any{
		"name":   "Coffee",
		"query":  "description:coffee",
		"labels": map[string]string{"Category": "Coffee"},
	}, &created)
	require.Equal(t, http.StatusCreated, code)
	assert.NotZero(t, created.ID)
	assert.Equal(t, "Coffee", created.Name)
	assert.True(t, created.Enabled)
	assert.Equal(t, map[string]string{"category": "Coffee"}, created.Labels)

	// Get
	var got api.RuleDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d", created.ID), &got))
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, "description:coffee", got.Query)

	// List
	var list rulesList
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/rules", &list))
	require.Len(t, list.Rules, 1)

	// Update replaces all editable fields.
	var updated api.RuleDTO
	code = putJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d", created.ID), map[string]any{
		"name":    "Coffee shops",
		"query":   "payee:cafe",
		"labels":  map[string]string{"category": "coffee"},
		"enabled": false,
	}, &updated)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Coffee shops", updated.Name)
	assert.Equal(t, "payee:cafe", updated.Query)
	assert.False(t, updated.Enabled)

	// Delete, then it is gone.
	require.Equal(t, http.StatusOK, deleteJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d", created.ID), nil))
	require.Equal(t, http.StatusNotFound, getJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d", created.ID), nil))
	require.Equal(t, http.StatusNotFound, deleteJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d", created.ID), nil))
}

func TestCreateRuleValidation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	cases := []struct {
		name string
		body map[string]any
	}{
		{"invalid query", map[string]any{"query": "amount:>", "labels": map[string]string{"a": "b"}}},
		{"empty query", map[string]any{"query": "   ", "labels": map[string]string{"a": "b"}}},
		{"no labels", map[string]any{"query": "description:coffee", "labels": map[string]string{}}},
		{"only invalid labels", map[string]any{"query": "description:coffee", "labels": map[string]string{"   ": "x"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e map[string]string
			code := postJSON(t, srv, "/api/v1/rules", tc.body, &e)
			assert.Equal(t, http.StatusBadRequest, code, "resp: %v", e)
			assert.NotEmpty(t, e["error"])
		})
	}
}

func TestRunRuleAppliesLabels(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var rule api.RuleDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules", map[string]any{
		"query":  "amount:<0",
		"labels": map[string]string{"flow": "out"},
	}, &rule))

	// Run: the two outflows (tx-1 -12.34, tx-2 -56.78) match and are labeled.
	var res runResp
	require.Equal(t, http.StatusOK, postJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d/run", rule.ID), nil, &res))
	assert.Equal(t, 2, res.Matched)
	assert.Equal(t, 2, res.Updated)

	var tx api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-1", &tx))
	assert.Equal(t, "out", tx.Labels["flow"])

	// A non-matching transaction (an inflow) is untouched.
	var dep api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-3", &dep))
	_, has := dep.Labels["flow"]
	assert.False(t, has)

	// Idempotent: a second run still matches but changes nothing.
	res = runResp{}
	require.Equal(t, http.StatusOK, postJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d/run", rule.ID), nil, &res))
	assert.Equal(t, 2, res.Matched)
	assert.Equal(t, 0, res.Updated)
}

func TestRunRuleOverwritesConflictingValue(t *testing.T) {
	srv, _, _ := newTestServer(t)

	// Pre-set a different value for the key the rule will write.
	require.Equal(t, http.StatusOK, putJSON(t, srv, "/api/v1/transactions/tx-1/labels", map[string]any{
		"labels": map[string]string{"flow": "unknown"},
	}, nil))

	var rule api.RuleDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules", map[string]any{
		"query":  "id:tx-1",
		"labels": map[string]string{"flow": "out"},
	}, &rule))
	var res runResp
	require.Equal(t, http.StatusOK, postJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d/run", rule.ID), nil, &res))
	assert.Equal(t, 1, res.Updated)

	var tx api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-1", &tx))
	assert.Equal(t, "out", tx.Labels["flow"], "the rule is authoritative for its key")
}

func TestRunAllRulesSkipsDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules", map[string]any{
		"query": "amount:<0", "labels": map[string]string{"flow": "out"},
	}, nil))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules", map[string]any{
		"query": "amount:>0", "labels": map[string]string{"flow": "in"}, "enabled": false,
	}, nil))

	var res runResp
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/rules/run", nil, &res))
	assert.Equal(t, 2, res.Updated) // only the enabled outflow rule

	// The disabled inflow rule did not label an inflow.
	var tx api.TransactionDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-3", &tx))
	_, has := tx.Labels["flow"]
	assert.False(t, has)
}

func TestRunRuleByIDRunsDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var rule api.RuleDTO
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/rules", map[string]any{
		"query": "amount:>0", "labels": map[string]string{"flow": "in"}, "enabled": false,
	}, &rule))
	require.False(t, rule.Enabled)

	// Running a specific rule by id applies it even though it is disabled.
	var res runResp
	require.Equal(t, http.StatusOK, postJSON(t, srv, fmt.Sprintf("/api/v1/rules/%d/run", rule.ID), nil, &res))
	assert.Equal(t, 2, res.Updated) // tx-3 (100), tx-4 (250)
}

func TestRunRuleNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	require.Equal(t, http.StatusNotFound, postJSON(t, srv, "/api/v1/rules/9999/run", nil, nil))
}

// --- MCP parity ---

func TestMCPRulesTools(t *testing.T) {
	session, _, _ := connectMCP(t)

	var created api.RuleDTO
	callTool(t, session, "create_rule", map[string]any{
		"name":   "Coffee",
		"query":  "description:coffee",
		"labels": map[string]any{"category": "coffee"},
	}, &created)
	require.NotZero(t, created.ID)

	var list rulesList
	callTool(t, session, "list_rules", map[string]any{}, &list)
	require.Len(t, list.Rules, 1)

	// run_rules with an id runs that one rule; tx-1 "Coffee" matches.
	var run runResp
	callTool(t, session, "run_rules", map[string]any{"id": created.ID}, &run)
	assert.Equal(t, 1, run.Updated)

	var del struct {
		Deleted bool `json:"deleted"`
	}
	callTool(t, session, "delete_rule", map[string]any{"id": created.ID}, &del)
	assert.True(t, del.Deleted)

	var list2 rulesList
	callTool(t, session, "list_rules", map[string]any{}, &list2)
	assert.Empty(t, list2.Rules)

	// An invalid query surfaces as a tool error, not a created rule.
	bad := callTool(t, session, "create_rule", map[string]any{
		"query": "amount:>", "labels": map[string]any{"a": "b"},
	}, nil)
	assert.True(t, bad.IsError)
}
