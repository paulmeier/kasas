package poller

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeSource is a source.Puller that returns a canned batch (or error). It lets
// the engine tests exercise persist behaviour without any provider or HTTP — the
// whole point of the source seam is that the engine is provider-agnostic.
type fakeSource struct {
	batch *source.ImportBatch
	err   error
}

func (f *fakeSource) Descriptor() source.Descriptor {
	return source.Descriptor{Type: "fake", Archetype: source.ArchetypePull, Title: "Fake"}
}

func (f *fakeSource) Fetch(context.Context, time.Time, string) (*source.ImportBatch, error) {
	return f.batch, f.err
}

// bareSource implements source.Source only (no Puller, no Credentialed).
type bareSource struct{}

func (bareSource) Descriptor() source.Descriptor {
	return source.Descriptor{Type: "bare", Archetype: source.ArchetypeManual}
}

// fakeReceiver is a push (webhook) source: it implements source.Receiver (returning
// a canned batch or error from Receive) and source.WebhookSecret, with no Puller —
// so it exercises the engine's Ingest path and the receiver-only no-op in Sync.
type fakeReceiver struct {
	batch  *source.ImportBatch
	err    error
	secret string
}

func (f *fakeReceiver) Descriptor() source.Descriptor {
	return source.Descriptor{Type: "webhook", Archetype: source.ArchetypeWebhook, Title: "Inbound webhook"}
}

func (f *fakeReceiver) Receive(context.Context, source.Delivery) (*source.ImportBatch, error) {
	return f.batch, f.err
}

func (f *fakeReceiver) RevealSecret(context.Context) (string, error) { return f.secret, nil }

func (f *fakeReceiver) RotateSecret(context.Context) (string, error) {
	f.secret = "whsec_rotated"
	return f.secret, nil
}

// sampleBatch mirrors the canonical two-transaction account used across tests.
// The org id is the domain (a real source derives a stable id before the engine
// sees it), and txn-2 is pending.
func sampleBatch() *source.ImportBatch {
	return &source.ImportBatch{
		Source: "simplefin",
		Accounts: []source.ImportAccount{{
			ExternalID:  "acct-1",
			Org:         source.ImportOrg{ID: "mybank.com", Domain: "mybank.com", Name: "My Bank", URL: "https://sfin.mybank.com"},
			Name:        "Checking",
			Currency:    "USD",
			Balance:     "1234.56",
			BalanceDate: 1700000000,
			Transactions: []source.ImportTxn{
				{ExternalID: "txn-1", Date: 1699990000, Amount: "-12.34", Description: "Coffee", Payee: "Cafe"},
				{ExternalID: "txn-2", Date: 1699995000, Amount: "-56.78", Description: "Books", Payee: "Store", Memo: "gift", Pending: true},
			},
		}},
	}
}

// refreshBatch describes txn-1 across two syncs: it starts pending with one
// amount, then posts with a corrected amount. Used to verify a re-sync refreshes
// bridge fields while preserving user labels.
func refreshBatch(amount, description string, pending bool) *source.ImportBatch {
	return &source.ImportBatch{
		Source: "simplefin",
		Accounts: []source.ImportAccount{{
			ExternalID:  "acct-1",
			Org:         source.ImportOrg{ID: "mybank.com", Domain: "mybank.com", Name: "My Bank", URL: "https://sfin.mybank.com"},
			Name:        "Checking",
			Currency:    "USD",
			Balance:     "100.00",
			BalanceDate: 1700000000,
			Transactions: []source.ImportTxn{
				{ExternalID: "txn-1", Date: 1699990000, Amount: amount, Description: description, Payee: "Cafe", Pending: pending},
			},
		}},
	}
}

func newPoller(t *testing.T, opts Options) (*Poller, db.Querier) {
	t.Helper()
	if opts.Store == nil {
		opts.Store = db.NewSQLiteStore(testutil.NewDB(t))
	}
	if opts.Interval == 0 {
		opts.Interval = time.Hour
	}
	return New(opts), opts.Store
}

func TestSyncPersistsAndIsIdempotent(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}})
	ctx := context.Background()

	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Accounts)
	assert.Equal(t, 2, res.NewTransactions)

	// Re-syncing identical data inserts nothing.
	res2, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.NewTransactions)

	orgs, err := queries.ListOrganizations(ctx)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	assert.Equal(t, "mybank.com", orgs[0].ID, "org id is the stable id the source supplied")

	accounts, err := queries.ListAccounts(ctx)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	assert.Equal(t, "1234.56", accounts[0].Balance)

	txns, err := queries.ListTransactions(ctx, db.ListTransactionsParams{RowLimit: 100})
	require.NoError(t, err)
	require.Len(t, txns, 2)

	pending, err := queries.GetTransaction(ctx, "txn-2")
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending.Pending)
	assert.Equal(t, "simplefin", pending.Source, "the batch's source is stamped as provenance")

	latest, err := queries.LatestSyncLog(ctx)
	require.NoError(t, err)
	assert.Equal(t, "success", latest.Status)
}

