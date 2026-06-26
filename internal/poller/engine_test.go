package poller

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
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

// multiCredSource is a MultiCredentialed source for engine tests: it holds a set
// of credential ids and supports listing and removing them individually.
type multiCredSource struct {
	ids []string
}

func (m *multiCredSource) Descriptor() source.Descriptor {
	return source.Descriptor{Type: "multi", Archetype: source.ArchetypePull, Title: "Multi"}
}
func (m *multiCredSource) Fetch(context.Context, time.Time, string) (*source.ImportBatch, error) {
	return &source.ImportBatch{Source: "multi"}, nil
}
func (m *multiCredSource) CredentialConfigured(context.Context) (bool, error) {
	return len(m.ids) > 0, nil
}
func (m *multiCredSource) SetCredential(_ context.Context, input string) error {
	m.ids = append(m.ids, input)
	return nil
}
func (m *multiCredSource) ListCredentials(context.Context) ([]source.CredentialEntry, error) {
	out := make([]source.CredentialEntry, len(m.ids))
	for i, id := range m.ids {
		out[i] = source.CredentialEntry{ID: id, Label: "••••" + id, Removable: true}
	}
	return out, nil
}
func (m *multiCredSource) RemoveCredential(_ context.Context, id string) error {
	var kept []string
	found := false
	for _, x := range m.ids {
		if x == id {
			found = true
			continue
		}
		kept = append(kept, x)
	}
	if !found {
		return fmt.Errorf("no credential %q", id)
	}
	m.ids = kept
	return nil
}

func TestEngineMultiCredential(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, &multiCredSource{ids: []string{"a", "b"}}))

	statuses, err := e.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.True(t, statuses[0].MultiCredential, "source advertises multi-credential")
	require.Len(t, statuses[0].CredentialEntries, 2, "both entries are listed (masked)")
	assert.Equal(t, "a", statuses[0].CredentialEntries[0].ID)

	require.NoError(t, e.RemoveSourceCredential(ctx, "multi", "a"))
	statuses, _ = e.Sources(ctx)
	require.Len(t, statuses[0].CredentialEntries, 1)
	assert.Equal(t, "b", statuses[0].CredentialEntries[0].ID)

	require.Error(t, e.RemoveSourceCredential(ctx, "multi", "nope"), "unknown id")
	require.Error(t, e.RemoveSourceCredential(ctx, "other", "a"), "unknown source")
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

// warmerSource is a Warmer (read-through cache) with no Puller — the shape of the
// market source. It counts warms so a test can assert when it is and isn't synced.
type warmerSource struct {
	typ    string
	warmed int
}

func (s *warmerSource) Descriptor() source.Descriptor {
	return source.Descriptor{Type: s.typ, Archetype: source.ArchetypeReference, Title: s.typ}
}
func (s *warmerSource) Warm(context.Context) error { s.warmed++; return nil }

// TestEngineSyncSkipsOnDemandCache verifies that a read-through cache source (a
// Warmer with no schedule) is skipped by Sync ("sync all") — so a bulk sync never
// eagerly warms market data nothing is displaying — yet is still warmable by an
// explicit per-source SyncSource ("Sync now").
func TestEngineSyncSkipsOnDemandCache(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	cache := &warmerSource{typ: "market"}
	e := NewEngine(
		enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}),
		New(Options{Store: store, Source: cache, Interval: 0}), // on-demand: no schedule
	)

	res, err := e.Sync(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewTransactions, "the pull source still syncs")
	assert.Equal(t, 0, cache.warmed, "sync-all does not warm an on-demand cache")

	_, err = e.SyncSource(context.Background(), "market")
	require.NoError(t, err)
	assert.Equal(t, 1, cache.warmed, "an explicit per-source sync warms it")
}

