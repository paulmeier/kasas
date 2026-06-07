package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
)

// All manually-created accounts hang off a single reserved organization. The
// account's source column (not its org) is what marks it manual, so this org is
// just a home for the FK; it is created lazily the first time a manual account is
// added. Keeping manual-ness on source (mirroring transactions.source) means a
// future enhancement can let manual accounts carry real institutions without
// touching the edit/delete gate.
const (
	manualOrgID   = "manual"
	manualOrgName = "Manual"
)

// errAccountReadOnly marks an attempt to edit or delete a non-manual (bridge-owned)
// account. Synced accounts are owned by the source, so they are read-only here.
// Handlers map this to 409 Conflict.
var errAccountReadOnly = errors.New("only manually-created accounts can be edited or deleted; this account is owned by its source")

// accountInput is the create/update request body for a manual account. Balance and
// BalanceDate are optional: balance defaults to "0" on create (and is left
// unchanged on update when omitted); balance_date defaults to now.
type accountInput struct {
	Name        string `json:"name"`
	Currency    string `json:"currency"`
	Balance     string `json:"balance"`
	BalanceDate string `json:"balance_date"` // YYYY-MM-DD, RFC3339, or unix seconds
}

// createManualAccount inserts a user-created account (source="manual") under the
// reserved Manual organization and emits account.created. The balance is a
// user-maintained value (kasas does not derive it from the account's transactions).
// Shared by the REST handler and the create_account MCP tool.
func (s *Server) createManualAccount(ctx context.Context, in accountInput) (db.Account, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return db.Account{}, validationError{errors.New("name is required")}
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return db.Account{}, validationError{errors.New("currency is required")}
	}
	balance := "0"
	if strings.TrimSpace(in.Balance) != "" {
		v, err := validateAmount(in.Balance)
		if err != nil {
			return db.Account{}, validationError{fmt.Errorf("balance: %s", err)}
		}
		balance = v
	}
	balanceDate := time.Now().Unix()
	if strings.TrimSpace(in.BalanceDate) != "" {
		d, err := parseDateInput(in.BalanceDate)
		if err != nil {
			return db.Account{}, validationError{fmt.Errorf("balance_date: %s", err)}
		}
		balanceDate = d
	}
	id := "man_acct_" + uuid.NewString()
	now := time.Now().Unix()

	var created db.Account
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		if uerr := q.UpsertOrganization(ctx, db.UpsertOrganizationParams{ID: manualOrgID, Name: manualOrgName}); uerr != nil {
			return uerr
		}
		if uerr := q.UpsertAccount(ctx, db.UpsertAccountParams{
			ID:          id,
			OrgID:       manualOrgID,
			Name:        name,
			Currency:    currency,
			Balance:     balance,
			BalanceDate: balanceDate,
			SyncedAt:    now,
			Source:      manualSource,
		}); uerr != nil {
			return uerr
		}
		var gerr error
		if created, gerr = q.GetAccount(ctx, id); gerr != nil {
			return gerr
		}
		return rec.Emit(ctx, q, events.TypeAccountCreated, events.EntityAccount, id, events.AccountSnapshot(created))
	})
	if err != nil {
		return db.Account{}, err
	}
	return created, nil
}

