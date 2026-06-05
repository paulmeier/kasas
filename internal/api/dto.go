package api

import (
	"time"

	"github.com/paulmeier/kasas/internal/db"
)

// OrganizationDTO is the JSON representation of an organization.
type OrganizationDTO struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Name    string `json:"name"`
	SfinURL string `json:"sfin_url"`
}

// AccountDTO is the JSON representation of an account.
type AccountDTO struct {
	ID          string    `json:"id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
	BalanceDate time.Time `json:"balance_date"`
	SyncedAt    time.Time `json:"synced_at"`
}

// TransactionDTO is the JSON representation of a transaction.
type TransactionDTO struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Amount      string    `json:"amount"`
	Pending     bool      `json:"pending"`
	Date        time.Time `json:"date"`
	Description string    `json:"description"`
	Payee       string    `json:"payee"`
	Memo        string    `json:"memo"`
	SyncedAt    time.Time `json:"synced_at"`
	Tags        []string  `json:"tags"`
}

// TagDTO is the JSON representation of a tag in the global vocabulary: its
// display name and the number of transactions that carry it.
type TagDTO struct {
	Name             string `json:"name"`
	TransactionCount int    `json:"transaction_count"`
}

// SyncDTO is the JSON representation of a sync_log entry.
type SyncDTO struct {
	ID          int64      `json:"id"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
}

func unixTime(sec int64) time.Time {
	return time.Unix(sec, 0).UTC()
}

func toOrganizationDTO(o db.Organization) OrganizationDTO {
	return OrganizationDTO{ID: o.ID, Domain: o.Domain, Name: o.Name, SfinURL: o.SfinUrl}
}

func toAccountDTO(a db.Account) AccountDTO {
	return AccountDTO{
		ID:          a.ID,
		OrgID:       a.OrgID,
		Name:        a.Name,
		Currency:    a.Currency,
		Balance:     a.Balance,
		BalanceDate: unixTime(a.BalanceDate),
		SyncedAt:    unixTime(a.SyncedAt),
	}
}

func toTransactionDTO(t db.Transaction) TransactionDTO {
	return TransactionDTO{
		ID:          t.ID,
		AccountID:   t.AccountID,
		Amount:      t.Amount,
		Pending:     t.Pending != 0,
		Date:        unixTime(t.Date),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		SyncedAt:    unixTime(t.SyncedAt),
		Tags:        decodeTags(t.Tags),
	}
}

func toSyncDTO(s db.SyncLog) SyncDTO {
	dto := SyncDTO{
		ID:        s.ID,
		StartedAt: unixTime(s.StartedAt),
		Status:    s.Status,
	}
	if s.CompletedAt.Valid {
		t := unixTime(s.CompletedAt.Int64)
		dto.CompletedAt = &t
	}
	if s.Error.Valid {
		dto.Error = s.Error.String
	}
	return dto
}

func toAccountDTOs(in []db.Account) []AccountDTO {
	out := make([]AccountDTO, len(in))
	for i, a := range in {
		out[i] = toAccountDTO(a)
	}
	return out
}

func toTransactionDTOs(in []db.Transaction) []TransactionDTO {
	out := make([]TransactionDTO, len(in))
	for i, t := range in {
		out[i] = toTransactionDTO(t)
	}
	return out
}