func TestSyncRefreshesExistingPreservingLabels(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSource{batch: refreshBatch("-10.00", "Pending coffee", true)}
	p, queries := newPoller(t, Options{Source: fake})

	// First sync inserts the pending transaction.
	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.NewTransactions)
	assert.Equal(t, 0, res.UpdatedTransactions)

	// The user labels the transaction.
	n, err := queries.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{
		ID:     "txn-1",
		Labels: `{"category":"food"}`,
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	// The source now reports the same transaction as posted with a corrected amount.
	fake.batch = refreshBatch("-12.34", "Coffee", false)

	res2, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.NewTransactions, "no new rows on re-sync")
	assert.Equal(t, 1, res2.UpdatedTransactions, "existing row refreshed")

	// Bridge-owned fields are refreshed...
	got, err := queries.GetTransaction(ctx, "txn-1")
	require.NoError(t, err)
	assert.Equal(t, "-12.34", got.Amount, "amount refreshed")
	assert.Equal(t, int64(0), got.Pending, "pending cleared after posting")
	assert.Equal(t, "Coffee", got.Description, "description refreshed")
	// ...while the user's label survives the sync untouched.
	assert.JSONEq(t, `{"category":"food"}`, got.Labels, "labels must not be clobbered by a sync")
}

// mustCreateRule inserts a labels-only rule directly for tests.
func mustCreateRule(t *testing.T, q db.Querier, query, labelsJSON string, enabled bool) db.Rule {
	t.Helper()
	return mustCreateRuleFull(t, q, query, labelsJSON, "{}", enabled)
}

// mustCreateRuleFull inserts a rule that may apply labels and/or extensions.
func mustCreateRuleFull(t *testing.T, q db.Querier, query, labelsJSON, extJSON string, enabled bool) db.Rule {
	t.Helper()
	en := int64(0)
	if enabled {
		en = 1
	}
	r, err := q.CreateRule(context.Background(), db.CreateRuleParams{
		Query: query, Labels: labelsJSON, Extensions: extJSON, Enabled: en, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)
	return r
}

func TestSyncAppliesRulesToNewTransactions(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}})
	ctx := context.Background()

	// An enabled rule labels coffee; a disabled rule that would label books must
	// be skipped.
	mustCreateRule(t, queries, "description:coffee", `{"category":"coffee"}`, true)
	mustCreateRule(t, queries, "description:books", `{"category":"books"}`, false)

	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, res.NewTransactions)
	assert.Equal(t, 1, res.AutoLabeled, "only the coffee transaction matched an enabled rule")

	coffee, err := queries.GetTransaction(ctx, "txn-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"category":"coffee"}`, coffee.Labels)

	books, err := queries.GetTransaction(ctx, "txn-2")
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, books.Labels, "the disabled rule must not apply")
}

func TestSyncRulesOnlyApplyToNewTransactions(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}})
	ctx := context.Background()

	// First sync inserts the transactions with no rules in place.
	_, err := p.Sync(ctx)
	require.NoError(t, err)

	// Add a matching rule, then re-sync the identical data: the existing rows are
	// not re-evaluated (rules apply only to brand-new inserts), preserving the
	// "a sync never clobbers labels" invariant.
	mustCreateRule(t, queries, "description:coffee", `{"category":"coffee"}`, true)

	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, res.NewTransactions)
	assert.Equal(t, 0, res.AutoLabeled)

	coffee, err := queries.GetTransaction(ctx, "txn-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, coffee.Labels, "rules do not re-label existing transactions on re-sync")
}

func TestSyncRecordsSourceErrorInLog(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeSource{err: errors.New("upstream boom")}})

	_, err := p.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream boom")

	latest, err := queries.LatestSyncLog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "error", latest.Status)
	assert.True(t, latest.Error.Valid)
}

func TestSyncWithoutPullerFails(t *testing.T) {
	p, queries := newPoller(t, Options{Source: bareSource{}})

	_, err := p.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support polling")

	latest, err := queries.LatestSyncLog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "error", latest.Status)
}

