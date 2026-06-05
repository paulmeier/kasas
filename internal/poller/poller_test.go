package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/testutil"
	"github.com/paulmeier/kasas/internal/vault"
)

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
