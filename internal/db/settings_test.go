package db_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
)

func TestSettingsUpsertListDelete(t *testing.T) {
	store := testutil.NewStore(t)
	ctx := context.Background()

	require.NoError(t, store.UpsertSetting(ctx, db.UpsertSettingParams{
		Key: "plugins.enabled", Value: "true", UpdatedAt: 1000,
	}))
	require.NoError(t, store.UpsertSetting(ctx, db.UpsertSettingParams{
		Key: "sync.interval", Value: "1h", UpdatedAt: 1001,
	}))

	rows, err := store.ListSettings(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "plugins.enabled", rows[0].Key, "ordered by key")
	assert.Equal(t, "true", rows[0].Value)

	// Upserting an existing key replaces its value in place.
	require.NoError(t, store.UpsertSetting(ctx, db.UpsertSettingParams{
		Key: "plugins.enabled", Value: "false", UpdatedAt: 1002,
	}))
	rows, err = store.ListSettings(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "false", rows[0].Value)
	assert.Equal(t, int64(1002), rows[0].UpdatedAt)

	n, err := store.DeleteSetting(ctx, "plugins.enabled")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = store.DeleteSetting(ctx, "plugins.enabled")
	require.NoError(t, err)
	assert.Zero(t, n, "deleting an absent key affects no rows")

	rows, err = store.ListSettings(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "sync.interval", rows[0].Key)
}