func TestIngestPersistsDelivery(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeReceiver{batch: sampleBatch()}})
	ctx := context.Background()

	res, err := p.Ingest(ctx, source.Delivery{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Accounts)
	assert.Equal(t, 2, res.NewTransactions)

	// The delivery rode the shared persist path, so the transaction exists...
	txn, err := queries.GetTransaction(ctx, "txn-1")
	require.NoError(t, err)
	assert.Equal(t, "Coffee", txn.Description)
	// ...and the run was recorded to sync_log as a success, like a scheduled sync.
	latest, err := queries.LatestSyncLog(ctx)
	require.NoError(t, err)
	assert.Equal(t, "success", latest.Status)
}

func TestIngestUnauthorizedWritesNoSyncLog(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeReceiver{err: source.ErrUnauthorizedDelivery}})
	ctx := context.Background()

	_, err := p.Ingest(ctx, source.Delivery{})
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)

	// A rejected delivery must not create a sync_log row (no log spam from an
	// unauthenticated caller).
	_, err = queries.LatestSyncLog(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestIngestPingIsNoOp(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeReceiver{batch: nil}})
	ctx := context.Background()

	res, err := p.Ingest(ctx, source.Delivery{})
	require.NoError(t, err)
	assert.Equal(t, SyncResult{}, res)
	// An authenticated ping persists nothing and logs nothing.
	_, err = queries.LatestSyncLog(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

func TestIngestOnNonReceiverFails(t *testing.T) {
	p, _ := newPoller(t, Options{Source: bareSource{}})
	_, err := p.Ingest(context.Background(), source.Delivery{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not accept inbound deliveries")
}

func TestSyncIsNoOpForReceiverOnlySource(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeReceiver{batch: sampleBatch()}})
	ctx := context.Background()

	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, SyncResult{}, res)

	// A push source is fed by Ingest; Sync ("Sync now"/"Sync all") neither persists
	// nor logs.
	_, err = queries.GetTransaction(ctx, "txn-1")
	assert.ErrorIs(t, err, sql.ErrNoRows)
	_, err = queries.LatestSyncLog(ctx)
	assert.ErrorIs(t, err, sql.ErrNoRows)
}

// credSource implements source.Credentialed so the engine's Connector methods
// can be exercised independently of any real source.
type credSource struct {
	connected bool
	lastInput string
	setErr    error
}

func (c *credSource) Descriptor() source.Descriptor { return source.Descriptor{Type: "cred"} }
func (c *credSource) Fetch(context.Context, time.Time, string) (*source.ImportBatch, error) {
	return &source.ImportBatch{Source: "cred"}, nil
}
func (c *credSource) CredentialConfigured(context.Context) (bool, error) { return c.connected, nil }
func (c *credSource) SetCredential(_ context.Context, input string) error {
	if c.setErr != nil {
		return c.setErr
	}
	c.lastInput = input
	c.connected = true
	return nil
}

func TestConnectorDelegatesToCredentialedSource(t *testing.T) {
	ctx := context.Background()
	cs := &credSource{}
	p, _ := newPoller(t, Options{Source: cs})

	connected, err := p.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, connected)

	require.NoError(t, p.SetCredential(ctx, "the-token"))
	assert.Equal(t, "the-token", cs.lastInput)

	connected, err = p.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.True(t, connected)
}

func TestConnectorWithoutCredentialedSource(t *testing.T) {
	ctx := context.Background()
	p, _ := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}})

	connected, err := p.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, connected, "a source without runtime credentials reports not-configured")

	err = p.SetCredential(ctx, "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support runtime credentials")
}

// --- event emission ---

func eventTypeCount(evs []db.Event, typ string) int {
	n := 0
	for _, e := range evs {
		if e.EventType == typ {
			n++
		}
	}
	return n
}

func TestSyncEmitsCreationEvents(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}, Emitter: events.NewEmitter(bus)})
	ctx := context.Background()

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	evs, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 100})
	require.NoError(t, err)
	assert.Equal(t, 1, eventTypeCount(evs, events.TypeAccountCreated))
	assert.Equal(t, 2, eventTypeCount(evs, events.TypeTransactionCreated))
	assert.Equal(t, 1, eventTypeCount(evs, events.TypeSyncCompleted))
}

func TestResyncEmitsTransactionUpdatedOnlyOnChange(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSource{batch: refreshBatch("-10.00", "Pending coffee", true)}
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: fake, Emitter: events.NewEmitter(bus)})

	_, err := p.Sync(ctx) // insert
	require.NoError(t, err)

	// An identical re-sync changes nothing, so no transaction.updated.
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	updated, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeTransactionUpdated, RowLimit: 100})
	require.NoError(t, err)
	assert.Empty(t, updated, "an unchanged re-sync emits no transaction.updated")

	// Changing the amount emits exactly one transaction.updated.
	fake.batch = refreshBatch("-12.34", "Coffee", false)
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	updated, err = queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeTransactionUpdated, RowLimit: 100})
	require.NoError(t, err)
	assert.Len(t, updated, 1, "a changed re-sync emits one transaction.updated")
}

