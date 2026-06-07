package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/provenance"
)

// buildProvenance assembles a transaction's provenance from data the ledger already
// keeps: source off the row, the institution from the account's organization
// (best-effort), last_seen from synced_at, and the transformation list from the
// version history. It is the shared seam for the REST handler and the MCP tool. The
// caller has already loaded (and existence-checked) txn.
func (s *Server) buildProvenance(ctx context.Context, txn db.Transaction) (provenance.Provenance, error) {
	rows, err := s.store.ListTransactionVersions(ctx, txn.ID)
	if err != nil {
		return provenance.Provenance{}, err
	}
	versions := make([]provenance.Version, 0, len(rows))
	for _, row := range rows {
		versions = append(versions, provenance.Version{
			Kind:       row.ChangeKind,
			OccurredAt: unixTime(row.OccurredAt),
			Snapshot:   decodeSnapshot(row.Data),
		})
	}
	return provenance.Build(provenance.Input{
		TransactionID: txn.ID,
		Source:        txn.Source,
		AccountID:     txn.AccountID,
		Institution:   s.lookupInstitution(ctx, txn.AccountID),
		LastSeen:      unixTime(txn.SyncedAt),
		Versions:      versions,
	}), nil
}

// lookupInstitution resolves a transaction's institution by walking
// account -> organization, preferring the org name and falling back to its domain.
// It is best-effort lineage enrichment: any miss (unknown account, no org, blank
// name) yields "", which Provenance omits.
func (s *Server) lookupInstitution(ctx context.Context, accountID string) string {
	acct, err := s.store.GetAccount(ctx, accountID)
	if err != nil {
		return ""
	}
	org, err := s.store.GetOrganization(ctx, acct.OrgID)
	if err != nil {
		return ""
	}
	if org.Name != "" {
		return org.Name
	}
	return org.Domain
}

// handleGetTransactionProvenance returns one transaction's provenance: where it came
// from and how it reached its current state. 404 if the transaction does not exist.
func (s *Server) handleGetTransactionProvenance(w http.ResponseWriter, r *http.Request) {
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
	prov, err := s.buildProvenance(r.Context(), txn)
	if err != nil {
		s.serverError(w, "build provenance", err)
		return
	}
	s.writeJSON(w, http.StatusOK, prov)
}
