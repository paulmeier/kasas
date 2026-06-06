package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/rules"
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
		Description: "List transactions, optionally filtered by account, date range, and label (key, or key+value).",
	}, s.mcpListTransactions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_transactions",
		Description: "Search transactions with the kasas query language: free text plus field filters (description:, payee:, memo:, account:, id:, amount:>10, date:2024-03, pending:true) and label filters (label:key=value, label:key for presence, or the key:value shorthand), combined with AND / OR / NOT and parentheses. An empty query matches all.",
	}, s.mcpSearchTransactions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_labels",
		Description: "List the label vocabulary: every key/value pair in use with the number of transactions carrying it.",
	}, s.mcpListLabels)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_rules",
		Description: "List the auto-labeling rules. Each rule applies its labels to every transaction matching its query (the kasas search syntax).",
	}, s.mcpListRules)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_rule",
		Description: "Create an auto-labeling rule: a condition (a kasas search query, e.g. 'amount:<0 description:coffee') and the labels to apply to every matching transaction. Enabled rules apply automatically to newly-synced transactions; use run_rules to also apply over existing ones.",
	}, s.mcpCreateRule)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_rule",
		Description: "Replace an existing rule's name, query, labels, and enabled flag by id.",
	}, s.mcpUpdateRule)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_rule",
		Description: "Delete a rule by id. Does not remove labels already applied to transactions.",
	}, s.mcpDeleteRule)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "run_rules",
		Description: "Run rules over all existing transactions, applying labels to matches. Pass an id to run a single rule (even if disabled); omit it to run every enabled rule. Returns how many transactions matched and how many were newly labeled.",
	}, s.mcpRunRules)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_events",
		Description: "Read the canonical event stream: an ordered, replayable log of changes (transaction.created/updated, account.created/updated, label.applied/removed, rule.created/updated/deleted/executed, sync.completed). Page with `after` (a sequence cursor; 0 or omitted starts from the beginning) and optionally filter by type, entity_type (transaction|account|label|rule|sync), or entity_id. Returns the events plus the `next` cursor to pass as `after` next time.",
	}, s.mcpListEvents)

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
	AccountID  string `json:"account_id,omitempty" jsonschema:"filter to a single account id (optional)"`
	Since      string `json:"since,omitempty" jsonschema:"lower bound as YYYY-MM-DD, RFC3339, or unix seconds (optional)"`
	Until      string `json:"until,omitempty" jsonschema:"upper bound as YYYY-MM-DD, RFC3339, or unix seconds (optional)"`
	Limit      int64  `json:"limit,omitempty" jsonschema:"maximum number of transactions to return (default 100)"`
	LabelKey   string `json:"label_key,omitempty" jsonschema:"filter to transactions carrying this label key (optional)"`
	LabelValue string `json:"label_value,omitempty" jsonschema:"with label_key, require this exact value; omit to match any value (optional)"`
}

type listTransactionsOutput struct {
	Transactions []TransactionDTO `json:"transactions"`
}

type listLabelsOutput struct {
	Labels []LabelDTO `json:"labels"`
}

type listRulesOutput struct {
	Rules []RuleDTO `json:"rules"`
}

type createRuleInput struct {
	Name    string            `json:"name,omitempty" jsonschema:"optional human-readable name for the rule"`
	Query   string            `json:"query" jsonschema:"the condition: a kasas search query, e.g. 'amount:<0 description:coffee' or 'label:category=food amount:>50'"`
	Labels  map[string]string `json:"labels" jsonschema:"the key:value labels to apply to every transaction the query matches"`
	Enabled *bool             `json:"enabled,omitempty" jsonschema:"whether the rule auto-applies to newly-synced transactions (default true)"`
}

type updateRuleInput struct {
	ID      int64             `json:"id" jsonschema:"the id of the rule to replace"`
	Name    string            `json:"name,omitempty" jsonschema:"optional human-readable name for the rule"`
	Query   string            `json:"query" jsonschema:"the condition: a kasas search query"`
	Labels  map[string]string `json:"labels" jsonschema:"the key:value labels to apply to every transaction the query matches"`
	Enabled *bool             `json:"enabled,omitempty" jsonschema:"whether the rule auto-applies to newly-synced transactions (default true)"`
}

type deleteRuleInput struct {
	ID int64 `json:"id" jsonschema:"the id of the rule to delete"`
}

type deleteRuleOutput struct {
	ID      int64 `json:"id"`
	Deleted bool  `json:"deleted"`
}

type runRulesInput struct {
	ID int64 `json:"id,omitempty" jsonschema:"a single rule id to run (even if disabled); omit to run all enabled rules"`
}

type runRulesOutput struct {
	Matched int `json:"matched"` // transactions matched by at least one rule
	Updated int `json:"updated"` // transactions whose labels actually changed
}

