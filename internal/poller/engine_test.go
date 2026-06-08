package poller

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/testutil"
)

// typedSource is a Puller with a configurable descriptor type, so engine tests can
// register more than one distinct source.
type typedSource struct {
	typ   string
	batch *source.ImportBatch
}

func (s typedSource) Descriptor() source.Descriptor {
	return source.Descriptor{Type: s.typ, Archetype: source.ArchetypePull, Title: s.typ}
}

func (s typedSource) Fetch(context.Context, time.Time, string) (*source.ImportBatch, error) {
	return s.batch, nil
}

// miniBatch is a one-account, one-transaction batch with ids scoped to a source so
// two sources never collide.
func miniBatch(src, acct, txn string) *source.ImportBatch {
	return &source.ImportBatch{
		Source: src,
		Accounts: []source.ImportAccount{{
			ExternalID:   acct,
			Org:          source.ImportOrg{ID: src + "-org", Name: src},
			Name:         acct,
			Currency:     "USD",
			Transactions: []source.ImportTxn{{ExternalID: txn, Date: 1700000000, Amount: "-1.00", Description: "x"}},
		}},
	}
}

func enginePoller(store db.Store, src source.Source) *Poller {
	return New(Options{Store: store, Source: src, Interval: time.Hour})
}

func TestEngineSyncAggregatesAllSources(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(
		enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}),
		enginePoller(store, typedSource{typ: "b", batch: miniBatch("b", "b-acct", "b-1")}),
	)

	res, err := e.Sync(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, res.Accounts, "one account per source")
	assert.Equal(t, 2, res.NewTransactions, "one transaction per source")
}

func TestEngineSyncSourceByType(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}))

	res, err := e.SyncSource(context.Background(), "a")
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewTransactions)

	_, err = e.SyncSource(context.Background(), "nope")
	require.Error(t, err)
}

func TestEngineSourcesAndCredentials(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(
		enginePoller(store, &credSource{}),                     // credentialed, not connected
		enginePoller(store, &fakeSource{batch: sampleBatch()}), // no runtime credential
	)

	statuses, err := e.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	byType := map[string]SourceStatus{}
	for _, s := range statuses {
		byType[s.Type] = s
	}
	assert.False(t, byType["cred"].Connected, "credentialed but unconfigured → not connected")
	assert.True(t, byType["fake"].Connected, "no runtime credential → always ready")

	require.NoError(t, e.SetCredential(ctx, "cred", "tok"))
	ok, err := e.CredentialConfigured(ctx, "cred")
	require.NoError(t, err)
	assert.True(t, ok)

	_, err = e.CredentialConfigured(ctx, "nope")
	require.Error(t, err, "unknown source")
	require.Error(t, e.SetCredential(ctx, "fake", "x"), "a source without runtime credentials rejects SetCredential")
}

// TestEngineSkipsUnconfiguredSource verifies an unconfigured credentialed source is
// a clean no-op (no error, no sync-log entry) — so a SimpleFIN-less, CSV-only setup
// stays quiet.
func TestEngineSkipsUnconfiguredSource(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, &credSource{})) // not connected

	res, err := e.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res.Accounts)

	_, err = store.LatestSyncLog(ctx)
	require.ErrorIs(t, err, sql.ErrNoRows, "a skipped sync writes no sync-log entry")
}