func TestSyncEmitsLabelAppliedFromRules(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}, Emitter: events.NewEmitter(bus)})
	ctx := context.Background()
	mustCreateRule(t, queries, "description:coffee", `{"category":"coffee"}`, true)

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	applied, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeLabelApplied, RowLimit: 100})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	assert.Equal(t, "txn-1", applied[0].EntityID)
}

func TestSyncWithoutEmitterRecordsNoEvents(t *testing.T) {
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}}) // no Emitter
	ctx := context.Background()

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	evs, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 100})
	require.NoError(t, err)
	assert.Empty(t, evs, "a nil emitter records nothing")

	vers, err := queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	assert.Empty(t, vers, "a nil emitter records no versions either")
}

func TestSyncRecordsImportedVersions(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}, Emitter: events.NewEmitter(bus)})
	ctx := context.Background()

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	for _, id := range []string{"txn-1", "txn-2"} {
		vers, err := queries.ListTransactionVersions(ctx, id)
		require.NoError(t, err)
		require.Len(t, vers, 1, "each new transaction gets exactly one imported version")
		assert.Equal(t, events.ChangeImported, vers[0].ChangeKind)
	}
}

func TestSyncImportedVersionFoldsRuleLabels(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}, Emitter: events.NewEmitter(bus)})
	ctx := context.Background()
	mustCreateRule(t, queries, "description:coffee", `{"category":"coffee"}`, true)

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	vers, err := queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	require.Len(t, vers, 1, "still a single imported version, with the rule labels folded in")
	assert.Equal(t, events.ChangeImported, vers[0].ChangeKind)
	assert.Contains(t, vers[0].Data, `"category":"coffee"`, "v1 snapshot captures the auto-applied label")
}

func TestSyncAppliesExtensionRuleToNewTransactions(t *testing.T) {
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: &fakeSource{batch: sampleBatch()}, Emitter: events.NewEmitter(bus)})
	ctx := context.Background()

	// A rule that applies only a schema extension (no label) to coffee.
	mustCreateRuleFull(t, queries, "description:coffee", "{}", `{"tax.category":"meal"}`, true)

	res, err := p.Sync(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.AutoLabeled, "the coffee transaction was modified by an extension rule")

	coffee, err := queries.GetTransaction(ctx, "txn-1")
	require.NoError(t, err)
	assert.JSONEq(t, `{"tax.category":"meal"}`, coffee.Extensions)
	assert.JSONEq(t, `{}`, coffee.Labels, "the rule applied only an extension")

	// An extension.set event was emitted for the new key.
	set, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeExtensionSet, RowLimit: 100})
	require.NoError(t, err)
	require.Len(t, set, 1)
	assert.Equal(t, "txn-1", set[0].EntityID)

	// The single imported version folds in the auto-applied extension.
	vers, err := queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	require.Len(t, vers, 1, "still a single imported version, with the rule extension folded in")
	assert.Equal(t, events.ChangeImported, vers[0].ChangeKind)
	assert.Contains(t, vers[0].Data, `"tax.category":"meal"`, "v1 snapshot captures the auto-applied extension")
}

func TestResyncRecordsSyncedVersion(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSource{batch: refreshBatch("-10.00", "Pending coffee", true)}
	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{Source: fake, Emitter: events.NewEmitter(bus)})

	_, err := p.Sync(ctx) // insert: imported v1 (pending, -10.00)
	require.NoError(t, err)
	vers, err := queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	require.Len(t, vers, 1)
	assert.Equal(t, events.ChangeImported, vers[0].ChangeKind)

	// An identical re-sync changes no bridge field, so it records no version.
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	vers, err = queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	require.Len(t, vers, 1, "an unchanged re-sync records no version")

	// Correcting the amount and posting the charge records one synced version.
	fake.batch = refreshBatch("-12.34", "Coffee", false)
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	vers, err = queries.ListTransactionVersions(ctx, "txn-1")
	require.NoError(t, err)
	require.Len(t, vers, 2, "a changed re-sync appends a synced version")
	assert.Equal(t, events.ChangeSynced, vers[1].ChangeKind)
	assert.Contains(t, vers[1].Data, "-12.34", "the synced snapshot has the corrected amount")
}

func TestBoolToInt(t *testing.T) {
	assert.Equal(t, int64(1), boolToInt(true))
	assert.Equal(t, int64(0), boolToInt(false))
}
