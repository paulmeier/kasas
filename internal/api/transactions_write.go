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
	"github.com/google/uuid"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/ledger"
)

// manualSource is the provenance stamped on transactions and accounts created by a
// user through the API/dashboard, as opposed to "simplefin" rows owned by the
// bridge. It is the marker the manual-only edit/delete gate keys off.
const manualSource = "manual"

// errTxnReadOnly marks an attempt to edit or delete a non-manual (bridge-owned)
// transaction. Synced transactions' core fields are owned by the source and would
// be clobbered on the next sync, so they are read-only here. Handlers map this to
// 409 Conflict (the request is well-formed; the resource state forbids it).
var errTxnReadOnly = errors.New("only manually-created transactions can be edited or deleted; this transaction is owned by its source")

// transactionInput is the create/update request body for a manual transaction.
// Pending is a pointer so an omitted field defaults to false rather than being
// indistinguishable from an explicit false.
type transactionInput struct {
	AccountID   string `json:"account_id"`
	Amount      string `json:"amount"`
	Date        string `json:"date"` // YYYY-MM-DD, RFC3339, or unix seconds
	Description string `json:"description"`
	Payee       string `json:"payee"`
	Memo        string `json:"memo"`
	Pending     *bool  `json:"pending"`
}

// parseDateInput parses a required date given as unix seconds, RFC3339, or
// YYYY-MM-DD, returning unix seconds. Unlike parseTimeParam (which returns 0 for
// both empty and invalid input, fine for an optional query filter) this reports an
// error, because a manual transaction must carry a real date.
func parseDateInput(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("date is required")
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix(), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.Unix(), nil
	}
	return 0, errors.New("date must be YYYY-MM-DD, RFC3339, or a unix timestamp")
}

// validateTransactionInput normalizes and validates a transaction body, returning
// the cleaned account id, canonical amount, date (unix seconds), and pending flag.
// Validation failures are plain errors; callers wrap them as validationError.
func validateTransactionInput(in transactionInput) (accountID, amount string, date int64, pending bool, err error) {
	accountID = strings.TrimSpace(in.AccountID)
	if accountID == "" {
		return "", "", 0, false, errors.New("account_id is required")
	}
	amount, err = validateAmount(in.Amount)
	if err != nil {
		return "", "", 0, false, err
	}
	date, err = parseDateInput(in.Date)
	if err != nil {
		return "", "", 0, false, err
	}
	if in.Pending != nil {
		pending = *in.Pending
	}
	return accountID, amount, date, pending, nil
}

// createManualTransaction inserts a user-entered transaction (source="manual"),
// emitting transaction.created and recording its v1 imported history snapshot — the
// same birth path the poller runs for a synced row, so provenance reads "imported
// from manual". The account must exist (a clean 400 rather than a raw FK error).
// Shared by the REST handler and the create_transaction MCP tool.
func (s *Server) createManualTransaction(ctx context.Context, in transactionInput) (db.Transaction, error) {
	accountID, amount, date, pending, verr := validateTransactionInput(in)
	if verr != nil {
		return db.Transaction{}, validationError{verr}
	}
	id := "man_" + uuid.NewString()
	now := time.Now().Unix()

	var created db.Transaction
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		if _, aerr := q.GetAccount(ctx, accountID); errors.Is(aerr, sql.ErrNoRows) {
			return validationError{fmt.Errorf("account %q does not exist", accountID)}
		} else if aerr != nil {
			return aerr
		}
		n, ierr := q.InsertTransaction(ctx, db.InsertTransactionParams{
			ID:          id,
			AccountID:   accountID,
			Amount:      amount,
			Pending:     boolToInt64(pending),
			Date:        date,
			Description: strings.TrimSpace(in.Description),
			Payee:       strings.TrimSpace(in.Payee),
			Memo:        strings.TrimSpace(in.Memo),
			SyncedAt:    now,
			Source:      manualSource,
		})
		if ierr != nil {
			return ierr
		}
		if n == 0 {
			// Impossible with a freshly minted uuid; treat a collision as a server error
			// rather than silently no-op'ing the create.
			return fmt.Errorf("transaction id collision for %q", id)
		}
		var gerr error
		if created, gerr = q.GetTransaction(ctx, id); gerr != nil {
			return gerr
		}
		if eerr := rec.Emit(ctx, q, events.TypeTransactionCreated, events.EntityTransaction, id, events.TransactionSnapshot(created)); eerr != nil {
			return eerr
		}
		return rec.Version(ctx, q, id, events.TransactionSnapshot(created), events.ChangeImported)
	})
	if err != nil {
		return db.Transaction{}, err
	}
	return created, nil
}

