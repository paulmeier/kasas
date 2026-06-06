package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
)

// refreshBody1/2 describe the same transaction (txn-1) across two syncs: it starts
// pending with one amount, then posts with a corrected amount. Used to verify a
// re-sync refreshes bridge fields while preserving user labels.
const refreshBody1 = `{"errors":[],"accounts":[{"org":{"domain":"mybank.com","name":"My Bank","sfin-url":"https://sfin.mybank.com"},"id":"acct-1","name":"Checking","currency":"USD","balance":"100.00","balance-date":1700000000,"transactions":[{"id":"txn-1","posted":1699990000,"amount":"-10.00","description":"Pending coffee","payee":"Cafe","pending":true}]}]}`

const refreshBody2 = `{"errors":[],"accounts":[{"org":{"domain":"mybank.com","name":"My Bank","sfin-url":"https://sfin.mybank.com"},"id":"acct-1","name":"Checking","currency":"USD","balance":"100.00","balance-date":1700000000,"transactions":[{"id":"txn-1","posted":1699990000,"amount":"-12.34","description":"Coffee","payee":"Cafe","pending":false}]}]}`

const sampleAccounts = `{
  "errors": [],
  "accounts": [
    {
      "org": {"domain": "mybank.com", "name": "My Bank", "sfin-url": "https://sfin.mybank.com"},
      "id": "acct-1",
      "name": "Checking",
      "currency": "USD",
      "balance": "1234.56",
      "balance-date": 1700000000,
      "transactions": [
        {"id": "txn-1", "posted": 1699990000, "amount": "-12.34", "description": "Coffee", "payee": "Cafe", "pending": false},
        {"id": "txn-2", "posted": 1699995000, "amount": "-56.78", "description": "Books", "payee": "Store", "memo": "gift", "pending": true}
      ]
    }
  ]
}`

func newPoller(t *testing.T, opts Options) (*Poller, db.Querier) {
	t.Helper()
	if opts.Store == nil {
		opts.Store = db.NewSQLiteStore(testutil.NewDB(t))
	}
	if opts.Secrets == nil {
		opts.Secrets = vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	}
	if opts.Interval == 0 {
		opts.Interval = time.Hour
	}
	return New(opts), opts.Store
}

func TestSyncPersistsAndIsIdempotent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/accounts", r.URL.Path)
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL})
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
	assert.Equal(t, "mybank.com", orgs[0].ID, "org id falls back to domain")

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

	latest, err := queries.LatestSyncLog(ctx)
	require.NoError(t, err)
	assert.Equal(t, "success", latest.Status)
}

func TestSyncRefreshesExistingPreservingLabels(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	body := refreshBody1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		b := body
		mu.Unlock()
		_, _ = w.Write([]byte(b))
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL})

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

	// The bridge now reports the same transaction as posted with a corrected amount.
	mu.Lock()
	body = refreshBody2
	mu.Unlock()

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

// mustCreateRule inserts a labeling rule directly for tests.
func mustCreateRule(t *testing.T, q db.Querier, query, labelsJSON string, enabled bool) db.Rule {
	t.Helper()
	en := int64(0)
	if enabled {
		en = 1
	}
	r, err := q.CreateRule(context.Background(), db.CreateRuleParams{
		Query: query, Labels: labelsJSON, Enabled: en, CreatedAt: 1, UpdatedAt: 1,
	})
	require.NoError(t, err)
	return r
}

func TestSyncAppliesRulesToNewTransactions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL})
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL})
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

