package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/search"
)

// handleSearchTransactions serves GET /api/v1/transactions/search?q=&limit=&offset=.
// The query language (see internal/search) is evaluated in Go over the full
// transaction set, so it supports any field and arbitrary label combinations
// that would be awkward to push down to portable SQL. A malformed query is a
// 400 with the parser's message.
func (s *Server) handleSearchTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	query, err := search.Parse(q)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := parseListParams(r) // search carries its own date filters; only limit/offset apply
	matched, total, err := s.searchTransactions(r.Context(), query, p.limit, p.offset)
	if err != nil {
		s.serverError(w, "search transactions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"query":        q,
		"total":        total,
		"transactions": toTransactionDTOs(matched),
	})
}

// searchTransactions evaluates query against every transaction, returning the
// requested page of matches (newest first, the query's natural order) and the
// total number of matches across all pages.
func (s *Server) searchTransactions(ctx context.Context, query *search.Query, limit, offset int64) ([]db.Transaction, int, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, 0, err
	}
	names := make(map[string]string, len(accounts))
	for _, a := range accounts {
		names[a.ID] = a.Name
	}

	txns, err := s.allTransactions(ctx)
	if err != nil {
		return nil, 0, err
	}

	matched := make([]db.Transaction, 0)
	for _, t := range txns {
		if query.Match(toSearchRecord(t, names[t.AccountID])) {
			matched = append(matched, t)
		}
	}
	total := len(matched)

	if offset < 0 {
		offset = 0
	}
	if offset > int64(total) {
		offset = int64(total)
	}
	matched = matched[offset:]
	if limit >= 0 && int64(len(matched)) > limit {
		matched = matched[:limit]
	}
	return matched, total, nil
}

// allTransactions loads every transaction, paging through ListTransactions in
// the largest batches the store allows (mirrors the dashboard client). Search
// filters in Go, so it needs the full set.
func (s *Server) allTransactions(ctx context.Context) ([]db.Transaction, error) {
	const batch = maxLimit
	var all []db.Transaction
	for offset := int64(0); ; offset += batch {
		rows, err := s.store.ListTransactions(ctx, db.ListTransactionsParams{
			RowLimit:  batch,
			RowOffset: offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if int64(len(rows)) < batch {
			break
		}
	}
	return all, nil
}

// toSearchRecord adapts a stored transaction into the search engine's neutral
// Record, resolving the account name and decoding the JSON labels here so the
// engine stays free of db/api dependencies.
func toSearchRecord(t db.Transaction, accountName string) search.Record {
	return search.Record{
		ID:          t.ID,
		AccountID:   t.AccountID,
		AccountName: accountName,
		Amount:      parseAmountValue(t.Amount),
		AmountRaw:   t.Amount,
		Pending:     t.Pending != 0,
		Date:        unixTime(t.Date),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      decodeLabels(t.Labels),
		SyncedAt:    unixTime(t.SyncedAt),
	}
}

// parseAmountValue parses a stored decimal amount string into a float for
// numeric comparisons, tolerating thousands separators. Unparseable values
// become 0 (matching the dashboard's lenient handling).
func parseAmountValue(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// --- MCP tool ---

type searchTransactionsInput struct {
	Q      string `json:"q" jsonschema:"the search query, e.g. 'coffee amount:<0 date:2024 label:category=food' (empty matches all)"`
	Limit  int64  `json:"limit,omitempty" jsonschema:"maximum number of transactions to return (default 100)"`
	Offset int64  `json:"offset,omitempty" jsonschema:"number of matches to skip, for pagination (optional)"`
}

type searchTransactionsOutput struct {
	Query        string           `json:"query"`
	Total        int              `json:"total"`
	Transactions []TransactionDTO `json:"transactions"`
}

func (s *Server) mcpSearchTransactions(ctx context.Context, _ *mcp.CallToolRequest, in searchTransactionsInput) (*mcp.CallToolResult, searchTransactionsOutput, error) {
	query, err := search.Parse(in.Q)
	if err != nil {
		return nil, searchTransactionsOutput{}, fmt.Errorf("invalid query: %w", err)
	}
	limit := int64(defaultLimit)
	if in.Limit > 0 {
		limit = in.Limit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	matched, total, err := s.searchTransactions(ctx, query, limit, in.Offset)
	if err != nil {
		return nil, searchTransactionsOutput{}, err
	}
	return &mcp.CallToolResult{}, searchTransactionsOutput{
		Query:        in.Q,
		Total:        total,
		Transactions: toTransactionDTOs(matched),
	}, nil
}
