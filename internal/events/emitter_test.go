package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func TestEmitterRecordCommitsAndPublishes(t *testing.T) {
	store := testutil.NewStore(t)
	bus := NewBus()
	defer bus.Close()
	sub, cancel := bus.Subscribe()
	defer cancel()
	em := NewEmitter(bus)
	ctx := context.Background()

	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		return rec.Emit(ctx, q, TypeAccountCreated, EntityAccount, "acct-1", map[string]string{"name": "Checking"})
	})
	require.NoError(t, err)

	// Persisted with a generated sequence and id.
	rows, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 10})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, TypeAccountCreated, rows[0].EventType)
	assert.NotZero(t, rows[0].ID)
	assert.NotEmpty(t, rows[0].EventID)

	// Published live with the same sequence and payload.
	ev := recv(t, sub)
	assert.Equal(t, TypeAccountCreated, ev.Type)
	assert.Equal(t, rows[0].ID, ev.Sequence)
	assert.JSONEq(t, `{"name":"Checking"}`, string(ev.Data))
}

func TestEmitterRollbackPublishesNothing(t *testing.T) {
	store := testutil.NewStore(t)
	bus := NewBus()
	defer bus.Close()
	sub, cancel := bus.Subscribe()
	defer cancel()
	em := NewEmitter(bus)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		if e := rec.Emit(ctx, q, TypeAccountCreated, EntityAccount, "acct-1", map[string]string{}); e != nil {
			return e
		}
		return sentinel // force a rollback after emitting
	})
	require.ErrorIs(t, err, sentinel)

	rows, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 10})
	require.NoError(t, err)
	assert.Empty(t, rows, "a rolled-back event must not persist")

	select {
	case ev := <-sub:
		t.Fatalf("a rolled-back event must not be published, got %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNilEmitterIsNoOp(t *testing.T) {
	store := testutil.NewStore(t)
	var em *Emitter // nil == events disabled
	ctx := context.Background()

	called := false
	err := em.Record(ctx, store, func(q db.Querier, rec *Recorder) error {
		called = true
		return rec.Emit(ctx, q, TypeAccountCreated, EntityAccount, "acct-1", map[string]string{})
	})
	require.NoError(t, err)
	assert.True(t, called, "the work function still runs under a nil emitter")
	assert.Nil(t, em.Bus())

	rows, err := store.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 10})
	require.NoError(t, err)
	assert.Empty(t, rows, "a nil emitter records no events")
}
