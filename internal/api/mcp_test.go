package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
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
		"list_accounts", "get_account", "create_account", "update_account", "delete_account",
		"list_transactions", "search_transactions",
		"create_transaction", "update_transaction", "delete_transaction",
		"list_labels", "set_transaction_extensions", "list_extensions",
		"get_transaction_relationships", "create_transaction_relationship",
		"delete_transaction_relationship", "list_relationship_kinds",
		"list_organizations", "sync_status", "trigger_sync",
		"list_rules", "create_rule", "update_rule", "delete_rule", "run_rules",
		"list_events", "get_transaction_history", "get_transaction_provenance",
		"list_api_keys", "create_api_key", "revoke_api_key",
		"list_webhooks", "create_webhook", "update_webhook", "delete_webhook", "test_webhook",
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

func TestMCPGetTransactionHistory(t *testing.T) {
	session, fx, _ := connectMCP(t)

	// A known transaction returns a valid history envelope. The MCP test server has
	// no emitter, so no versions are recorded yet -- an empty list, but a success.
	var out api.HistoryDTO
	res := callTool(t, session, "get_transaction_history", map[string]any{"transaction_id": fx.TxIDsByDateDesc[0]}, &out)
	require.False(t, res.IsError)
	assert.Equal(t, fx.TxIDsByDateDesc[0], out.TransactionID)
	assert.Empty(t, out.Versions)

	// An unknown id surfaces as a tool error.
	missing := callTool(t, session, "get_transaction_history", map[string]any{"transaction_id": "nope"}, nil)
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

func TestMCPListEvents(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	_, err := store.InsertEvent(context.Background(), db.InsertEventParams{
		EventID: "e1", EventType: "transaction.created", EntityType: "transaction",
		EntityID: "tx-1", OccurredAt: 1, Data: "{}",
	})
	require.NoError(t, err)

	s := api.New(api.Options{
		Store:      store,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		MCPEnabled: true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = s.MCPServer().Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	var out struct {
		Events []api.EventDTO `json:"events"`
		Next   int64          `json:"next"`
	}
	res := callTool(t, session, "list_events", map[string]any{}, &out)
	require.False(t, res.IsError)
	require.Len(t, out.Events, 1)
	assert.Equal(t, "transaction.created", out.Events[0].Type)
	assert.Equal(t, out.Events[0].Sequence, out.Next)

	// A filter matching nothing returns an empty page.
	var empty struct {
		Events []api.EventDTO `json:"events"`
	}
	callTool(t, session, "list_events", map[string]any{"type": "rule.created"}, &empty)
	assert.Empty(t, empty.Events)
}
