package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
)

// relResp is the GET/POST/DELETE relationships response shape.
type relResp struct {
	ID            string                `json:"id"`
	Relationships []api.RelationshipDTO `json:"relationships"`
}

func TestTransactionRelationships(t *testing.T) {
	srv, _, _ := newTestServer(t)

	t.Run("create asserts an outbound edge and echoes the neighborhood", func(t *testing.T) {
		var out relResp
		code := postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
			map[string]any{"kind": "refund_of", "target": "tx-2"}, &out)
		require.Equal(t, http.StatusCreated, code)
		require.Len(t, out.Relationships, 1)
		assert.Equal(t, api.RelationshipDTO{Kind: "refund_of", Direction: "outbound", OtherTransactionID: "tx-2"}, out.Relationships[0])
	})

	t.Run("outbound on the subject, inbound on the target", func(t *testing.T) {
		var sub relResp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-1/relationships", &sub))
		require.Len(t, sub.Relationships, 1)
		assert.Equal(t, "outbound", sub.Relationships[0].Direction)

		var tgt relResp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-2/relationships", &tgt))
		require.Len(t, tgt.Relationships, 1)
		assert.Equal(t, api.RelationshipDTO{Kind: "refund_of", Direction: "inbound", OtherTransactionID: "tx-1"}, tgt.Relationships[0])
	})

	t.Run("outbound edges surface on the transaction DTO", func(t *testing.T) {
		var txn api.TransactionDTO
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-1", &txn))
		require.Len(t, txn.Relationships, 1)
		assert.Equal(t, "refund_of", txn.Relationships[0].Kind)
		assert.Equal(t, "tx-2", txn.Relationships[0].Target)
	})

	t.Run("create is idempotent and normalizes the kind", func(t *testing.T) {
		var out relResp
		// Same edge, messy kind ("Refund Of" normalizes to refund_of) -> still one edge.
		require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
			map[string]any{"kind": "Refund Of", "target": "tx-2"}, &out))
		outbound := 0
		for _, e := range out.Relationships {
			if e.Direction == "outbound" {
				outbound++
				assert.Equal(t, "refund_of", e.Kind)
			}
		}
		assert.Equal(t, 1, outbound)
	})

	t.Run("self-edge is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
			map[string]any{"kind": "refund_of", "target": "tx-1"}, nil))
	})

	t.Run("missing target is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
			map[string]any{"kind": "refund_of", "target": "does-not-exist"}, nil))
	})

	t.Run("missing kind is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
			map[string]any{"kind": "   ", "target": "tx-3"}, nil))
	})

	t.Run("unknown subject is 404", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, postJSON(t, srv, "/api/v1/transactions/nope/relationships",
			map[string]any{"kind": "refund_of", "target": "tx-2"}, nil))
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/transactions/nope/relationships", nil))
	})

	t.Run("delete removes the edge from both views", func(t *testing.T) {
		var out relResp
		require.Equal(t, http.StatusOK, deleteJSON(t, srv,
			"/api/v1/transactions/tx-1/relationships?kind=refund_of&target=tx-2", &out))
		assert.Empty(t, out.Relationships)

		var tgt relResp
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/tx-2/relationships", &tgt))
		assert.Empty(t, tgt.Relationships)
	})

	t.Run("delete requires kind and target", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, deleteJSON(t, srv, "/api/v1/transactions/tx-1/relationships", nil))
	})
}

