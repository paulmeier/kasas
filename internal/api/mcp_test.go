package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/testutil"
)

func connectMCP(t *testing.T) (*mcp.ClientSession, testutil.Fixtures, *fakeSyncer) {
	t.Helper()
	s, fx, fs := newAPIServer(t)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	clientT, serverT := mcp.NewInMemoryTransports()
	// The server must be connected before the client initializes the session.
	go func() { _ = s.MCPServer().Run(ctx, serverT) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session, fx, fs
}

func callTool(t *testing.T, s *mcp.ClientSession, name string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	if out != nil && !res.IsError {
		require.NotEmpty(t, res.Content)
		text, ok := res.Content[0].(*mcp.TextContent)
		require.True(t, ok, "expected text content")
		require.NoError(t, json.Unmarshal([]byte(text.Text), out))
	}
	return res
}

func TestMCPListsAllTools(t *testing.T) {
	session, _, _ := connectMCP(t)

	tools, err := session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, len(tools.Tools))
	for i, tool := range tools.Tools {
		names[i] = tool.Name
	}
	assert.ElementsMatch(t, []string{
		"list_accounts", "get_account", "list_transactions", "search_transactions",
		"list_labels", "list_organizations", "sync_status", "trigger_sync",
		"list_rules", "create_rule", "update_rule", "delete_rule", "run_rules",
	}, names)
}

func TestMCPListAccounts(t *testing.T) {
	session, _, _ := connectMCP(t)

	var out struct {
		Accounts []api.AccountDTO `json:"accounts"`
	}
	res := callTool(t, session, "list_accounts", map[string]any{}, &out)
	require.False(t, res.IsError)
	require.Len(t, out.Accounts, 2)
	assert.Equal(t, "Checking", out.Accounts[0].Name)
}

func TestMCPGetAccount(t *testing.T) {
	session, fx, _ := connectMCP(t)

	var acct api.AccountDTO
	res := callTool(t, session, "get_account", map[string]any{"id": fx.CheckingID}, &acct)
	require.False(t, res.IsError)
	assert.Equal(t, fx.CheckingID, acct.ID)
	assert.Equal(t, "1000.00", acct.Balance)

	// Unknown id surfaces as a tool error (IsError), not a protocol error.
	missing := callTool(t, session, "get_account", map[string]any{"id": "nope"}, nil)
	assert.True(t, missing.IsError)
}

func TestMCPListTransactions(t *testing.T) {
	session, fx, _ := connectMCP(t)

	var out struct {
		Transactions []api.TransactionDTO `json:"transactions"`
	}
	callTool(t, session, "list_transactions", map[string]any{
		"account_id": fx.CheckingID,
		"limit":      2,
	}, &out)
	require.Len(t, out.Transactions, 2)
	assert.Equal(t, "tx-3", out.Transactions[0].ID)
	assert.Equal(t, "tx-2", out.Transactions[1].ID)
}

func TestMCPListTransactionsByLabel(t *testing.T) {
	session, _, _ := connectMCP(t)

	// The label filter is wired through the same queryTransactions path as the
	// HTTP handler (covered by TestFilterTransactionsByLabel). Here we just assert
	// the MCP input is accepted and the SQL filter path runs: no labels are
	// seeded, so a key filter returns an empty (non-error) result.
	var out struct {
		Transactions []api.TransactionDTO `json:"transactions"`
	}
	res := callTool(t, session, "list_transactions", map[string]any{"label_key": "category"}, &out)
	require.False(t, res.IsError)
	assert.Empty(t, out.Transactions)
}

func TestMCPListLabels(t *testing.T) {
	session, _, _ := connectMCP(t)

	var out struct {
		Labels []api.LabelDTO `json:"labels"`
	}
	res := callTool(t, session, "list_labels", map[string]any{}, &out)
	require.False(t, res.IsError)
	// No labels seeded -> empty (but non-null) vocabulary.
	assert.Empty(t, out.Labels)
}

func TestMCPSyncStatus(t *testing.T) {
	session, _, _ := connectMCP(t)

	var status api.SyncDTO
	callTool(t, session, "sync_status", map[string]any{}, &status)
	assert.Equal(t, "success", status.Status)
}

func TestMCPTriggerSync(t *testing.T) {
	session, _, fs := connectMCP(t)

	var out struct {
		Accounts        int    `json:"accounts"`
		NewTransactions int    `json:"new_transactions"`
		Duration        string `json:"duration"`
	}
	res := callTool(t, session, "trigger_sync", map[string]any{}, &out)
	require.False(t, res.IsError)
	assert.Equal(t, 1, fs.Calls())
	assert.Equal(t, 3, out.NewTransactions)
}
