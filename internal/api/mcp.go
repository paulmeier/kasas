package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/extensions"
	"github.com/paulmeier/kasas/internal/provenance"
	"github.com/paulmeier/kasas/internal/relationships"
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
		Description: "List transactions, optionally filtered by account, date range, and label (key, or key+value). Each transaction includes its labels and schema extensions.",
	}, s.mcpListTransactions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_transactions",
		Description: "Search transactions with the kasas query language: free text plus field filters (description:, payee:, memo:, account:, id:, amount:>10, date:2024-03, pending:true), label filters (label:key=value, label:key for presence, or the key:value shorthand), and schema-extension filters (ext:tax.category=meal, ext:forecast.recurring=true, ext:custom.myapp.score for presence, ext:key~substr for contains), combined with AND / OR / NOT and parentheses. An empty query matches all.",
	}, s.mcpSearchTransactions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_labels",
		Description: "List the label vocabulary: every key/value pair in use with the number of transactions carrying it.",
	}, s.mcpListLabels)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "set_transaction_extensions",
		Description: "Replace a transaction's schema extensions: arbitrary, app-owned namespaced metadata whose values may be any JSON (e.g. {\"tax.category\":\"meal\",\"forecast.recurring\":true,\"custom.myapp.score\":88}). Replaces the WHOLE set — send the full desired object; an empty object clears it. Extensions are parallel to labels (which are strict key:value strings for categorization); use these for app/agent-owned data. Returns the updated transaction.",
	}, s.mcpSetTransactionExtensions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_extensions",
		Description: "List the schema-extension vocabulary: every namespaced key in use, with its namespace (the part before the first dot) and the number of transactions carrying it.",
	}, s.mcpListExtensions)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_transaction_relationships",
		Description: "Get one transaction's relationships: explicit directed edges to other transactions (e.g. a refund_of a purchase, a transfer_to another account). Returns both OUTBOUND edges (this transaction is the subject) and INBOUND edges (another transaction points at this one), each with its kind, direction (outbound|inbound), and the other transaction's id.",
	}, s.mcpGetTransactionRelationships)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_transaction_relationship",
		Description: "Assert a directed relationship from one transaction to another: an outbound edge transaction_id --kind--> target (e.g. mark a refund as refund_of its original purchase, or one leg of a transfer as transfer_to the other). kind is a freeform lowercase verb (refund_of, transfer_to, withholding_for, related_to, ...). The target transaction must exist and cannot be the subject itself. Adding an existing edge is a no-op. Returns the updated neighborhood.",
	}, s.mcpCreateTransactionRelationship)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_transaction_relationship",
		Description: "Remove a directed relationship: the outbound edge transaction_id --kind--> target. Removing an absent edge is a no-op. To remove an inbound edge, call this on the transaction that owns it (the edge's subject). Returns the updated neighborhood.",
	}, s.mcpDeleteTransactionRelationship)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_relationship_kinds",
		Description: "List the relationship-kind vocabulary: every kind in use (refund_of, transfer_to, ...) with the number of outbound edges using it.",
	}, s.mcpListRelationshipKinds)

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
		Name:        "get_transaction_history",
		Description: "Get the immutable version history of one transaction: an ordered list of full snapshots (v1 imported, then synced or labeled changes), each with a diff against the previous version. Answers why a transaction looks different now than it did before. Returns an empty list for a transaction that has not changed since history began.",
	}, s.mcpGetTransactionHistory)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_transaction_provenance",
		Description: "Get one transaction's provenance: where it came from and how it reached its current state. Returns the source (ingestion path, e.g. simplefin), the upstream source_transaction_id, the account and institution, when it was first imported and last seen, and an ordered list of transformations (imported, synced, labeled, extended), each with a one-line summary. A read-only lineage view derived from the ledger; it mirrors get_transaction_history but as an origin summary rather than full snapshots.",
	}, s.mcpGetTransactionProvenance)

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

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_api_keys",
		Description: "List the provisioned API keys (metadata only: id, name, prefix, scope, created/last-used times). Secrets are never returned.",
	}, s.mcpListApiKeys)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_api_key",
		Description: "Provision a new API key for programmatic REST access, with scope read (GET only) or read_write (GET + mutations). Returns the full secret in `key` exactly once — it is stored only as a hash and cannot be retrieved again.",
	}, s.mcpCreateApiKey)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "revoke_api_key",
		Description: "Revoke (delete) an API key by id. The key stops working immediately.",
	}, s.mcpRevokeApiKey)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_webhooks",
		Description: "List the registered webhook endpoints (url, subscribed event types, enabled flag, and last-delivery health). Signing secrets are not returned.",
	}, s.mcpListWebhooks)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_webhook",
		Description: "Register a webhook: an absolute http(s) URL that kasas POSTs each subscribed event to, HMAC-signed (X-Kasas-Signature). Subscribe to specific types (transaction.created/updated, account.created/updated, label.applied/removed, rule.*, sync.completed) or use [\"*\"] / omit for all. Returns the signing secret.",
	}, s.mcpCreateWebhook)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "update_webhook",
		Description: "Replace a webhook's url, subscribed event types, and enabled flag by id. Does not change the signing secret.",
	}, s.mcpUpdateWebhook)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "delete_webhook",
		Description: "Delete a webhook by id. Deliveries stop immediately.",
	}, s.mcpDeleteWebhook)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "test_webhook",
		Description: "Send a synthetic webhook.test event to a webhook's endpoint now and report the delivery status, so connectivity and signature handling can be verified.",
	}, s.mcpTestWebhook)

	// Plugin management is registered only when the plugin system is enabled.
	if s.pluginMgr != nil {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "list_plugins",
			Description: "List installed plugins: each plugin's runtime, version, the lifecycle hooks it subscribes to, the capabilities it requests and was granted, whether it is enabled and currently loaded, its state (loaded|disabled|error|missing), and the health of its most recent run.",
		}, s.mcpListPlugins)

		mcp.AddTool(srv, &mcp.Tool{
			Name:        "get_plugin",
			Description: "Get one plugin's full status by id.",
		}, s.mcpGetPlugin)

		mcp.AddTool(srv, &mcp.Tool{
			Name:        "enable_plugin",
			Description: "Enable a plugin by id: load its code and start running its hooks against committed events. Enabling executes third-party code. Returns the updated status.",
		}, s.mcpEnablePlugin)

		mcp.AddTool(srv, &mcp.Tool{
			Name:        "disable_plugin",
			Description: "Disable a plugin by id: stop running its hooks and unload it. Returns the updated status.",
		}, s.mcpDisablePlugin)

		mcp.AddTool(srv, &mcp.Tool{
			Name:        "reload_plugin",
			Description: "Reload a plugin by id from disk, picking up code and manifest changes without a restart (reloads only if the plugin is enabled). Returns the updated status.",
		}, s.mcpReloadPlugin)
	}

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