// updateAccount edits a manual account's user-owned fields and emits account.updated.
// Omitted balance / balance_date are left unchanged. notFound is true (nil error)
// for an unknown id; errAccountReadOnly for a non-manual account. Shared by REST and
// the update_account MCP tool.
func (s *Server) updateAccount(ctx context.Context, id string, in accountInput) (db.Account, bool, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return db.Account{}, false, validationError{errors.New("name is required")}
	}
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		return db.Account{}, false, validationError{errors.New("currency is required")}
	}
	var balanceOverride *string
	if strings.TrimSpace(in.Balance) != "" {
		v, err := validateAmount(in.Balance)
		if err != nil {
			return db.Account{}, false, validationError{fmt.Errorf("balance: %s", err)}
		}
		balanceOverride = &v
	}
	var balanceDateOverride *int64
	if strings.TrimSpace(in.BalanceDate) != "" {
		d, err := parseDateInput(in.BalanceDate)
		if err != nil {
			return db.Account{}, false, validationError{fmt.Errorf("balance_date: %s", err)}
		}
		balanceDateOverride = &d
	}
	now := time.Now().Unix()

	var (
		notFound bool
		updated  db.Account
	)
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		prev, gerr := q.GetAccount(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		if prev.Source != manualSource {
			return errAccountReadOnly
		}
		balance := prev.Balance
		if balanceOverride != nil {
			balance = *balanceOverride
		}
		balanceDate := prev.BalanceDate
		if balanceDateOverride != nil {
			balanceDate = *balanceDateOverride
		}
		n, uerr := q.UpdateAccount(ctx, db.UpdateAccountParams{
			ID:          id,
			Name:        name,
			Currency:    currency,
			Balance:     balance,
			BalanceDate: balanceDate,
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
		if updated, rerr = q.GetAccount(ctx, id); rerr != nil {
			return rerr
		}
		return rec.Emit(ctx, q, events.TypeAccountUpdated, events.EntityAccount, id, events.AccountSnapshot(updated))
	})
	if err != nil {
		return db.Account{}, false, err
	}
	return updated, notFound, nil
}

// deleteAccount removes a manual account and all of its transactions. The row delete
// cascades to transactions in the database, but that cascade is invisible to the
// event stream and history, so this enumerates the account's transactions and
// deletes each explicitly (emitting transaction.deleted and cleaning versions +
// inbound relationship edges) BEFORE deleting the account and emitting
// account.deleted — a child-before-parent order so a replaying consumer tears down
// leaves first. notFound is true (nil error) for an unknown id; errAccountReadOnly
// for a non-manual account. Shared by REST and the delete_account MCP tool.
func (s *Server) deleteAccount(ctx context.Context, id string) (bool, error) {
	notFound := false
	err := s.emitter.Record(ctx, s.store, func(q db.Querier, rec *events.Recorder) error {
		acct, gerr := q.GetAccount(ctx, id)
		if errors.Is(gerr, sql.ErrNoRows) {
			notFound = true
			return nil
		}
		if gerr != nil {
			return gerr
		}
		if acct.Source != manualSource {
			return errAccountReadOnly
		}
		// Enumerate every transaction in the account (a large limit so none is missed —
		// the cascade would delete them regardless, but we must emit/clean them here).
		children, lerr := q.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
			AccountID: id, Since: 0, Until: 0, RowLimit: math.MaxInt32, RowOffset: 0,
		})
		if lerr != nil {
			return lerr
		}
		for _, child := range children {
			if derr := s.deleteTransactionTx(ctx, q, rec, child); derr != nil {
				return derr
			}
		}
		if _, derr := q.DeleteAccount(ctx, id); derr != nil {
			return derr
		}
		return rec.Emit(ctx, q, events.TypeAccountDeleted, events.EntityAccount, id, events.AccountSnapshot(acct))
	})
	return notFound, err
}

// --- REST handlers ---

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	in, ok := s.decodeAccountInput(w, r)
	if !ok {
		return
	}
	acct, err := s.createManualAccount(r.Context(), in)
	if err != nil {
		if isValidationError(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "create account", err)
		return
	}
	s.writeJSON(w, http.StatusCreated, toAccountDTO(acct))
}

func (s *Server) handleUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	in, ok := s.decodeAccountInput(w, r)
	if !ok {
		return
	}
	acct, notFound, err := s.updateAccount(r.Context(), id, in)
	if err != nil {
		switch {
		case isValidationError(err):
			s.writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, errAccountReadOnly):
			s.writeError(w, http.StatusConflict, err.Error())
		default:
			s.serverError(w, "update account", err)
		}
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "account not found")
		return
	}
	s.writeJSON(w, http.StatusOK, toAccountDTO(acct))
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	notFound, err := s.deleteAccount(r.Context(), id)
	if err != nil {
		if errors.Is(err, errAccountReadOnly) {
			s.writeError(w, http.StatusConflict, err.Error())
			return
		}
		s.serverError(w, "delete account", err)
		return
	}
	if notFound {
		s.writeError(w, http.StatusNotFound, "account not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
}

func (s *Server) decodeAccountInput(w http.ResponseWriter, r *http.Request) (accountInput, bool) {
	var in accountInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return accountInput{}, false
	}
	return in, true
}