type listEventsInput struct {
	After      int64  `json:"after,omitempty" jsonschema:"return events after this sequence cursor; 0 or omitted starts from the beginning"`
	Limit      int64  `json:"limit,omitempty" jsonschema:"maximum number of events to return (default 100, max 1000)"`
	Type       string `json:"type,omitempty" jsonschema:"filter to one event type, e.g. transaction.created (optional)"`
	EntityType string `json:"entity_type,omitempty" jsonschema:"filter to one entity kind: transaction, account, label, rule, or sync (optional)"`
	EntityID   string `json:"entity_id,omitempty" jsonschema:"filter to one entity id, e.g. a transaction id (optional)"`
}

type listEventsOutput struct {
	Events []EventDTO `json:"events"`
	Next   int64      `json:"next"` // cursor to pass as `after` on the next call
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
	p := listParams{
		limit: limit,
		since: parseTimeParam(in.Since),
		until: parseTimeParam(in.Until),
	}
	lf := labelFilter{key: normalizeKey(in.LabelKey)}
	if in.LabelValue != "" {
		lf.hasValue = true
		lf.value = normalizeValue(in.LabelValue)
	}

	txns, err := s.queryTransactions(ctx, in.AccountID, p, lf)
	if err != nil {
		return nil, listTransactionsOutput{}, err
	}
	return &mcp.CallToolResult{}, listTransactionsOutput{Transactions: toTransactionDTOs(txns)}, nil
}

func (s *Server) mcpListLabels(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listLabelsOutput, error) {
	rows, err := s.store.ListLabeledTransactions(ctx)
	if err != nil {
		return nil, listLabelsOutput{}, err
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Labels
	}
	return &mcp.CallToolResult{}, listLabelsOutput{Labels: labelCounts(sets)}, nil
}

func (s *Server) mcpListRules(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listRulesOutput, error) {
	rs, err := s.store.ListRules(ctx)
	if err != nil {
		return nil, listRulesOutput{}, err
	}
	return &mcp.CallToolResult{}, listRulesOutput{Rules: toRuleDTOs(rs)}, nil
}

func (s *Server) mcpCreateRule(ctx context.Context, _ *mcp.CallToolRequest, in createRuleInput) (*mcp.CallToolResult, RuleDTO, error) {
	// createRuleInput is field-for-field a ruleInput (with richer schema docs).
	rule, err := s.createRule(ctx, ruleInput(in))
	if err != nil {
		return nil, RuleDTO{}, err
	}
	return &mcp.CallToolResult{}, toRuleDTO(rule), nil
}

func (s *Server) mcpUpdateRule(ctx context.Context, _ *mcp.CallToolRequest, in updateRuleInput) (*mcp.CallToolResult, RuleDTO, error) {
	rule, err := s.updateRule(ctx, in.ID, ruleInput{Name: in.Name, Query: in.Query, Labels: in.Labels, Enabled: in.Enabled})
	if errors.Is(err, errRuleNotFound) {
		return nil, RuleDTO{}, fmt.Errorf("rule %d not found", in.ID)
	}
	if err != nil {
		return nil, RuleDTO{}, err
	}
	return &mcp.CallToolResult{}, toRuleDTO(rule), nil
}

func (s *Server) mcpDeleteRule(ctx context.Context, _ *mcp.CallToolRequest, in deleteRuleInput) (*mcp.CallToolResult, deleteRuleOutput, error) {
	deleted, err := s.deleteRule(ctx, in.ID)
	if err != nil {
		return nil, deleteRuleOutput{}, err
	}
	if !deleted {
		return nil, deleteRuleOutput{}, fmt.Errorf("rule %d not found", in.ID)
	}
	return &mcp.CallToolResult{}, deleteRuleOutput{ID: in.ID, Deleted: true}, nil
}

func (s *Server) mcpRunRules(ctx context.Context, _ *mcp.CallToolRequest, in runRulesInput) (*mcp.CallToolResult, runRulesOutput, error) {
	var compiled []rules.Compiled
	if in.ID > 0 {
		rule, err := s.store.GetRule(ctx, in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, runRulesOutput{}, fmt.Errorf("rule %d not found", in.ID)
		}
		if err != nil {
			return nil, runRulesOutput{}, err
		}
		c, cerr := rules.Compile(ruleFromDB(rule))
		if cerr != nil {
			return nil, runRulesOutput{}, cerr
		}
		compiled = []rules.Compiled{c}
	} else {
		var err error
		compiled, err = s.enabledCompiledRules(ctx)
		if err != nil {
			return nil, runRulesOutput{}, err
		}
	}
	matched, updated, err := s.applyRules(ctx, compiled, in.ID)
	if err != nil {
		return nil, runRulesOutput{}, err
	}
	return &mcp.CallToolResult{}, runRulesOutput{Matched: matched, Updated: updated}, nil
}

func (s *Server) mcpListEvents(ctx context.Context, _ *mcp.CallToolRequest, in listEventsInput) (*mcp.CallToolResult, listEventsOutput, error) {
	rows, next, err := s.listEvents(ctx, in.After, in.Limit, in.Type, in.EntityType, in.EntityID)
	if err != nil {
		return nil, listEventsOutput{}, err
	}
	return &mcp.CallToolResult{}, listEventsOutput{Events: toEventDTOs(rows), Next: next}, nil
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
