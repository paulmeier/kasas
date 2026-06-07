package simplefin

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
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

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func newSource(t *testing.T, opts Options) (*Source, vault.SecretStore) {
	t.Helper()
	if opts.Secrets == nil {
		opts.Secrets = vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	}
	return New(opts), opts.Secrets
}

// --- client ---

func TestClaim(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte("https://user:pass@bridge/simplefin\n"))
		}))
		defer srv.Close()

		got, err := NewClient().Claim(ctx, base64Encode(srv.URL))
		require.NoError(t, err)
		assert.Equal(t, "https://user:pass@bridge/simplefin", got)
	})

	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := NewClient().Claim(ctx, base64Encode(srv.URL))
		require.Error(t, err)
	})

	t.Run("bad base64 is an error", func(t *testing.T) {
		_, err := NewClient().Claim(ctx, "!!!not-base64!!!")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode setup token")
	})
}

func TestFetch(t *testing.T) {
	ctx := context.Background()

	t.Run("sends basic auth and query params, parses response", func(t *testing.T) {
		var gotAuth, gotPath string
		var gotQuery url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if u, p, ok := r.BasicAuth(); ok {
				gotAuth = u + ":" + p
			}
			gotPath = r.URL.Path
			gotQuery = r.URL.Query()
			_, _ = w.Write([]byte(sampleAccounts))
		}))
		defer srv.Close()

		u, _ := url.Parse(srv.URL)
		u.User = url.UserPassword("user", "secret")

		set, err := NewClient().Fetch(ctx, u.String(), time.Unix(testutil.Date2024Jun, 0))
		require.NoError(t, err)
		require.Len(t, set.Accounts, 1)
		assert.Equal(t, "acct-1", set.Accounts[0].ID)
		assert.Len(t, set.Accounts[0].Transactions, 2)

		assert.Equal(t, "/accounts", gotPath)
		assert.Equal(t, "user:secret", gotAuth, "credentials from the URL userinfo are sent as basic auth")
		assert.Equal(t, "1", gotQuery.Get("pending"))
		assert.Equal(t, strconv.FormatInt(testutil.Date2024Jun, 10), gotQuery.Get("start-date"))
	})

	t.Run("omits start-date when since is zero", func(t *testing.T) {
		var gotQuery url.Values
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			_, _ = w.Write([]byte(sampleAccounts))
		}))
		defer srv.Close()

		_, err := NewClient().Fetch(ctx, srv.URL, time.Time{})
		require.NoError(t, err)
		assert.Empty(t, gotQuery.Get("start-date"))
	})

	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := NewClient().Fetch(ctx, srv.URL, time.Time{})
		require.Error(t, err)
	})

	t.Run("invalid url error does not leak credentials", func(t *testing.T) {
		_, err := NewClient().Fetch(ctx, "://user:supersecret@bad", time.Time{})
		require.Error(t, err)
		assert.Equal(t, "invalid SimpleFIN access URL", err.Error())
		assert.NotContains(t, err.Error(), "supersecret")
	})
}

func TestStableOrgID(t *testing.T) {
	assert.Equal(t, "the-id", Org{ID: "the-id", Domain: "d", SfinURL: "s"}.StableOrgID())
	assert.Equal(t, "d", Org{Domain: "d", SfinURL: "s"}.StableOrgID())
	assert.Equal(t, "s", Org{SfinURL: "s"}.StableOrgID())
}

func TestTransactionDate(t *testing.T) {
	assert.Equal(t, int64(5), transactionDate(Transaction{Posted: 5, TransactedAt: 9}))
	assert.Equal(t, int64(9), transactionDate(Transaction{Posted: 0, TransactedAt: 9}))
}

// --- mapping ---