func TestSetCredential(t *testing.T) {
	ctx := context.Background()

	t.Run("stores an access url directly", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		p, _ := newPoller(t, Options{Secrets: store})

		connected, err := p.CredentialConfigured(ctx)
		require.NoError(t, err)
		assert.False(t, connected)

		require.NoError(t, p.SetCredential(ctx, "https://user:pass@bridge.example/simplefin"))

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://user:pass@bridge.example/simplefin", stored)

		connected, err = p.CredentialConfigured(ctx)
		require.NoError(t, err)
		assert.True(t, connected)
	})

	t.Run("claims a setup token", func(t *testing.T) {
		claim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte("https://claimed-access\n"))
		}))
		defer claim.Close()

		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		p, _ := newPoller(t, Options{Secrets: store})

		require.NoError(t, p.SetCredential(ctx, base64Encode(claim.URL)))

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", stored)
	})

	t.Run("rejects an empty credential", func(t *testing.T) {
		p, _ := newPoller(t, Options{})
		require.Error(t, p.SetCredential(ctx, "   "))
	})
}

func TestSyncFailsWithoutAccessURL(t *testing.T) {
	p, queries := newPoller(t, Options{})

	_, err := p.Sync(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SimpleFIN access URL")

	latest, err := queries.LatestSyncLog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "error", latest.Status)
	assert.Equal(t, "no SimpleFIN access URL configured (set simplefin.setup_token or simplefin.access_url)", latest.Error.String)
}

func TestSyncReportsUpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL})
	_, err := p.Sync(context.Background())
	require.Error(t, err)

	latest, err := queries.LatestSyncLog(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "error", latest.Status)
	assert.True(t, latest.Error.Valid)
}

func TestResolveAccessURLPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("stored secret wins", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		require.NoError(t, store.SetAccessURL(ctx, "https://stored"))
		p, _ := newPoller(t, Options{Secrets: store, ConfigAccessURL: "https://config", SetupToken: "ignored"})

		got, err := p.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://stored", got)
	})

	t.Run("config url persisted when nothing stored", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		p, _ := newPoller(t, Options{Secrets: store, ConfigAccessURL: "https://config"})

		got, err := p.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://config", got)

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://config", stored, "resolved url should be persisted")
	})

	t.Run("claims setup token as last resort", func(t *testing.T) {
		claim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte("https://claimed-access\n"))
		}))
		defer claim.Close()

		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		token := base64Encode(claim.URL)
		p, _ := newPoller(t, Options{Secrets: store, SetupToken: token})

		got, err := p.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", got)

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", stored)
	})

	t.Run("empty when unconfigured", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		p, _ := newPoller(t, Options{Secrets: store})

		got, err := p.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL, Emitter: events.NewEmitter(bus)})
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
	var mu sync.Mutex
	body := refreshBody1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		b := body
		mu.Unlock()
		_, _ = w.Write([]byte(b))
	}))
	defer srv.Close()

	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL, Emitter: events.NewEmitter(bus)})

	_, err := p.Sync(ctx) // insert
	require.NoError(t, err)

	// An identical re-sync changes nothing, so no transaction.updated.
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	updated, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeTransactionUpdated, RowLimit: 100})
	require.NoError(t, err)
	assert.Empty(t, updated, "an unchanged re-sync emits no transaction.updated")

	// Changing the amount emits exactly one transaction.updated.
	mu.Lock()
	body = refreshBody2
	mu.Unlock()
	_, err = p.Sync(ctx)
	require.NoError(t, err)
	updated, err = queries.ListEventsAfter(ctx, db.ListEventsAfterParams{EventType: events.TypeTransactionUpdated, RowLimit: 100})
	require.NoError(t, err)
	assert.Len(t, updated, 1, "a changed re-sync emits one transaction.updated")
}

func TestSyncEmitsLabelAppliedFromRules(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	bus := events.NewBus()
	defer bus.Close()
	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL, Emitter: events.NewEmitter(bus)})
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	p, queries := newPoller(t, Options{ConfigAccessURL: srv.URL}) // no Emitter
	ctx := context.Background()

	_, err := p.Sync(ctx)
	require.NoError(t, err)

	evs, err := queries.ListEventsAfter(ctx, db.ListEventsAfterParams{RowLimit: 100})
	require.NoError(t, err)
	assert.Empty(t, evs, "a nil emitter records nothing")
}
