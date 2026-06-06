package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

func TestTransactionHistoryRecordsLabelEdits(t *testing.T) {
	srv, fx := newEventsServer(t)
	id := fx.TxIDsByDateDesc[0]

	// The first label edit synthesizes a v1 "imported" baseline from the prior
	// state, then records the change as v2 "labeled".
	applyLabel(t, srv, id, "category", "food")

	var h api.HistoryDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/history", &h))
	require.Equal(t, id, h.TransactionID)
	require.Len(t, h.Versions, 2)

	assert.Equal(t, 1, h.Versions[0].Version)
	assert.Equal(t, "imported", h.Versions[0].ChangeKind)
	assert.NotEmpty(t, h.Versions[0].Diff.Fields, "the first version is a birth diff")

	assert.Equal(t, 2, h.Versions[1].Version)
	assert.Equal(t, "labeled", h.Versions[1].ChangeKind)
	assert.Empty(t, h.Versions[1].Diff.Fields, "a label edit changes no scalar fields")
	assert.Equal(t, map[string]string{"category": "food"}, h.Versions[1].Diff.LabelsAdded)
	assert.Equal(t, map[string]string{"category": "food"}, h.Versions[1].Transaction.Labels)

	// A second edit replaces the whole set (drops category, adds person) -> v3.
	applyLabel(t, srv, id, "person", "dad")
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+id+"/history", &h))
	require.Len(t, h.Versions, 3)
	assert.Equal(t, "labeled", h.Versions[2].ChangeKind)
	assert.Equal(t, map[string]string{"person": "dad"}, h.Versions[2].Diff.LabelsAdded)
	assert.Equal(t, map[string]string{"category": "food"}, h.Versions[2].Diff.LabelsRemoved)
}

func TestTransactionHistoryUnknownIs404(t *testing.T) {
	srv, _ := newEventsServer(t)
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/nope/history", nil))
}

func TestTransactionHistoryEmptyForUntouched(t *testing.T) {
	srv, fx := newEventsServer(t)
	// A seeded transaction nobody has changed has no versions yet: 200 + empty list,
	// distinguishing "no history" from "no such transaction" (which 404s).
	var h api.HistoryDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+fx.TxIDsByDateDesc[1]+"/history", &h))
	assert.Equal(t, fx.TxIDsByDateDesc[1], h.TransactionID)
	assert.Empty(t, h.Versions)
}