func TestToImportBatch(t *testing.T) {
	set := &AccountSet{Accounts: []Account{{
		Org:         Org{Domain: "mybank.com", Name: "My Bank", SfinURL: "https://sfin.mybank.com"},
		ID:          "acct-1",
		Name:        "Checking",
		Currency:    "USD",
		Balance:     "1234.56",
		BalanceDate: 1700000000,
		Transactions: []Transaction{
			{ID: "txn-1", Posted: 1699990000, Amount: "-12.34", Description: "Coffee", Payee: "Cafe"},
			{ID: "txn-2", TransactedAt: 1699995000, Amount: "-56.78", Description: "Books", Pending: true}, // Posted=0 -> date from TransactedAt
		},
	}}}

	b := toImportBatch(set)
	assert.Equal(t, SourceType, b.Source)
	require.Len(t, b.Accounts, 1)

	a := b.Accounts[0]
	assert.Equal(t, "acct-1", a.ExternalID)
	assert.Equal(t, "mybank.com", a.Org.ID, "org id falls back to domain")
	assert.Equal(t, "https://sfin.mybank.com", a.Org.URL)
	require.Len(t, a.Transactions, 2)

	assert.Equal(t, "txn-1", a.Transactions[0].ExternalID)
	assert.Equal(t, int64(1699990000), a.Transactions[0].Date, "uses posted when set")
	assert.False(t, a.Transactions[0].Pending)

	assert.Equal(t, int64(1699995000), a.Transactions[1].Date, "falls back to transacted_at")
	assert.True(t, a.Transactions[1].Pending)
}

// --- source: fetch + credentials ---

func TestSourceFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/accounts", r.URL.Path)
		_, _ = w.Write([]byte(sampleAccounts))
	}))
	defer srv.Close()

	s, _ := newSource(t, Options{ConfigAccessURL: srv.URL})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "acct-1", batch.Accounts[0].ExternalID)
	assert.Len(t, batch.Accounts[0].Transactions, 2)
}

func TestSourceFetchWithoutCredential(t *testing.T) {
	s, _ := newSource(t, Options{})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no SimpleFIN access URL")
}

func TestSetCredential(t *testing.T) {
	ctx := context.Background()

	t.Run("stores an access url directly", func(t *testing.T) {
		s, store := newSource(t, Options{})

		connected, err := s.CredentialConfigured(ctx)
		require.NoError(t, err)
		assert.False(t, connected)

		require.NoError(t, s.SetCredential(ctx, "https://user:pass@bridge.example/simplefin"))

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://user:pass@bridge.example/simplefin", stored)

		connected, err = s.CredentialConfigured(ctx)
		require.NoError(t, err)
		assert.True(t, connected)
	})

	t.Run("claims a setup token", func(t *testing.T) {
		claim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte("https://claimed-access\n"))
		}))
		defer claim.Close()

		s, store := newSource(t, Options{})
		require.NoError(t, s.SetCredential(ctx, base64Encode(claim.URL)))

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", stored)
	})

	t.Run("rejects an empty credential", func(t *testing.T) {
		s, _ := newSource(t, Options{})
		require.Error(t, s.SetCredential(ctx, "   "))
	})
}

func TestResolveAccessURLPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("stored secret wins", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		require.NoError(t, store.SetAccessURL(ctx, "https://stored"))
		s, _ := newSource(t, Options{Secrets: store, ConfigAccessURL: "https://config", SetupToken: "ignored"})

		got, err := s.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://stored", got)
	})

	t.Run("config url persisted when nothing stored", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		s, _ := newSource(t, Options{Secrets: store, ConfigAccessURL: "https://config"})

		got, err := s.resolveAccessURL(ctx)
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
		s, _ := newSource(t, Options{Secrets: store, SetupToken: base64Encode(claim.URL)})

		got, err := s.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", got)

		stored, err := store.AccessURL(ctx)
		require.NoError(t, err)
		assert.Equal(t, "https://claimed-access", stored)
	})

	t.Run("empty when unconfigured", func(t *testing.T) {
		s, _ := newSource(t, Options{})
		got, err := s.resolveAccessURL(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

// --- registration ---

func TestRegisteredAndConstructable(t *testing.T) {
	assert.True(t, source.Registered(SourceType), "importing the package registers the source")

	s, err := source.New(SourceType, source.Env{
		Secrets: vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		Options: map[string]string{"access_url": "https://x"},
	})
	require.NoError(t, err)

	_, isPuller := s.(source.Puller)
	assert.True(t, isPuller, "the simplefin source is a Puller")
	_, isCred := s.(source.Credentialed)
	assert.True(t, isCred, "the simplefin source is Credentialed")
	assert.Equal(t, SourceType, s.Descriptor().Type)
}
