package db_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func insertEvent(t *testing.T, store db.Store, typ, entityType, entityID string, occurredAt int64) db.Event {
	t.Helper()
	ev, err := store.InsertEvent(context.Background(), db.InsertEventParams{
		EventID:    typ + ":" + entityID,
		EventType:  typ,
		EntityType: entityType,
		EntityID:   entityID,
		OccurredAt: occurredAt,
		Data:       `{"k":"v"}`,
	})
	require.NoError(t, err)
	return ev
}

func TestInsertEventAssignsSequence(t *testing.T) {
	store := testutil.NewStore(t)
	a := insertEvent(t, store, "transaction.created", "transaction", "tx-1", 1000)
	b := insertEvent(t, store, "label.applied", "transaction", "tx-1", 1001)
	assert.Equal(t, int64(1), a.ID)
	assert.Equal(t, a.ID+1, b.ID, "sequence is monotonic")
	assert.Equal(t, "transaction.created", a.EventType)
}

func TestListEventsAfterCursorAndLimit(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	a := insertEvent(t, store, "transaction.created", "transaction", "tx-1", 1000)
	b := insertEvent(t, store, "label.applied", "transaction", "tx-1", 1001)
	c := insertEvent(t, store, "account.created", "account", "acct-1", 1002)

	rows, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{After: a.ID, RowLimit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, b.ID, rows[0].ID)
	assert.Equal(t, c.ID, rows[1].ID)

	rows, err = store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 1})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, a.ID, rows[0].ID, "ordered ascending by sequence")
}

func TestListEventsAfterFilters(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	insertEvent(t, store, "transaction.created", "transaction", "tx-1", 1000)
	insertEvent(t, store, "label.applied", "transaction", "tx-1", 1001)
	insertEvent(t, store, "account.created", "account", "acct-1", 1002)

	byType, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: "account.created", RowLimit: 10})
	require.NoError(t, err)
	require.Len(t, byType, 1)
	assert.Equal(t, "acct-1", byType[0].EntityID)

	byEntityType, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{EntityType: "transaction", RowLimit: 10})
	require.NoError(t, err)
	assert.Len(t, byEntityType, 2)

	byEntityID, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{EntityID: "tx-1", RowLimit: 10})
	require.NoError(t, err)
	assert.Len(t, byEntityID, 2)

	all, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 10})
	require.NoError(t, err)
	assert.Len(t, all, 3, "empty filters match everything")
}

func TestGetEventBySequence(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	a := insertEvent(t, store, "transaction.created", "transaction", "tx-1", 1000)

	got, err := store.GetEventBySequence(ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, "transaction.created", got.EventType)

	_, err = store.GetEventBySequence(ctx, 9999)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestListRecentEventsNewestFirst(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	insertEvent(t, store, "transaction.created", "transaction", "tx-1", 1000)
	insertEvent(t, store, "account.created", "account", "acct-1", 1001)

	recent, err := store.ListRecentEvents(ctx, 1)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	assert.Equal(t, "account.created", recent[0].EventType, "most recent first")
}

func TestDeleteEventsBefore(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()
	old := insertEvent(t, store, "x", "y", "z", 1000)
	insertEvent(t, store, "x", "y", "z2", 5000)

	n, err := store.DeleteEventsBefore(ctx, 2000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	_, err = store.GetEventBySequence(ctx, old.ID)
	assert.ErrorIs(t, err, sql.ErrNoRows, "the old event was pruned")

	remaining, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 10})
	require.NoError(t, err)
	assert.Len(t, remaining, 1)
}
