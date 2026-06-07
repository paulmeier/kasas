package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/provenance"
)

// TestTransactionProvenanceUntouched covers a seeded transaction nobody has changed:
// the origin fields come straight off the row and the account's organization, there
// are no transformations yet, and imported_at falls back to last_seen.
func TestTransactionProvenanceUntouched(t *testing.T) {
	srv, fx := newEventsServer(t)
	id := fx.TxIDsByDateDesc[1]

	var p provenance.Provenance
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/provenance", &p))

	assert.Equal(t, id, p.TransactionID)
	assert.Equal(t, "simplefin", p.Source)
	assert.Equal(t, id, p.SourceTransactionID)
	assert.Equal(t, fx.CheckingID, p.AccountID)
	assert.Equal(t, "Acme Bank", p.Institution)
	assert.Empty(t, p.Transformations)
	assert.False(t, p.ImportedAt.IsZero())
	assert.True(t, p.ImportedAt.Equal(p.LastSeen), "imported_at falls back to last_seen when there is no history")
}

// TestTransactionProvenanceAfterLabelEdit covers the lineage path: a label edit
// synthesizes a v1 "imported" baseline and records the change, so provenance reports
// two transformations with their summaries and an imported_at at the baseline.
func TestTransactionProvenanceAfterLabelEdit(t *testing.T) {
	srv, fx := newEventsServer(t)
	id := fx.TxIDsByDateDesc[0]

	applyLabel(t, srv, id, "category", "food")

	var p provenance.Provenance
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/provenance", &p))

	assert.Equal(t, "simplefin", p.Source)
	require.Len(t, p.Transformations, 2)
	assert.Equal(t, "imported", p.Transformations[0].Kind)
	assert.Equal(t, "imported from simplefin", p.Transformations[0].Summary)
	assert.Equal(t, "labeled", p.Transformations[1].Kind)
	assert.Equal(t, "+category:food", p.Transformations[1].Summary)
	assert.True(t, p.ImportedAt.Equal(p.Transformations[0].OccurredAt), "imported_at is the earliest version's timestamp")
}

func TestTransactionProvenanceUnknownIs404(t *testing.T) {
	srv, _ := newEventsServer(t)
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/nope/provenance", nil))
}

// TestMCPGetTransactionProvenance confirms the MCP tool returns the same view as the
// REST endpoint and errors on an unknown id.
func TestMCPGetTransactionProvenance(t *testing.T) {
	session, fx, _ := connectMCP(t)
	id := fx.TxIDsByDateDesc[0]

	var p provenance.Provenance
	callTool(t, session, "get_transaction_provenance", map[string]any{"transaction_id": id}, &p)
	assert.Equal(t, id, p.TransactionID)
	assert.Equal(t, "simplefin", p.Source)
	assert.Equal(t, id, p.SourceTransactionID)
	assert.Equal(t, "Acme Bank", p.Institution)

	res := callTool(t, session, "get_transaction_provenance", map[string]any{"transaction_id": "nope"}, nil)
	assert.True(t, res.IsError, "unknown transaction should be an error")
}
