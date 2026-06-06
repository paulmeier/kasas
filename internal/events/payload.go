package events

import (
	"strconv"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/labels"
)

// The payload types below are the `data` object embedded in each event. They are
// self-contained snapshots, so a consumer can act without a follow-up query (and a
// *.deleted event still carries the entity's last-known state). They mirror the
// REST DTOs but are defined here to keep internal/events free of any dependency on
// the api package.

// TransactionPayload is the snapshot embedded in transaction.* and label.* events.
type TransactionPayload struct {
	ID          string            `json:"id"`
	AccountID   string            `json:"account_id"`
	Amount      string            `json:"amount"`
	Pending     bool              `json:"pending"`
	Date        time.Time         `json:"date"`
	Description string            `json:"description"`
	Payee       string            `json:"payee"`
	Memo        string            `json:"memo"`
	Labels      map[string]string `json:"labels"`
}

// TransactionSnapshot builds a TransactionPayload from a stored row.
func TransactionSnapshot(t db.Transaction) TransactionPayload {
	return TransactionPayload{
		ID:          t.ID,
		AccountID:   t.AccountID,
		Amount:      t.Amount,
		Pending:     t.Pending != 0,
		Date:        time.Unix(t.Date, 0).UTC(),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      labels.Decode(t.Labels),
	}
}

// AccountPayload is the snapshot embedded in account.* events.
type AccountPayload struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
	BalanceDate time.Time `json:"balance_date"`
}

// AccountSnapshot builds an AccountPayload from a stored row.
func AccountSnapshot(a db.Account) AccountPayload {
	return AccountPayload{
		ID:          a.ID,
		OrgID:       a.OrgID,
		Name:        a.Name,
		Currency:    a.Currency,
		Balance:     a.Balance,
		BalanceDate: time.Unix(a.BalanceDate, 0).UTC(),
	}
}

// LabelPayload is the data for a granular label.applied / label.removed event on a
// single transaction. The event's entity is that transaction (EntityTransaction),
// so the transaction id is repeated here for convenience.
type LabelPayload struct {
	TransactionID string `json:"transaction_id"`
	Key           string `json:"key"`
	Value         string `json:"value,omitempty"`
}

// LabelDeletedPayload is the data for the coarse label.removed event emitted when a
// label is deleted from the whole vocabulary (DELETE /labels/{key}). That event's
// entity is the label key itself (EntityLabel), not any single transaction.
type LabelDeletedPayload struct {
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	RemovedFrom int64  `json:"removed_from"`
}

// RulePayload is the snapshot embedded in rule.created / rule.updated /
// rule.deleted events.
type RulePayload struct {
	ID      int64             `json:"id"`
	Name    string            `json:"name"`
	Query   string            `json:"query"`
	Labels  map[string]string `json:"labels"`
	Enabled bool              `json:"enabled"`
}

// RuleSnapshot builds a RulePayload from a stored row.
func RuleSnapshot(r db.Rule) RulePayload {
	return RulePayload{
		ID:      r.ID,
		Name:    r.Name,
		Query:   r.Query,
		Labels:  labels.Decode(r.Labels),
		Enabled: r.Enabled != 0,
	}
}

// RuleExecutedPayload is the data for a rule.executed event: how many transactions
// the run matched and how many it newly labeled. RuleID is 0 for a run-all.
type RuleExecutedPayload struct {
	RuleID  int64 `json:"rule_id,omitempty"`
	Matched int   `json:"matched"`
	Updated int   `json:"updated"`
}

// SyncCompletedPayload is the data for a sync.completed event.
type SyncCompletedPayload struct {
	Accounts            int    `json:"accounts"`
	NewTransactions     int    `json:"new_transactions"`
	UpdatedTransactions int    `json:"updated_transactions"`
	AutoLabeled         int    `json:"auto_labeled"`
	Duration            string `json:"duration"`
}

// EntityID renders an integer entity id (a rule or sync-log id) as the string the
// event stores in EntityID.
func EntityID(id int64) string { return strconv.FormatInt(id, 10) }
