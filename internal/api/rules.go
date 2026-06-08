package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/rules"
	"github.com/paulmeier/kasas/internal/search"
)

// RuleDTO is the JSON representation of a rule: a condition (a kasas search
// query) and the labels and/or schema extensions applied to every transaction it
// matches.
type RuleDTO struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Query      string            `json:"query"`
	Labels     map[string]string `json:"labels"`
	Extensions map[string]any    `json:"extensions"`
	Enabled    bool              `json:"enabled"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

func toRuleDTO(r db.Rule) RuleDTO {
	return RuleDTO{
		ID:         r.ID,
		Name:       r.Name,
		Query:      r.Query,
		Labels:     decodeLabels(r.Labels),
		Extensions: decodeExtensions(r.Extensions),
		Enabled:    r.Enabled != 0,
		CreatedAt:  unixTime(r.CreatedAt),
		UpdatedAt:  unixTime(r.UpdatedAt),
	}
}

func toRuleDTOs(in []db.Rule) []RuleDTO {
	out := make([]RuleDTO, len(in))
	for i, r := range in {
		out[i] = toRuleDTO(r)
	}
	return out
}

// ruleInput is the create/update request body. The MCP create/update tool inputs
// mirror it but take extensions as map[string]any (the SDK rejects RawMessage in a
// tool schema) and round-trip them to raw JSON before building a ruleInput.
// Enabled is a pointer so an omitted field defaults to enabled.
type ruleInput struct {
	Name       string                     `json:"name"`
	Query      string                     `json:"query"`
	Labels     map[string]string          `json:"labels"`
	Extensions map[string]json.RawMessage `json:"extensions"`
	Enabled    *bool                      `json:"enabled"`
}

// validateRule checks the input shared by create and update: the query must be
// non-empty (a rule needs a real condition — an empty query would match every
// transaction) and parse cleanly, and the action must normalize to at least one
// label or extension. It returns the canonical query string plus the JSON-encoded
// labels and extensions.
func validateRule(in ruleInput) (query, encodedLabels, encodedExtensions string, err error) {
	query = strings.TrimSpace(in.Query)
	if query == "" {
		return "", "", "", errors.New("a rule must have a query")
	}
	if _, perr := search.Parse(query); perr != nil {
		return "", "", "", fmt.Errorf("invalid query: %s", perr.Error())
	}
	normLabels := normalizeLabels(in.Labels)
	normExt := normalizeExtensions(in.Extensions)
	if len(normLabels) == 0 && len(normExt) == 0 {
		return "", "", "", errors.New("a rule must apply at least one label or extension")
	}
	encodedLabels, err = encodeLabels(normLabels)
	if err != nil {
		return "", "", "", err
	}
	encodedExtensions, err = encodeExtensions(normExt)
	if err != nil {
		return "", "", "", err
	}
	return query, encodedLabels, encodedExtensions, nil
}

func (s *Server) handleListRules(w http.ResponseWriter, r *http.Request) {
	rs, err := s.store.ListRules(r.Context())
	if err != nil {
		s.serverError(w, "list rules", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"rules": toRuleDTOs(rs)})
}

func (s *Server) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeRuleInput(w, r)
	if !ok {
		return
	}
	rule, err := s.createRule(r.Context(), in)
	if err != nil {
		if isValidationError(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "create rule", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toRuleDTO(rule))
}

func (s *Server) handleGetRule(w http.ResponseWriter, r *http.Request) {
	id, err := ruleIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.store.GetRule(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err != nil {
		s.serverError(w, "get rule", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toRuleDTO(rule))
}

func (s *Server) handleUpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := ruleIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	in, ok := s.decodeRuleInput(w, r)
	if !ok {
		return
	}
	rule, err := s.updateRule(r.Context(), id, in)
	if err != nil {
		switch {
		case isValidationError(err):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errRuleNotFound):
			s.writeError(w, http.StatusNotFound, "rule not found")
		default:
			s.serverError(w, "update rule", err)
		}
		return
	}
	s.writeJSON(w, http.StatusOK, toRuleDTO(rule))
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := ruleIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	deleted, err := s.deleteRule(r.Context(), id)
	if err != nil {
		s.serverError(w, "delete rule", err)
		return
	}
	if !deleted {
		s.writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

// handleRunRule runs one rule over every existing transaction, applying its
// labels to matches. A rule is run by id even if it is disabled (an explicit,
// one-off action).
func (s *Server) handleRunRule(w http.ResponseWriter, r *http.Request) {
	id, err := ruleIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid rule id")
		return
	}
	rule, err := s.store.GetRule(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeError(w, http.StatusNotFound, "rule not found")
		return
	}
	if err != nil {
		s.serverError(w, "get rule", err)
		return
	}
	compiled, err := rules.Compile(ruleFromDB(rule))
	if err != nil {
		s.serverError(w, "compile rule", err)
		return
	}
	matched, updated, err := s.applyRules(r.Context(), []rules.Compiled{compiled}, id)
	if err != nil {
		s.serverError(w, "run rule", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"matched": matched, "updated": updated})
}

// handleRunAllRules runs every enabled rule over every existing transaction.
func (s *Server) handleRunAllRules(w http.ResponseWriter, r *http.Request) {
	compiled, err := s.enabledCompiledRules(r.Context())
	if err != nil {
		s.serverError(w, "load rules", err)
		return
	}
	matched, updated, err := s.applyRules(r.Context(), compiled, 0)
	if err != nil {
		s.serverError(w, "run rules", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"matched": matched, "updated": updated})
}

// --- shared service helpers (reused by the MCP tools) ---

var errRuleNotFound = errors.New("rule not found")

// validationError marks an error as a client (400) error rather than a server
// (500) error.
type validationError struct{ err error }

func (e validationError) Error() string { return e.err.Error() }

func isValidationError(err error) bool {
	var v validationError
	return errors.As(err, &v)
}

// createRule validates and inserts a rule. Validation failures are returned as
// validationError so handlers map them to 400.
func (s *Server) createRule(ctx context.Context, in ruleInput) (db.Rule, error) {
	query, encodedLabels, encodedExt, err := validateRule(in)
	if err != nil {
		return db.Rule{}, validationError{err}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	now := time.Now().Unix()
	var created db.Rule
	err = s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		var cerr error
		created, cerr = q.CreateRule(ctx, db.CreateRuleParams{
			Name:       strings.TrimSpace(in.Name),
			Query:      query,
			Labels:     encodedLabels,
			Extensions: encodedExt,
			Enabled:    boolToInt64(enabled),
			CreatedAt:  now,
			UpdatedAt:  now,
		})
		if cerr != nil {
			return cerr
		}
		return rec.Emit(ctx, q, events.TypeRuleCreated, events.EntityRule, events.EntityID(created.ID), events.RuleSnapshot(created))
	})
	if err != nil {
		return db.Rule{}, err
	}
	return created, nil
}

// updateRule validates and replaces a rule's editable fields, returning the
// canonical stored rule. errRuleNotFound signals an unknown id.
func (s *Server) updateRule(ctx context.Context, id int64, in ruleInput) (db.Rule, error) {
	query, encodedLabels, encodedExt, err := validateRule(in)
	if err != nil {
		return db.Rule{}, validationError{err}
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var updated db.Rule
	err = s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		n, uerr := q.UpdateRule(ctx, db.UpdateRuleParams{
			ID:         id,
			Name:       strings.TrimSpace(in.Name),
			Query:      query,
			Labels:     encodedLabels,
			Extensions: encodedExt,
			Enabled:    boolToInt64(enabled),
			UpdatedAt:  time.Now().Unix(),
		})
		if uerr != nil {
			return uerr
		}
		if n == 0 {
			return errRuleNotFound
		}
		var gerr error
		updated, gerr = q.GetRule(ctx, id)
		if gerr != nil {
			return gerr
		}
		return rec.Emit(ctx, q, events.TypeRuleUpdated, events.EntityRule, events.EntityID(id), events.RuleSnapshot(updated))
	})
	if err != nil {
		return db.Rule{}, err
	}
	return updated, nil
}

// deleteRule deletes a rule by id, emitting rule.deleted (carrying the rule's last
// state) when one was actually removed. It reports whether a rule was deleted so
// callers can map a miss to 404 / a not-found tool error. Shared by REST and MCP.
func (s *Server) deleteRule(ctx context.Context, id int64) (bool, error) {
	deleted := false
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		rule, gerr := q.GetRule(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			return nil // not found; deleted stays false
		}
		if gerr != nil {
			return gerr
		}
		n, derr := q.DeleteRule(ctx, id)
		if derr != nil {
			return derr
		}
		if n == 0 {
			return nil
		}
		deleted = true
		return rec.Emit(ctx, q, events.TypeRuleDeleted, events.EntityRule, events.EntityID(id), events.RuleSnapshot(rule))
	})
	return deleted, err
}

// applyRules runs the compiled rules over every stored transaction, writing the
// merged labels and/or extensions for each transaction whose set changed. All
// writes happen in one transaction. It returns the number of transactions matched
// by at least one rule and the number actually updated (a match whose labels and
// extensions were all already present is matched but not updated).
func (s *Server) applyRules(ctx context.Context, compiled []rules.Compiled, ruleID int64) (matched, updated int, err error) {
	if len(compiled) == 0 {
		return 0, 0, nil
	}

	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return 0, 0, err
	}
	names := make(map[string]string, len(accounts))
	for _, a := range accounts {
		names[a.ID] = a.Name
	}

	txns, err := s.allTransactions(ctx)
	if err != nil {
		return 0, 0, err
	}

	err = s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		for _, t := range txns {
			sr := toSearchRecord(t, names[t.AccountID])
			if anyMatch(compiled, sr) {
				matched++
			}

			oldLabels := decodeLabels(t.Labels)
			mergedLabels, labelsChanged := rules.Apply(compiled, sr, oldLabels)
			oldExt := decodeExtensionsRaw(t.Extensions)
			mergedExt, extChanged := rules.ApplyExtensions(compiled, sr, oldExt)
			if !labelsChanged && !extChanged {
				continue
			}

			// Labels and extensions are independent mutation seams: write each that
			// changed and record a version per seam (labeled, then extended). next
			// accumulates the run's changes so each version's diff-on-read is exactly
			// that seam's change; VersionChange synthesizes a v1 baseline from the
			// prior state only on the first version a transaction predating history gets.
			next := t
			if labelsChanged {
				encoded, eerr := encodeLabels(mergedLabels)
				if eerr != nil {
					return eerr
				}
				if _, eerr := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: t.ID, Labels: encoded}); eerr != nil {
					return eerr
				}
				if eerr := rec.EmitLabelDiff(ctx, q, t.ID, oldLabels, mergedLabels); eerr != nil {
					return eerr
				}
				next.Labels = encoded
				if eerr := rec.VersionChange(ctx, q, t.ID, events.TransactionSnapshot(t), events.TransactionSnapshot(next), events.ChangeLabeled); eerr != nil {
					return eerr
				}
			}
			if extChanged {
				beforeExt := events.TransactionSnapshot(next) // state before this extension change
				encoded, eerr := encodeExtensions(mergedExt)
				if eerr != nil {
					return eerr
				}
				if _, eerr := q.UpdateTransactionExtensions(ctx, db.UpdateTransactionExtensionsParams{ID: t.ID, Extensions: encoded}); eerr != nil {
					return eerr
				}
				if eerr := rec.EmitExtensionDiff(ctx, q, t.ID, oldExt, mergedExt); eerr != nil {
					return eerr
				}
				next.Extensions = encoded
				if eerr := rec.VersionChange(ctx, q, t.ID, beforeExt, events.TransactionSnapshot(next), events.ChangeExtended); eerr != nil {
					return eerr
				}
			}
			updated++
		}
		// One rule.executed summarizes the run (ruleID 0 means run-all-enabled).
		entityID := ""
		if ruleID > 0 {
			entityID = events.EntityID(ruleID)
		}
		return rec.Emit(ctx, q, events.TypeRuleExecuted, events.EntityRule, entityID, events.RuleExecutedPayload{
			RuleID:  ruleID,
			Matched: matched,
			Updated: updated,
		})
	})
	if err != nil {
		return 0, 0, err
	}
	return matched, updated, nil
}

// enabledCompiledRules loads and compiles every enabled rule, skipping (and
// logging) any whose stored query fails to parse.
func (s *Server) enabledCompiledRules(ctx context.Context) ([]rules.Compiled, error) {
	rows, err := s.store.ListEnabledRules(ctx)
	if err != nil {
		return nil, err
	}
	compiled := make([]rules.Compiled, 0, len(rows))
	for _, r := range rows {
		c, cerr := rules.Compile(ruleFromDB(r))
		if cerr != nil {
			s.logger.Warn("skipping rule with invalid query", "rule_id", r.ID, "error", cerr)
			continue
		}
		compiled = append(compiled, c)
	}
	return compiled, nil
}

// anyMatch reports whether at least one compiled rule matches the record.
func anyMatch(compiled []rules.Compiled, rec search.Record) bool {
	for _, c := range compiled {
		if c.Matches(rec) {
			return true
		}
	}
	return false
}

// ruleFromDB adapts a stored rule row into the engine's Rule (decoding labels and
// extensions).
func ruleFromDB(r db.Rule) rules.Rule {
	return rules.NewRule(r.ID, r.Name, r.Query, r.Labels, r.Extensions, r.Enabled != 0)
}

// decodeRuleInput reads and JSON-decodes a rule request body, writing a 400 and
// returning ok=false on a malformed body.
func (s *Server) decodeRuleInput(w http.ResponseWriter, r *http.Request) (ruleInput, bool) {
	var in ruleInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)) // 16 KiB is plenty
	if err := dec.Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return ruleInput{}, false
	}
	return in, true
}

func ruleIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
