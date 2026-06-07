package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/relationships"
)

// Relationships are stored on a transaction as a JSON array of {kind,target} edges
// asserted OUTBOUND from that transaction (see the 00012 migration), e.g.
// '[{"kind":"refund_of","target":"txn_123"}]'. The canonical model (encode/decode
// and the normalization rules: lowercase-identifier kinds, trimmed targets,
// de-duplicated, capped, sorted) lives in internal/relationships so the REST API
// and the MCP writer agree on the stored form. The inbound direction is derived by
// scanning, never stored, so an edge has a single home.

// decodeRelationships decodes a transaction's stored outbound edges. Never nil.
func decodeRelationships(stored string) []relationships.Relationship {
	return relationships.Decode(stored)
}

// relationshipKindCounts explodes stored outbound edges into the global
// relationship-kind vocabulary: one entry per distinct kind with the number of
// edges using it, sorted by kind. Built in Go (not SQL) to stay portable across
// SQLite and Postgres, like extensionCounts. Powers GET /relationships and the
// list_relationship_kinds MCP tool.
func relationshipKindCounts(sets []string) []RelationshipKindDTO {
	counts := make(map[string]int)
	for _, set := range sets {
		for _, e := range decodeRelationships(set) {
			counts[e.Kind]++
		}
	}
	out := make([]RelationshipKindDTO, 0, len(counts))
	for k, n := range counts {
		out = append(out, RelationshipKindDTO{Kind: k, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// mutateRelationships atomically transforms a transaction's OUTBOUND relationship
// set: it reads the current edges, applies transform, normalizes the result, then
// validates every NEWLY added edge (no self-reference; the target must exist),
// writes the new set, and emits the per-edge relationship.created /
// relationship.removed diff. Unlike labels/extensions it records NO transaction
// version — an edge is not a field of the transaction's own state. Shared by the
// REST handlers and the MCP tools. notFound is true (with a nil error) when id is
// unknown; a bad edge is a validationError (mapped to 400). Adding an edge that is
// already present, or removing one that is absent, is an idempotent no-op.
func (s *Server) mutateRelationships(ctx context.Context, id string, transform func(current []relationships.Relationship) []relationships.Relationship) (db.Transaction, bool, error) {
	var (
		notFound bool
		next     db.Transaction
	)
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		prev, gerr := q.GetTransaction(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		oldRels := decodeRelationships(prev.Relationships)
		newRels := relationships.Normalize(transform(oldRels))

		// Validate only the edges that are new this call: a removal never triggers a
		// target lookup, and re-asserting an existing edge stays cheap.
		oldKeys := make(map[string]struct{}, len(oldRels))
		for _, e := range oldRels {
			oldKeys[e.Key()] = struct{}{}
		}
		for _, e := range newRels {
			if _, existed := oldKeys[e.Key()]; existed {
				continue
			}
			if e.Target == id {
				return validationError{errors.New("a transaction cannot relate to itself")}
			}
			if _, terr := q.GetTransaction(ctx, e.Target); errors.Is(terr, sql.ErrNoRows) {
				return validationError{fmt.Errorf("target transaction %q does not exist", e.Target)}
			} else if terr != nil {
				return terr
			}
		}

		encoded, eerr := relationships.Encode(newRels)
		if eerr != nil {
			return eerr
		}
		if _, uerr := q.UpdateTransactionRelationships(ctx, db.UpdateTransactionRelationshipsParams{ID: id, Relationships: encoded}); uerr != nil {
			return uerr
		}
		next = prev
		next.Relationships = encoded
		return rec.EmitRelationshipDiff(ctx, q, id, oldRels, newRels)
	})
	if err != nil {
		return db.Transaction{}, false, err
	}
	return next, notFound, nil
}

// addRelationship asserts one outbound edge (id --kind--> target). Shared by the
// REST POST handler and the create_transaction_relationship MCP tool.
func (s *Server) addRelationship(ctx context.Context, id, kind, target string) (db.Transaction, bool, error) {
	return s.mutateRelationships(ctx, id, func(current []relationships.Relationship) []relationships.Relationship {
		return append(current, relationships.Relationship{Kind: kind, Target: target})
	})
}

// removeRelationship drops one outbound edge (id --kind--> target), matching the
// stored, normalized form. Shared by the REST DELETE handler and the
// delete_transaction_relationship MCP tool.
func (s *Server) removeRelationship(ctx context.Context, id, kind, target string) (db.Transaction, bool, error) {
	want := relationships.Relationship{Kind: relationships.NormalizeKind(kind), Target: strings.TrimSpace(target)}
	return s.mutateRelationships(ctx, id, func(current []relationships.Relationship) []relationships.Relationship {
		out := make([]relationships.Relationship, 0, len(current))
		for _, e := range current {
			if e.Key() != want.Key() {
				out = append(out, e)
			}
		}
		return out
	})
}

// listTransactionRelationships returns a transaction's full relationship
// neighborhood: its own OUTBOUND edges plus the INBOUND edges of every other
// transaction that targets it (derived by scanning, since edges live only on the
// subject side). notFound is true when id is unknown. Output is deterministic:
// outbound first, then inbound, each by (kind, other id).
func (s *Server) listTransactionRelationships(ctx context.Context, id string) ([]RelationshipDTO, bool, error) {
	txn, err := s.store.GetTransaction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	out := []RelationshipDTO{}
	for _, e := range decodeRelationships(txn.Relationships) {
		out = append(out, RelationshipDTO{Kind: e.Kind, Direction: relationships.DirectionOutbound, OtherTransactionID: e.Target})
	}
	rows, err := s.store.ListRelatedTransactions(ctx)
	if err != nil {
		return nil, false, err
	}
	for _, row := range rows {
		if row.ID == id {
			continue // its own edges are outbound, already added above
		}
		for _, e := range decodeRelationships(row.Relationships) {
			if e.Target == id {
				out = append(out, RelationshipDTO{Kind: e.Kind, Direction: relationships.DirectionInbound, OtherTransactionID: row.ID})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Direction != out[j].Direction {
			return out[i].Direction == relationships.DirectionOutbound // outbound first
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].OtherTransactionID < out[j].OtherTransactionID
	})
	return out, false, nil
}

// relationshipRequest is the body of POST /transactions/{id}/relationships.
type relationshipRequest struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// handleGetTransactionRelationships serves GET /transactions/{id}/relationships:
// the transaction's full neighborhood (outbound + inbound). 404 on unknown id.
func (s *Server) handleGetTransactionRelationships(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rels, notFound, err := s.listTransactionRelationships(r.Context(), id)
	if err != nil {
		s.serverError(w, "list transaction relationships", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "relationships": rels})
}

// handleCreateTransactionRelationship serves POST /transactions/{id}/relationships,
// asserting one outbound edge {kind, target}. 400 on a missing kind/target, a
// self-reference, or an unknown target; 404 on an unknown subject; 201 with the
// updated neighborhood otherwise. Adding an existing edge is an idempotent success.
func (s *Server) handleCreateTransactionRelationship(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req relationshipRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if relationships.NormalizeKind(req.Kind) == "" {
		s.writeError(w, http.StatusBadRequest, "a relationship must have a kind")
		return
	}
	if strings.TrimSpace(req.Target) == "" {
		s.writeError(w, http.StatusBadRequest, "a relationship must have a target")
		return
	}

	_, notFound, err := s.addRelationship(r.Context(), id, req.Kind, req.Target)
	if isValidationError(err) {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		s.serverError(w, "create transaction relationship", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.respondRelationships(w, r, id, http.StatusCreated)
}

// handleDeleteTransactionRelationship serves
// DELETE /transactions/{id}/relationships?kind=&target=, dropping one outbound
// edge. 404 on an unknown subject; 200 with the updated neighborhood otherwise.
// Deleting an absent edge is an idempotent success.
func (s *Server) handleDeleteTransactionRelationship(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	kind := r.URL.Query().Get("kind")
	target := r.URL.Query().Get("target")
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(target) == "" {
		s.writeError(w, http.StatusBadRequest, "kind and target query parameters are required")
		return
	}

	_, notFound, err := s.removeRelationship(r.Context(), id, kind, target)
	if err != nil {
		s.serverError(w, "delete transaction relationship", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.respondRelationships(w, r, id, http.StatusOK)
}

// respondRelationships writes the transaction's current neighborhood as the
// response to a create/delete, so callers see the post-mutation state.
func (s *Server) respondRelationships(w http.ResponseWriter, r *http.Request, id string, status int) {
	rels, _, err := s.listTransactionRelationships(r.Context(), id)
	if err != nil {
		s.serverError(w, "list transaction relationships", err)
		return
	}
	s.writeJSON(w, status, map[string]any{"id": id, "relationships": rels})
}

// handleListRelationships serves GET /relationships: the global relationship-kind
// vocabulary (each distinct kind with the number of outbound edges using it).
func (s *Server) handleListRelationships(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListRelatedTransactions(r.Context())
	if err != nil {
		s.serverError(w, "list relationships", err)
		return
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Relationships
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"relationships": relationshipKindCounts(sets)})
}
