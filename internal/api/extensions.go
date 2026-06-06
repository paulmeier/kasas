package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/extensions"
)

// Schema extensions are stored on a transaction as a JSON object of namespaced
// key->arbitrary-JSON-value pairs (see the 00009 migration), e.g.
// '{"tax.category":"meal","forecast.recurring":true,"custom.myapp.score":88}'.
// They are app-owned metadata, parallel to (not a replacement for) labels. The
// canonical model — JSON encode/decode and the normalization rules (trimmed,
// case-preserved keys; valid-JSON values; length/count/size caps) — lives in
// internal/extensions so the REST API and the MCP writer agree on the stored
// form. The unexported helpers here are thin aliases for readability.

// decodeExtensions decodes a stored extensions object for the output boundary
// (map[string]any, MCP-safe), mirroring how event Data is decoded to `any`.
func decodeExtensions(stored string) map[string]any { return extensions.Values(stored) }

// decodeExtensionsRaw decodes losslessly (raw JSON values) for the write path.
func decodeExtensionsRaw(stored string) map[string]json.RawMessage { return extensions.Decode(stored) }

func encodeExtensions(in map[string]json.RawMessage) (string, error) { return extensions.Encode(in) }
func normalizeExtensions(in map[string]json.RawMessage) map[string]json.RawMessage {
	return extensions.Normalize(in)
}

// extensionCounts explodes the stored JSON extension objects into the global
// extension vocabulary: one entry per distinct key, annotated with its namespace
// and the number of transactions carrying it. Each input string is one
// transaction's extensions object (unique keys), so each contributes at most once
// per key. The result is sorted by key. Built in Go (not SQL) to stay portable
// across SQLite and Postgres, like labelCounts; it powers the Extensions
// vocabulary endpoint / MCP tool.
func extensionCounts(sets []string) []ExtensionDTO {
	counts := make(map[string]int)
	for _, set := range sets {
		for k := range decodeExtensionsRaw(set) {
			counts[k]++
		}
	}
	out := make([]ExtensionDTO, 0, len(counts))
	for k, n := range counts {
		out = append(out, ExtensionDTO{Namespace: extensions.Namespace(k), Key: k, TransactionCount: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// updateExtensionsRequest is the body of PUT /transactions/{id}/extensions. It
// replaces the transaction's entire extensions set with the (normalized) pairs.
type updateExtensionsRequest struct {
	Extensions map[string]json.RawMessage `json:"extensions"`
}

// setExtensions replaces a transaction's whole schema-extensions set with the
// normalized form of raw, atomically: it reads the previous set, writes the new
// one, emits the per-key extension.set / extension.removed diff, and records an
// "extended" version (synthesizing a v1 baseline if the transaction predates
// history). Shared by the REST handler and the set_transaction_extensions MCP
// tool. Returns the updated row; notFound is true (with a nil error) when the id
// is unknown.
func (s *Server) setExtensions(ctx context.Context, id string, raw map[string]json.RawMessage) (db.Transaction, bool, error) {
	newExt := normalizeExtensions(raw)
	encoded, err := encodeExtensions(newExt)
	if err != nil {
		return db.Transaction{}, false, err
	}

	var (
		notFound bool
		next     db.Transaction
	)
	err = s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		prev, gerr := q.GetTransaction(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		if _, uerr := q.UpdateTransactionExtensions(ctx, db.UpdateTransactionExtensionsParams{ID: id, Extensions: encoded}); uerr != nil {
			return uerr
		}
		if derr := emitExtensionDiff(ctx, q, rec, id, decodeExtensionsRaw(prev.Extensions), newExt); derr != nil {
			return derr
		}
		next = prev
		next.Extensions = encoded
		return rec.VersionChange(ctx, q, id, events.TransactionSnapshot(prev), events.TransactionSnapshot(next), events.ChangeExtended)
	})
	if err != nil {
		return db.Transaction{}, false, err
	}
	return next, notFound, nil
}

// emitExtensionDiff records an extension.set event for every key added or changed
// and an extension.removed for every key dropped between a transaction's old and
// new extension sets. Values are compared as raw JSON bytes (already compacted by
// normalization). The event entity is the transaction.
func emitExtensionDiff(ctx context.Context, q db.Querier, rec *events.Recorder, txnID string, oldExt, newExt map[string]json.RawMessage) error {
	for k, v := range newExt {
		if old, ok := oldExt[k]; !ok || !bytes.Equal(old, v) {
			if err := rec.Emit(ctx, q, events.TypeExtensionSet, events.EntityTransaction, txnID, events.ExtensionPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	for k, v := range oldExt {
		if _, ok := newExt[k]; !ok {
			if err := rec.Emit(ctx, q, events.TypeExtensionRemoved, events.EntityTransaction, txnID, events.ExtensionPayload{TransactionID: txnID, Key: k, Value: v}); err != nil {
				return err
			}
		}
	}
	return nil
}

// handleUpdateTransactionExtensions serves PUT /transactions/{id}/extensions,
// replacing the transaction's entire extensions object with a server-normalized
// one (case-preserved keys, valid-JSON values, caps applied). 404 on unknown id.
func (s *Server) handleUpdateTransactionExtensions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req updateExtensionsRequest
	// 256 KiB: extensions can hold small JSON blobs, so be more generous than labels.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	next, notFound, err := s.setExtensions(r.Context(), id, req.Extensions)
	if err != nil {
		s.serverError(w, "update transaction extensions", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "extensions": decodeExtensions(next.Extensions)})
}

// handleListExtensions serves GET /extensions: the global extension vocabulary
// (each distinct key with its namespace and transaction count).
func (s *Server) handleListExtensions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListExtendedTransactions(r.Context())
	if err != nil {
		s.serverError(w, "list extensions", err)
		return
	}
	sets := make([]string, len(rows))
	for i, row := range rows {
		sets[i] = row.Extensions
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"extensions": extensionCounts(sets)})
}