// TestEngineAddRemovePoller verifies that a source can be registered and
// deregistered at runtime — the mechanism a plugin source (ADR 0005) rides on
// enable/disable — and that it joins/leaves the listing and "sync all".
func TestEngineAddRemovePoller(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}))

	// A dynamically-added source appears in the listing and can be synced by type.
	require.NoError(t, e.AddPoller(ctx, enginePoller(store, typedSource{typ: "plugin:demo", batch: miniBatch("plugin:demo", "plugin:demo:acct", "plugin:demo:1")})))
	srcs, err := e.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, srcs, 2)
	assert.Equal(t, "plugin:demo", srcs[1].Type, "added source keeps registration order (appended)")

	res, err := e.SyncSource(ctx, "plugin:demo")
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewTransactions)

	// "Sync all" includes the added pull source.
	agg, err := e.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, agg.Accounts, "sync-all covers both the built-in and the added source")

	// After removal it is gone from the listing and unknown to per-source sync.
	require.NoError(t, e.RemovePoller(ctx, "plugin:demo"))
	srcs, err = e.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, srcs, 1)
	assert.Equal(t, "a", srcs[0].Type)

	_, err = e.SyncSource(ctx, "plugin:demo")
	require.Error(t, err, "a removed source is unknown")
	require.Error(t, e.RemovePoller(ctx, "plugin:demo"), "removing an unknown source errors")
}

// TestEngineAddPollerReplaces verifies that adding a source whose type already
// exists replaces it in place (a plugin source reload) without duplicating it.
func TestEngineAddPollerReplaces(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine()

	require.NoError(t, e.AddPoller(ctx, enginePoller(store, typedSource{typ: "plugin:demo", batch: miniBatch("plugin:demo", "v1-acct", "v1-1")})))
	require.NoError(t, e.AddPoller(ctx, enginePoller(store, typedSource{typ: "plugin:demo", batch: miniBatch("plugin:demo", "v2-acct", "v2-1")})))

	srcs, err := e.Sources(ctx)
	require.NoError(t, err)
	require.Len(t, srcs, 1, "re-adding the same type replaces rather than duplicates")
}

// TestEngineConcurrentAddRemoveAndSync exercises the engine mutex: concurrent
// add/remove/list/sync must not race (run with -race).
func TestEngineConcurrentAddRemoveAndSync(t *testing.T) {
	ctx := context.Background()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, typedSource{typ: "base", batch: miniBatch("base", "base-acct", "base-1")}))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		typ := fmt.Sprintf("plugin:%d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.AddPoller(ctx, enginePoller(store, typedSource{typ: typ, batch: miniBatch(typ, typ+":acct", typ+":1")}))
			_, _ = e.Sources(ctx)
			_ = e.RemovePoller(ctx, typ)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 8; i++ {
			_, _ = e.Sync(ctx)
		}
	}()
	wg.Wait()
}

func TestEngineIngestRoutesToReceiver(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, &fakeReceiver{batch: miniBatch("webhook", "wh-acct", "wh-1")}))

	res, err := e.Ingest(context.Background(), "webhook", source.Delivery{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewTransactions)
}

func TestEngineIngestUnknownSource(t *testing.T) {
	e := NewEngine()
	_, err := e.Ingest(context.Background(), "nope", source.Delivery{})
	assert.Error(t, err)
}

func TestEngineIngestNonReceiverSource(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}))

	_, err := e.Ingest(context.Background(), "a", source.Delivery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not accept inbound deliveries")
}

func TestEngineRevealAndRotateSecret(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, &fakeReceiver{}))
	ctx := context.Background()

	got, err := e.RevealSourceSecret(ctx, "webhook")
	require.NoError(t, err)
	assert.Empty(t, got, "no secret before rotate")

	minted, err := e.RotateSourceSecret(ctx, "webhook")
	require.NoError(t, err)
	assert.NotEmpty(t, minted)

	got, err = e.RevealSourceSecret(ctx, "webhook")
	require.NoError(t, err)
	assert.Equal(t, minted, got)
}

func TestEngineSecretOnNonWebhookSource(t *testing.T) {
	store := db.NewSQLiteStore(testutil.NewDB(t))
	e := NewEngine(enginePoller(store, typedSource{typ: "a", batch: miniBatch("a", "a-acct", "a-1")}))

	_, err := e.RevealSourceSecret(context.Background(), "a")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no managed signing secret")
}