// updateTransactionCore edits a manual transaction's core fields, emitting
// transaction.updated and recording an "edited" history version (with a synthesized
// v1 baseline if needed). notFound is true (nil error) for an unknown id;
// errTxnReadOnly for a non-manual row. Shared by REST and the update_transaction
// MCP tool.
func (s *Server) updateTransactionCore(ctx context.Context, id string, in transactionInput) (db.Transaction, bool, error) {
	accountID, amount, date, pending, verr := validateTransactionInput(in)
	if verr != nil {
		return db.Transaction{}, false, validationError{verr}
	}
	now := time.Now().Unix()

	var (
		notFound bool
		updated  db.Transaction
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
		if prev.Source != manualSource {
			return errTxnReadOnly
		}
		if _, aerr := q.GetAccount(ctx, accountID); errors.Is(aerr, sql.ErrNoRows) {
			return validationError{fmt.Errorf("account %q does not exist", accountID)}
		} else if aerr != nil {
			return aerr
		}
		n, uerr := q.UpdateTransactionCore(ctx, db.UpdateTransactionCoreParams{
			ID:          id,
			AccountID:   accountID,
			Amount:      amount,
			Pending:     boolToInt64(pending),
			Date:        date,
			Description: strings.TrimSpace(in.Description),
			Payee:       strings.TrimSpace(in.Payee),
			Memo:        strings.TrimSpace(in.Memo),
			SyncedAt:    now,
		})
		if uerr != nil {
			return uerr
		}
		if n == 0 {
			notFound = true
			return nil
		}
		var rerr error
		if updated, rerr = q.GetTransaction(ctx, id); rerr != nil {
			return rerr
		}
		if eerr := rec.Emit(ctx, q, events.TypeTransactionUpdated, events.EntityTransaction, id, events.TransactionSnapshot(updated)); eerr != nil {
			return eerr
		}
		return rec.VersionChange(ctx, q, id, events.TransactionSnapshot(prev), events.TransactionSnapshot(updated), events.ChangeEdited)
	})
	if err != nil {
		return db.Transaction{}, false, err
	}
	return updated, notFound, nil
}

// deleteTransaction removes a manual transaction. notFound is true (nil error) for
// an unknown id; errTxnReadOnly for a non-manual row. Shared by REST and the
// delete_transaction MCP tool.
func (s *Server) deleteTransaction(ctx context.Context, id string) (bool, error) {
	notFound := false
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		txn, gerr := q.GetTransaction(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		if txn.Source != manualSource {
			return errTxnReadOnly
		}
		return ledger.DeleteTransactionTx(ctx, q, rec, txn)
	})
	return notFound, err
}

// --- REST handlers ---

func (s *Server) handleCreateTransaction(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeTransactionInput(w, r)
	if !ok {
		return
	}
	txn, err := s.createManualTransaction(r.Context(), in)
	if err != nil {
		if isValidationError(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "create transaction", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toTransactionDTO(txn))
}

func (s *Server) handleUpdateTransaction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	in, ok := s.decodeTransactionInput(w, r)
	if !ok {
		return
	}
	txn, notFound, err := s.updateTransactionCore(r.Context(), id, in)
	if err != nil {
		switch {
		case isValidationError(err):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errTxnReadOnly):
			s.writeError(w, http.StatusConflict, err.Error())
		default:
			s.serverError(w, "update transaction", err)
		}
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.writeJSON(w, http.StatusOK, toTransactionDTO(txn))
}

func (s *Server) handleDeleteTransaction(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	notFound, err := s.deleteTransaction(r.Context(), id)
	if err != nil {
		if errors.Is(err, errTxnReadOnly) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.serverError(w, "delete transaction", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "transaction not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (s *Server) decodeTransactionInput(w http.ResponseWriter, r *http.Request) (transactionInput, bool) {
	var in transactionInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return transactionInput{}, false
	}
	return in, true
}
