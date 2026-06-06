package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// HistoryDTO is the JSON representation of a transaction's immutable history: an
// ordered list of full snapshots, oldest first. It is the body of both
// GET /api/v1/transactions/{id}/history and the get_transaction_history MCP tool.
type HistoryDTO struct {
	TransactionID string       `json:"transaction_id"`
	Versions      []VersionDTO `json:"versions"`
}

// VersionDTO is one entry in a transaction's history: a full snapshot at that
// version plus the diff against the previous version. `version` is the 1-based
// ordinal (assigned on read from the stored insert order); `change_kind` is
// imported, synced, or labeled.
type VersionDTO struct {
	Version     int                       `json:"version"`
	ChangeKind  string                    `json:"change_kind"`
	OccurredAt  time.Time                 `json:"occurred_at"`
	Transaction events.TransactionPayload `json:"transaction"`
	Diff        VersionDiffDTO            `json:"diff"`
}

// VersionDiffDTO is the wire form of events.VersionDiff: the scalar field changes
// plus the label and schema-extension add/remove/change deltas from the previous
// version. The first version diffs against an empty snapshot (a "birth" diff), so
// every entry — including v1 — carries a diff.
type VersionDiffDTO struct {
	Fields            []events.FieldChange      `json:"fields"`
	LabelsAdded       map[string]string         `json:"labels_added"`
	LabelsRemoved     map[string]string         `json:"labels_removed"`
	LabelsChanged     map[string]LabelChangeDTO `json:"labels_changed"`
	ExtensionsAdded   map[string]string         `json:"extensions_added"`
	ExtensionsRemoved map[string]string         `json:"extensions_removed"`
	ExtensionsChanged map[string]LabelChangeDTO `json:"extensions_changed"`
}

// LabelChangeDTO is a single label or extension whose value changed between
// versions (extension values are rendered as strings — see events.VersionDiff).
type LabelChangeDTO struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// buildHistory turns a transaction's stored version rows into the wire history,
// assigning the 1-based version ordinal (the rows arrive in insert order) and
// computing each version's diff against the previous snapshot. The first version
// diffs against the zero snapshot. txn supplies the id for the envelope so a
// known-but-unversioned transaction still returns its id with an empty list.
// Shared by the REST handler and the MCP tool.
func buildHistory(txn db.Transaction, rows []db.TransactionVersion) HistoryDTO {
	out := HistoryDTO{TransactionID: txn.ID, Versions: make([]VersionDTO, 0, len(rows))}
	var prev events.TransactionPayload
	for i, row := range rows {
		snap := decodeSnapshot(row.Data)
		out.Versions = append(out.Versions, VersionDTO{
			Version:     i + 1,
			ChangeKind:  row.ChangeKind,
			OccurredAt:  unixTime(row.OccurredAt),
			Transaction: snap,
			Diff:        toVersionDiffDTO(events.DiffSnapshots(prev, snap)),
		})
		prev = snap
	}
	return out
}

// decodeSnapshot parses a stored version's JSON snapshot into a TransactionPayload,
// returning a zero value (with a non-nil labels map) on missing or malformed data.
func decodeSnapshot(data string) events.TransactionPayload {
	var p events.TransactionPayload
	if data != "" {
		_ = json.Unmarshal([]byte(data), &p)
	}
	if p.Labels == nil {
		p.Labels = map[string]string{}
	}
	if p.Extensions == nil {
		p.Extensions = map[string]any{}
	}
	return p
}

// toVersionDiffDTO converts the internal diff into its wire form, flattening the
// changed-label [from,to] pairs into structs and ensuring slices/maps render as
// [] / {} rather than null.
func toVersionDiffDTO(d events.VersionDiff) VersionDiffDTO {
	changed := make(map[string]LabelChangeDTO, len(d.LabelsChanged))
	for k, v := range d.LabelsChanged {
		changed[k] = LabelChangeDTO{From: v[0], To: v[1]}
	}
	extChanged := make(map[string]LabelChangeDTO, len(d.ExtensionsChanged))
	for k, v := range d.ExtensionsChanged {
		extChanged[k] = LabelChangeDTO{From: v[0], To: v[1]}
	}
	fields := d.Fields
	if fields == nil {
		fields = []events.FieldChange{}
	}
	return VersionDiffDTO{
		Fields:            fields,
		LabelsAdded:       d.LabelsAdded,
		LabelsRemoved:     d.LabelsRemoved,
		LabelsChanged:     changed,
		ExtensionsAdded:   d.ExtensionsAdded,
		ExtensionsRemoved: d.ExtensionsRemoved,
		ExtensionsChanged: extChanged,
	}
}

// handleGetTransactionHistory returns the full version history of one transaction,
// oldest first, each version carrying its snapshot and a diff against the previous
// one. 404 if the transaction does not exist; 200 with an empty list if it exists
// but has no recorded versions yet (it predates history and has not changed since).
func (s *Server) handleGetTransactionHistory(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.store.ListTransactionVersions(r.Context(), id)
	if err != nil {
		s.serverError(w, "list transaction versions", err)
		return
	}
	s.writeJSON(w, http.StatusOK, buildHistory(txn, rows))
}
