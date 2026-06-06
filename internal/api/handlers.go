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
	"github.com/paulmeier/kasas/internal/events"
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
	txns, err := s.queryTransactions(r.Context(), id, parseListParams(r), parseLabelFilter(r))
	if err != nil {
		s.serverError(w, "list account transactions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"transactions": toTransactionDTOs(txns)})
}

func (s *Server) handleListTransactions(w http.ResponseWriter, r *http.Request) {
	accountID := r.URL.Query().Get("account_id")
	txns, err := s.queryTransactions(r.Context(), accountID, parseListParams(r), parseLabelFilter(r))
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

// updateLabelsRequest is the body of PUT /transactions/{id}/labels. It replaces
// the transaction's entire label set with the (normalized) key:value pairs.
type updateLabelsRequest struct {
	Labels map[string]string `json:"labels"`
}

func (s *Server) handleUpdateTransactionLabels(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req updateLabelsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)) // 16 KiB is plenty
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	newLabels := normalizeLabels(req.Labels)
	encoded, err := encodeLabels(newLabels)
	if err != nil {
		s.serverError(w, "encode labels", err)
		return
	}

	// Update and emit the per-key label diff atomically: read the old labels, write
	// the new set, then record label.applied / label.removed for what changed.
	notFound := false
	err = s.emitter.Record(r.Context(), s.store, func(q db.Querier, rec *events.Recorder) error {
		prev, gerr := q.GetTransaction(r.Context(), id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		if _, uerr := q.UpdateTransactionLabels(r.Context(), db.UpdateTransactionLabelsParams{ID: id, Labels: encoded}); uerr != nil {
			return uerr
		}
		if derr := rec.EmitLabelDiff(r.Context(), q, id, decodeLabels(prev.Labels), newLabels); derr != nil {
			return derr
		}
		// Append a labeled version, synthesizing a v1 baseline from the prior state
		// if this transaction predates history.
		next := prev
		next.Labels = encoded
		return rec.VersionChange(r.Context(), q, id, events.TransactionSnapshot(prev), events.TransactionSnapshot(next), events.ChangeLabeled)
	})
	if err != nil {
		s.serverError(w, "update transaction labels", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "labels": newLabels})
}

func (s *Server) handleListLabels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListLabeledTransactions(r.Context())
	if err != nil {
		s.serverError(w, "list labels", err)
		return
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Labels
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"labels": labelCounts(sets)})
}

// handleDeleteLabel removes a label from the vocabulary by stripping it from
// every transaction that carries it, pushed down to SQL. DELETE /labels/{key}
// removes the key (any value); adding ?value=<v> removes it only where it holds
// that value. Labels live on the transactions themselves (no separate table), so
// deleting a label IS removing it everywhere. Idempotent: an unknown label
// affects 0 rows and still returns 200.
func (s *Server) handleDeleteLabel(w http.ResponseWriter, r *http.Request) {
	key := normalizeKey(chi.URLParam(r, "key"))
	if key == "" {
		s.writeError(w, http.StatusBadRequest, "label key required")
		return
	}

	hasValue := r.URL.Query().Has("value")
	value := normalizeValue(r.URL.Query().Get("value"))

	resp := map[string]any{"key": key}
	if hasValue {
		resp["value"] = value
	}

	// Delete and emit a single coarse label.removed for the vocabulary change. A
	// bulk delete can touch many transactions, so it is one event (entity = the
	// label key) rather than a per-transaction fan-out; granular label.removed
	// events are reserved for single-transaction label edits (see Recorder.EmitLabelDiff).
	var removed int64
	err := s.emitter.Record(r.Context(), s.store, func(q db.Querier, rec *events.Recorder) error {
		var derr error
		if hasValue {
			removed, derr = q.DeleteLabelByValue(r.Context(), db.DeleteLabelByValueParams{LabelKey: key, LabelValue: value})
		} else {
			removed, derr = q.DeleteLabelByKey(r.Context(), key)
		}
		if derr != nil || removed == 0 {
			return derr // nothing removed -> nothing to emit
		}
		payload := events.LabelDeletedPayload{Key: key, RemovedFrom: removed}
		if hasValue {
			payload.Value = value
		}
		return rec.Emit(r.Context(), q, events.TypeLabelRemoved, events.EntityLabel, key, payload)
	})
	if err != nil {
		s.serverError(w, "delete label", err)
		return
	}
	resp["removed_from"] = removed
	s.writeJSON(w, http.StatusOK, resp)
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

// labelFilter is an optional drill-down by label. key == "" means no filter.
// hasValue distinguishes "key present with any value" from "key equals value"
// (an empty value is not a valid label, so the param's presence is the signal).
type labelFilter struct {
	key      string
	value    string
	hasValue bool
}

// parseLabelFilter reads label_key / label_value query params, canonicalizing
// them the same way stored labels are so they match.
func parseLabelFilter(r *http.Request) labelFilter {
	q := r.URL.Query()
	lf := labelFilter{key: normalizeKey(q.Get("label_key"))}
	if q.Has("label_value") {
		lf.hasValue = true
		lf.value = normalizeValue(q.Get("label_value"))
	}
	return lf
}

// queryTransactions selects transactions for an optional account filter, date
// range, pagination, and optional label drill-down. A label filter (when set)
// is pushed down to SQL via the per-dialect FilterTransactionsByLabel* queries,
// which also honor the account/date/pagination bounds. accountID == "" disables
// the account filter.
func (s *Server) queryTransactions(ctx context.Context, accountID string, p listParams, lf labelFilter) ([]db.Transaction, error) {
	switch {
	case lf.key != "" && lf.hasValue:
		return s.store.FilterTransactionsByLabelValue(ctx, db.FilterTransactionsByLabelValueParams{
			LabelKey:   lf.key,
			LabelValue: lf.value,
			AccountID:  accountID,
			Since:      p.since,
			Until:      p.until,
			RowLimit:   p.limit,
			RowOffset:  p.offset,
		})
	case lf.key != "":
		return s.store.FilterTransactionsByLabelKey(ctx, db.FilterTransactionsByLabelKeyParams{
			LabelKey:  lf.key,
			AccountID: accountID,
			Since:     p.since,
			Until:     p.until,
			RowLimit:  p.limit,
			RowOffset: p.offset,
		})
	case accountID != "":
		return s.store.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
			AccountID: accountID,
			Since:     p.since,
			Until:     p.until,
			RowLimit:  p.limit,
			RowOffset: p.offset,
		})
	default:
		return s.store.ListTransactions(ctx, db.ListTransactionsParams{
			Since:     p.since,
			Until:     p.until,
			RowLimit:  p.limit,
			RowOffset: p.offset,
		})
	}
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
