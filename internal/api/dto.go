package api

import (
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/relationships"
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

// TransactionDTO is the JSON representation of a transaction. Labels are strict
// key->value strings; Extensions are arbitrary, app-owned namespaced metadata
// whose values are any JSON (decoded to `any` so the type is MCP-output-safe).
type TransactionDTO struct {
	ID          string            `json:"id"`
	AccountID   string            `json:"account_id"`
	Amount      string            `json:"amount"`
	Pending     bool              `json:"pending"`
	Date        time.Time         `json:"date"`
	Description string            `json:"description"`
	Payee       string            `json:"payee"`
	Memo        string            `json:"memo"`
	SyncedAt    time.Time         `json:"synced_at"`
	Labels      map[string]string `json:"labels"`
	Extensions  map[string]any    `json:"extensions"`
	// Relationships are this transaction's own OUTBOUND edges ({kind,target}). The
	// inbound direction is derived only on demand via GET
	// /transactions/{id}/relationships, not inlined on every row. Carrying the
	// outbound edges here lets the dashboard show an indicator and build the inbound
	// index client-side for search.
	Relationships []relationships.Relationship `json:"relationships"`
}

// LabelDTO is the JSON representation of one label in the global vocabulary: a
// key/value pair and the number of transactions that carry it.
type LabelDTO struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	TransactionCount int    `json:"transaction_count"`
}

// ExtensionDTO is the JSON representation of one schema-extension key in the
// global vocabulary: its namespace (the part before the first dot), the full key,
// and the number of transactions that carry it.
type ExtensionDTO struct {
	Namespace        string `json:"namespace"`
	Key              string `json:"key"`
	TransactionCount int    `json:"transaction_count"`
}

// RelationshipDTO is one edge in a transaction's neighborhood: the kind of
// relationship, the direction from the focal transaction's perspective ("outbound"
// = this transaction asserts the edge; "inbound" = another transaction's edge
// targets this one), and the other transaction's id.
type RelationshipDTO struct {
	Kind               string `json:"kind"`
	Direction          string `json:"direction"`
	OtherTransactionID string `json:"other_transaction_id"`
}

// RelationshipKindDTO is one relationship kind in the global vocabulary with the
// number of outbound edges that use it.
type RelationshipKindDTO struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
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
		ID:            t.ID,
		AccountID:     t.AccountID,
		Amount:        t.Amount,
		Pending:       t.Pending != 0,
		Date:          unixTime(t.Date),
		Description:   t.Description,
		Payee:         t.Payee,
		Memo:          t.Memo,
		SyncedAt:      unixTime(t.SyncedAt),
		Labels:        decodeLabels(t.Labels),
		Extensions:    decodeExtensions(t.Extensions),
		Relationships: decodeRelationships(t.Relationships),
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
