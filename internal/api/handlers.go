package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
)

const (
	defaultLimit = 100
	maxLimit     = 1000
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleListOrganizations(w http.ResponseWriter, r *http.Request) {
	orgs, err := s.store.ListOrganizations(r.Context())
	if err != nil {
		s.serverError(w, "list organizations", err)
		return
	}
	out := make([]OrganizationDTO, len(orgs))
	for i, o := range orgs {
		out[i] = toOrganizationDTO(o)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"organizations": out})
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	var (
		accounts []db.Account
		err      error
	)
	if orgID := r.URL.Query().Get("org_id"); orgID != "" {
		accounts, err = s.store.ListAccountsByOrg(r.Context(), orgID)
	} else {
		accounts, err = s.store.ListAccounts(r.Context())
	}
	if err != nil {
		s.serverError(w, "list accounts", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"accounts": toAccountDTOs(accounts)})
}

func (s *Server) handleGetAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	account, err := s.store.GetAccount(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "account not found")
		return
	}
	if err != nil {
		s.serverError(w, "get account", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toAccountDTO(account))
}

func (s *Server) handleListAccountTransactions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p := parseListParams(r)
	txns, err := s.store.ListTransactionsByAccount(r.Context(), db.ListTransactionsByAccountParams{
		AccountID: id,
		Since:     p.since,
		Until:     p.until,
		RowLimit:  p.limit,
		RowOffset: p.offset,
	})
	if err != nil {
		s.serverError(w, "list account transactions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"transactions": toTransactionDTOs(txns)})
}

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	p := parseListParams(r)
	if accountID := r.URL.Query().Get("account_id"); accountID != "" {
		txns, err := s.store.ListTransactionsByAccount(r.Context(), db.ListTransactionsByAccountParams{
			AccountID: accountID,
			Since:     p.since,
			Until:     p.until,
			RowLimit:  p.limit,
			RowOffset: p.offset,
		})
		if err != nil {
			s.serverError(w, "list transactions", err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"transactions": toTransactionDTOs(txns)})
		return
	}

	txns, err := s.store.ListTransactions(r.Context(), db.ListTransactionsParams{
		Since:     p.since,
		Until:     p.until,
		RowLimit:  p.limit,
		RowOffset: p.offset,
	})
	if err != nil {
		s.serverError(w, "list transactions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"transactions": toTransactionDTOs(txns)})
}

func (s *Server) handleGetTransaction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	txn, err := s.store.GetTransaction(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	if err != nil {
		s.serverError(w, "get transaction", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toTransactionDTO(txn))
}

func (s *Server) handleSyncStatus(w http.ResponseWriter, r *http.Request) {
	latest, err := s.store.LatestSyncLog(r.Context())
	if errors.Is(err, sql.ErrNoRows) {
		s.writeJSON(w, http.StatusOK, map[string]any{"latest": nil})
		return
	}
	if err != nil {
		s.serverError(w, "sync status", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"latest": toSyncDTO(latest)})
}

func (s *Server) handleSyncHistory(w http.ResponseWriter, r *http.Request) {
	limit := int64(defaultLimit)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	logs, err := s.store.ListSyncLogs(r.Context(), limit)
	if err != nil {
		s.serverError(w, "sync history", err)
		return
	}
	out := make([]SyncDTO, len(logs))
	for i, l := range logs {
		out[i] = toSyncDTO(l)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"history": out})
}

func (s *Server) handleTriggerSync(w http.ResponseWriter, r *http.Request) {
	if s.syncer == nil {
		s.writeError(w, http.StatusServiceUnavailable, "sync is not available")
		return
	}
	// Run asynchronously: a sync involves a network round-trip to the bridge.
	// Progress is observable via GET /api/v1/sync.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.syncer.Sync(ctx); err != nil {
			s.logger.Error("on-demand sync failed", "error", err)
		}
	}()
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync started"})
}

// listParams holds common pagination and date-range query parameters.
type listParams struct {
	limit  int64
	offset int64
	since  int64 // unix seconds; 0 means no lower bound
	until  int64 // unix seconds; 0 means no upper bound
}

func parseListParams(r *http.Request) listParams {
	q := r.URL.Query()
	p := listParams{limit: defaultLimit}

	if v := q.Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			p.limit = n
		}
	}
	if p.limit > maxLimit {
		p.limit = maxLimit
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			p.offset = n
		}
	}
	p.since = parseTimeParam(q.Get("since"))
	p.until = parseTimeParam(q.Get("until"))
	return p
}

// parseTimeParam accepts a unix timestamp, an RFC3339 datetime, or a YYYY-MM-DD
// date and returns unix seconds. Empty or invalid input returns 0.
func parseTimeParam(v string) int64 {
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		return n
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.Unix()
	}
	return 0
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("encode response", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) serverError(w http.ResponseWriter, op string, err error) {
	s.logger.Error("request failed", "op", op, "error", err)
	s.writeError(w, http.StatusInternalServerError, "internal server error")
}
