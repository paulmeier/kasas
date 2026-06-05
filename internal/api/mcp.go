package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/db"
)

// MCPServer builds the MCP server with all kasas tools registered. It is used
// both for the HTTP transport (mounted at /mcp) and the stdio transport.
func (s *Server) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "kasas",
		Title:   "kasas — SimpleFIN financial data",
		Version: s.version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_accounts",
		Description: "List all synced financial accounts with their current balances.",
	}, s.mcpListAccounts)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_account",
		Description: "Get a single account by its id.",
	}, s.mcpGetAccount)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_transactions",
		Description: "List transactions, optionally filtered by account and date range.",
	}, s.mcpListTransactions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_organizations",
		Description: "List the financial institutions (organizations) that own the accounts.",
	}, s.mcpListOrganizations)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "sync_status",
		Description: "Report the status of the most recent SimpleFIN sync.",
	}, s.mcpSyncStatus)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "trigger_sync",
		Description: "Trigger an immediate SimpleFIN sync and wait for it to finish.",
	}, s.mcpTriggerSync)

	return srv
}

// MCPHandler returns an HTTP handler serving the MCP server over the streamable
// HTTP transport.
func (s *Server) MCPHandler() http.Handler {
	srv := s.MCPServer()
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
}

// RunMCPStdio runs the MCP server over stdin/stdout, for desktop MCP clients
// that launch kasas as a subprocess. It blocks until the client disconnects or
// ctx is cancelled.
func (s *Server) RunMCPStdio(ctx context.Context) error {
	return s.MCPServer().Run(ctx, &mcp.StdioTransport{})
}

// --- Tool input/output types ---

type emptyInput struct{}

type listAccountsOutput struct {
	Accounts []AccountDTO `json:"accounts"`
}

type getAccountInput struct {
	ID string `json:"id" jsonschema:"the account id"`
}

type listTransactionsInput struct {
	AccountID string `json:"account_id,omitempty" jsonschema:"filter to a single account id (optional)"`
	Since     string `json:"since,omitempty" jsonschema:"lower bound as YYYY-MM-DD, RFC3339, or unix seconds (optional)"`
	Until     string `json:"until,omitempty" jsonschema:"upper bound as YYYY-MM-DD, RFC3339, or unix seconds (optional)"`
	Limit     int64  `json:"limit,omitempty" jsonschema:"maximum number of transactions to return (default 100)"`
}

type listTransactionsOutput struct {
	Transactions []TransactionDTO `json:"transactions"`
}

type listOrganizationsOutput struct {
	Organizations []OrganizationDTO `json:"organizations"`
}

type triggerSyncOutput struct {
	Accounts        int    `json:"accounts"`
	NewTransactions int    `json:"new_transactions"`
	Duration        string `json:"duration"`
}

// --- Tool handlers ---

func (s *Server) mcpListAccounts(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listAccountsOutput, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, listAccountsOutput{}, err
	}
	return &mcp.CallToolResult{}, listAccountsOutput{Accounts: toAccountDTOs(accounts)}, nil
}

func (s *Server) mcpGetAccount(ctx context.Context, _ *mcp.CallToolRequest, in getAccountInput) (*mcp.CallToolResult, AccountDTO, error) {
	account, err := s.store.GetAccount(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, AccountDTO{}, fmt.Errorf("account %q not found", in.ID)
	}
	if err != nil {
		return nil, AccountDTO{}, err
	}
	return &mcp.CallToolResult{}, toAccountDTO(account), nil
}

func (s *Server) mcpListTransactions(ctx context.Context, _ *mcp.CallToolRequest, in listTransactionsInput) (*mcp.CallToolResult, listTransactionsOutput, error) {
	limit := int64(defaultLimit)
	if in.Limit > 0 {
		limit = in.Limit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	since := parseTimeParam(in.Since)
	until := parseTimeParam(in.Until)

	var (
		txns []db.Transaction
		err  error
	)
	if in.AccountID != "" {
		txns, err = s.store.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
			AccountID: in.AccountID,
			Since:     since,
			Until:     until,
			RowLimit:  limit,
			RowOffset: 0,
		})
	} else {
		txns, err = s.store.ListTransactions(ctx, db.ListTransactionsParams{
			Since:     since,
			Until:     until,
			RowLimit:  limit,
			RowOffset: 0,
		})
	}
	if err != nil {
		return nil, listTransactionsOutput{}, err
	}
	return &mcp.CallToolResult{}, listTransactionsOutput{Transactions: toTransactionDTOs(txns)}, nil
}

func (s *Server) mcpListOrganizations(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listOrganizationsOutput, error) {
	orgs, err := s.store.ListOrganizations(ctx)
	if err != nil {
		return nil, listOrganizationsOutput{}, err
	}
	out := make([]OrganizationDTO, len(orgs))
	for i, o := range orgs {
		out[i] = toOrganizationDTO(o)
	}
	return &mcp.CallToolResult{}, listOrganizationsOutput{Organizations: out}, nil
}

func (s *Server) mcpSyncStatus(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, SyncDTO, error) {
	latest, err := s.store.LatestSyncLog(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return &mcp.CallToolResult{}, SyncDTO{Status: "never run"}, nil
	}
	if err != nil {
		return nil, SyncDTO{}, err
	}
	return &mcp.CallToolResult{}, toSyncDTO(latest), nil
}

func (s *Server) mcpTriggerSync(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, triggerSyncOutput, error) {
	if s.syncer == nil {
		return nil, triggerSyncOutput{}, errors.New("sync is not available")
	}
	res, err := s.syncer.Sync(ctx)
	if err != nil {
		return nil, triggerSyncOutput{}, err
	}
	return &mcp.CallToolResult{}, triggerSyncOutput{
		Accounts:        res.Accounts,
		NewTransactions: res.NewTransactions,
		Duration:        res.Duration.String(),
	}, nil
}