type setTransactionExtensionsInput struct {
	TransactionID string         `json:"transaction_id" jsonschema:"the id of the transaction whose extensions to replace"`
	Extensions    map[string]any `json:"extensions" jsonschema:"the full namespaced metadata object to store; keys are namespaced (e.g. tax.category) and values may be any JSON (string, number, boolean, object, array); an empty object clears all extensions"`
}

type listExtensionsOutput struct {
	Extensions []ExtensionDTO `json:"extensions"`
}

type getTransactionRelationshipsInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"the id of the transaction whose relationships to list"`
}

type relationshipMutationInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"the id of the SUBJECT transaction the outbound edge is asserted from"`
	Target        string `json:"target" jsonschema:"the id of the target transaction the edge points at"`
	Kind          string `json:"kind" jsonschema:"the relationship kind: a freeform lowercase verb, e.g. refund_of, transfer_to, withholding_for"`
}

type transactionRelationshipsOutput struct {
	TransactionID string            `json:"transaction_id"`
	Relationships []RelationshipDTO `json:"relationships"`
}

type listRelationshipKindsOutput struct {
	Relationships []RelationshipKindDTO `json:"relationships"`
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

type getTransactionHistoryInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"the id of the transaction whose history to fetch"`
}

type getTransactionProvenanceInput struct {
	TransactionID string `json:"transaction_id" jsonschema:"the id of the transaction whose provenance to fetch"`
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

func (s *Server) mcpSetTransactionExtensions(ctx context.Context, _ *mcp.CallToolRequest, in setTransactionExtensionsInput) (*mcp.CallToolResult, TransactionDTO, error) {
	// Round-trip the decoded values back to raw JSON so the shared write path can
	// normalize/validate them losslessly.
	b, err := json.Marshal(in.Extensions)
	if err != nil {
		return nil, TransactionDTO{}, err
	}
	next, notFound, err := s.setExtensions(ctx, in.TransactionID, extensions.Decode(string(b)))
	if err != nil {
		return nil, TransactionDTO{}, err
	}
	if notFound {
		return nil, TransactionDTO{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	return &mcp.CallToolResult{}, toTransactionDTO(next), nil
}

func (s *Server) mcpListExtensions(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listExtensionsOutput, error) {
	rows, err := s.store.ListExtendedTransactions(ctx)
	if err != nil {
		return nil, listExtensionsOutput{}, err
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Extensions
	}
	return &mcp.CallToolResult{}, listExtensionsOutput{Extensions: extensionCounts(sets)}, nil
}

func (s *Server) mcpGetTransactionRelationships(ctx context.Context, _ *mcp.CallToolRequest, in getTransactionRelationshipsInput) (*mcp.CallToolResult, transactionRelationshipsOutput, error) {
	rels, notFound, err := s.listTransactionRelationships(ctx, in.TransactionID)
	if err != nil {
		return nil, transactionRelationshipsOutput{}, err
	}
	if notFound {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	return &mcp.CallToolResult{}, transactionRelationshipsOutput{TransactionID: in.TransactionID, Relationships: rels}, nil
}

func (s *Server) mcpCreateTransactionRelationship(ctx context.Context, _ *mcp.CallToolRequest, in relationshipMutationInput) (*mcp.CallToolResult, transactionRelationshipsOutput, error) {
	if relationships.NormalizeKind(in.Kind) == "" {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("a relationship must have a kind")
	}
	if strings.TrimSpace(in.Target) == "" {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("a relationship must have a target")
	}
	_, notFound, err := s.addRelationship(ctx, in.TransactionID, in.Kind, in.Target)
	if err != nil {
		return nil, transactionRelationshipsOutput{}, err
	}
	if notFound {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	return s.mcpRelationshipsResult(ctx, in.TransactionID)
}

func (s *Server) mcpDeleteTransactionRelationship(ctx context.Context, _ *mcp.CallToolRequest, in relationshipMutationInput) (*mcp.CallToolResult, transactionRelationshipsOutput, error) {
	if strings.TrimSpace(in.Kind) == "" || strings.TrimSpace(in.Target) == "" {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("kind and target are required")
	}
	_, notFound, err := s.removeRelationship(ctx, in.TransactionID, in.Kind, in.Target)
	if err != nil {
		return nil, transactionRelationshipsOutput{}, err
	}
	if notFound {
		return nil, transactionRelationshipsOutput{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	return s.mcpRelationshipsResult(ctx, in.TransactionID)
}

// mcpRelationshipsResult loads and returns a transaction's neighborhood as the
// result of a create/delete tool call, so the caller sees the post-mutation state.
func (s *Server) mcpRelationshipsResult(ctx context.Context, id string) (*mcp.CallToolResult, transactionRelationshipsOutput, error) {
	rels, _, err := s.listTransactionRelationships(ctx, id)
	if err != nil {
		return nil, transactionRelationshipsOutput{}, err
	}
	return &mcp.CallToolResult{}, transactionRelationshipsOutput{TransactionID: id, Relationships: rels}, nil
}

func (s *Server) mcpListRelationshipKinds(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listRelationshipKindsOutput, error) {
	rows, err := s.store.ListRelatedTransactions(ctx)
	if err != nil {
		return nil, listRelationshipKindsOutput{}, err
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Relationships
	}
	return &mcp.CallToolResult{}, listRelationshipKindsOutput{Relationships: relationshipKindCounts(sets)}, nil
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

func (s *Server) mcpGetTransactionHistory(ctx context.Context, _ *mcp.CallToolRequest, in getTransactionHistoryInput) (*mcp.CallToolResult, HistoryDTO, error) {
	txn, err := s.store.GetTransaction(ctx, in.TransactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, HistoryDTO{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	if err != nil {
		return nil, HistoryDTO{}, err
	}
	rows, err := s.store.ListTransactionVersions(ctx, in.TransactionID)
	if err != nil {
		return nil, HistoryDTO{}, err
	}
	return &mcp.CallToolResult{}, buildHistory(txn, rows), nil
}

func (s *Server) mcpGetTransactionProvenance(ctx context.Context, _ *mcp.CallToolRequest, in getTransactionProvenanceInput) (*mcp.CallToolResult, provenance.Provenance, error) {
	txn, err := s.store.GetTransaction(ctx, in.TransactionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, provenance.Provenance{}, fmt.Errorf("transaction %q not found", in.TransactionID)
	}
	if err != nil {
		return nil, provenance.Provenance{}, err
	}
	prov, err := s.buildProvenance(ctx, txn)
	if err != nil {
		return nil, provenance.Provenance{}, err
	}
	return &mcp.CallToolResult{}, prov, nil
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