func TestListRelationshipKinds(t *testing.T) {
	srv, _, _ := newTestServer(t)

	var out struct {
		Relationships []api.RelationshipKindDTO `json:"relationships"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/relationships", &out))
	assert.NotNil(t, out.Relationships)
	assert.Empty(t, out.Relationships)

	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
		map[string]any{"kind": "refund_of", "target": "tx-2"}, nil))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/tx-3/relationships",
		map[string]any{"kind": "transfer_to", "target": "tx-2"}, nil))
	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/tx-1/relationships",
		map[string]any{"kind": "transfer_to", "target": "tx-3"}, nil))

	out.Relationships = nil
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/relationships", &out))
	// Sorted by kind: refund_of (1 edge), transfer_to (2 edges).
	require.Len(t, out.Relationships, 2)
	assert.Equal(t, api.RelationshipKindDTO{Kind: "refund_of", Count: 1}, out.Relationships[0])
	assert.Equal(t, api.RelationshipKindDTO{Kind: "transfer_to", Count: 2}, out.Relationships[1])
}

func TestRelationshipEventsAndNoHistory(t *testing.T) {
	srv, fx := newEventsServer(t)
	from, to := fx.TxIDsByDateDesc[0], fx.TxIDsByDateDesc[1]

	require.Equal(t, http.StatusCreated, postJSON(t, srv, "/api/v1/transactions/"+from+"/relationships",
		map[string]any{"kind": "transfer_to", "target": to}, nil))

	// A relationship.created event on the subject transaction, carrying the edge.
	var created eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=relationship.created", &created))
	require.Len(t, created.Events, 1)
	e := created.Events[0]
	assert.Equal(t, "relationship.created", e.Type)
	assert.Equal(t, "transaction", e.EntityType)
	assert.Equal(t, from, e.EntityID)
	data, ok := e.Data.(map[string]any)
	require.True(t, ok, "event data is an object")
	assert.Equal(t, "transfer_to", data["kind"])
	assert.Equal(t, to, data["target"])

	// Relationships do NOT record transaction history: a transaction whose only
	// change is a new edge has no versions.
	var h api.HistoryDTO
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/transactions/"+from+"/history", &h))
	assert.Empty(t, h.Versions, "an edge is not a field of the transaction's own state")

	// Deleting emits relationship.removed.
	require.Equal(t, http.StatusOK, deleteJSON(t, srv,
		"/api/v1/transactions/"+from+"/relationships?kind=transfer_to&target="+to, nil))
	var removed eventsResponse
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/events?type=relationship.removed", &removed))
	require.Len(t, removed.Events, 1)
	assert.Equal(t, from, removed.Events[0].EntityID)
}

func TestMCPRelationships(t *testing.T) {
	session, fx, _ := connectMCP(t)
	from, to := fx.TxIDsByDateDesc[0], fx.TxIDsByDateDesc[1]

	type relOut struct {
		TransactionID string                `json:"transaction_id"`
		Relationships []api.RelationshipDTO `json:"relationships"`
	}

	var created relOut
	res := callTool(t, session, "create_transaction_relationship", map[string]any{
		"transaction_id": from, "target": to, "kind": "refund_of",
	}, &created)
	require.False(t, res.IsError)
	require.Len(t, created.Relationships, 1)
	assert.Equal(t, api.RelationshipDTO{Kind: "refund_of", Direction: "outbound", OtherTransactionID: to}, created.Relationships[0])

	// The target sees it inbound.
	var got relOut
	callTool(t, session, "get_transaction_relationships", map[string]any{"transaction_id": to}, &got)
	require.Len(t, got.Relationships, 1)
	assert.Equal(t, "inbound", got.Relationships[0].Direction)
	assert.Equal(t, from, got.Relationships[0].OtherTransactionID)

	var vocab struct {
		Relationships []api.RelationshipKindDTO `json:"relationships"`
	}
	callTool(t, session, "list_relationship_kinds", map[string]any{}, &vocab)
	require.Len(t, vocab.Relationships, 1)
	assert.Equal(t, api.RelationshipKindDTO{Kind: "refund_of", Count: 1}, vocab.Relationships[0])

	// Delete clears it.
	var afterDelete relOut
	callTool(t, session, "delete_transaction_relationship", map[string]any{
		"transaction_id": from, "target": to, "kind": "refund_of",
	}, &afterDelete)
	assert.Empty(t, afterDelete.Relationships)

	// A self-edge surfaces as a tool error.
	res = callTool(t, session, "create_transaction_relationship", map[string]any{
		"transaction_id": from, "target": from, "kind": "refund_of",
	}, nil)
	assert.True(t, res.IsError)
}
