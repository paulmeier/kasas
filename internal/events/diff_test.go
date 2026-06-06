package events

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDiffSnapshotsBirth(t *testing.T) {
	// The first version diffs against the zero value: every set field is a change
	// from empty, and all labels are additions.
	next := TransactionPayload{
		ID:          "tx-1",
		AccountID:   "acct-1",
		Amount:      "-4.50",
		Pending:     true,
		Date:        time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
		Description: "STARBUCKS #123",
		Payee:       "Starbucks",
		Labels:      map[string]string{"category": "food"},
	}
	d := DiffSnapshots(TransactionPayload{}, next)

	fields := fieldMap(d)
	assert.Equal(t, [2]string{"", "acct-1"}, fields["account_id"])
	assert.Equal(t, [2]string{"", "-4.50"}, fields["amount"])
	assert.Equal(t, [2]string{"false", "true"}, fields["pending"])
	assert.Equal(t, [2]string{"", "2024-03-01T00:00:00Z"}, fields["date"])
	assert.Equal(t, [2]string{"", "Starbucks"}, fields["payee"])
	assert.Equal(t, map[string]string{"category": "food"}, d.LabelsAdded)
	assert.Empty(t, d.LabelsRemoved)
	assert.Empty(t, d.LabelsChanged)
}

func TestDiffSnapshotsScalarChange(t *testing.T) {
	prev := TransactionPayload{ID: "tx-1", AccountID: "acct-1", Amount: "-4.50", Pending: true, Payee: "PENDING"}
	next := TransactionPayload{ID: "tx-1", AccountID: "acct-2", Amount: "-5.00", Pending: false, Payee: "Starbucks"}
	d := DiffSnapshots(prev, next)

	fields := fieldMap(d)
	assert.Equal(t, [2]string{"acct-1", "acct-2"}, fields["account_id"], "account_id changes are tracked")
	assert.Equal(t, [2]string{"-4.50", "-5.00"}, fields["amount"])
	assert.Equal(t, [2]string{"true", "false"}, fields["pending"], "pending flip posts")
	assert.Equal(t, [2]string{"PENDING", "Starbucks"}, fields["payee"])
	assert.NotContains(t, fields, "memo", "unchanged fields are omitted")
}

func TestDiffSnapshotsLabels(t *testing.T) {
	prev := TransactionPayload{Labels: map[string]string{"category": "food", "person": "dad"}}
	next := TransactionPayload{Labels: map[string]string{"category": "coffee", "trip": "2024"}}
	d := DiffSnapshots(prev, next)

	assert.Empty(t, d.Fields, "no scalar changes")
	assert.Equal(t, map[string]string{"trip": "2024"}, d.LabelsAdded)
	assert.Equal(t, map[string]string{"person": "dad"}, d.LabelsRemoved)
	assert.Equal(t, map[string][2]string{"category": {"food", "coffee"}}, d.LabelsChanged)
}

func TestDiffSnapshotsNoChange(t *testing.T) {
	p := TransactionPayload{ID: "tx-1", Amount: "-1.00", Labels: map[string]string{"a": "b"}}
	d := DiffSnapshots(p, p)
	assert.Empty(t, d.Fields)
	assert.Empty(t, d.LabelsAdded)
	assert.Empty(t, d.LabelsRemoved)
	assert.Empty(t, d.LabelsChanged)
}

// fieldMap indexes a diff's scalar field changes by field name as {from, to}.
func fieldMap(d VersionDiff) map[string][2]string {
	out := make(map[string][2]string, len(d.Fields))
	for _, f := range d.Fields {
		out[f.Field] = [2]string{f.From, f.To}
	}
	return out
}
