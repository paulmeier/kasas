package poller

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/testutil"
)

func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func TestClaim(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			_, _ = w.Write([]byte("https://user:pass@bridge/simplefin\n"))
		}))
		defer srv.Close()

		got, err := NewSimpleFINClient().Claim(ctx, base64Encode(srv.URL))
		require.NoError(t, err)
		assert.Equal(t, "https://user:pass@bridge/simplefin", got)
	})

	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
		defer srv.Close()

		_, err := NewSimpleFINClient().Claim(ctx, base64Encode(srv.URL))
		require.Error(t, err)
	})

	t.Run("bad base64 is an error", func(t *testing.T) {
		_, err := NewSimpleFINClient().Claim(ctx, "!!!not-base64!!!")
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

		set, err := NewSimpleFINClient().Fetch(ctx, u.String(), time.Unix(testutil.Date2024Jun, 0))
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

		_, err := NewSimpleFINClient().Fetch(ctx, srv.URL, time.Time{})
		require.NoError(t, err)
		assert.Empty(t, gotQuery.Get("start-date"))
	})

	t.Run("non-200 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		_, err := NewSimpleFINClient().Fetch(ctx, srv.URL, time.Time{})
		require.Error(t, err)
	})

	t.Run("invalid url error does not leak credentials", func(t *testing.T) {
		_, err := NewSimpleFINClient().Fetch(ctx, "://user:supersecret@bad", time.Time{})
		require.Error(t, err)
		assert.Equal(t, "invalid SimpleFIN access URL", err.Error())
		assert.NotContains(t, err.Error(), "supersecret")
	})
}

func TestStableOrgID(t *testing.T) {
	assert.Equal(t, "the-id", SimpleFINOrg{ID: "the-id", Domain: "d", SfinURL: "s"}.StableOrgID())
	assert.Equal(t, "d", SimpleFINOrg{Domain: "d", SfinURL: "s"}.StableOrgID())
	assert.Equal(t, "s", SimpleFINOrg{SfinURL: "s"}.StableOrgID())
}

func TestTransactionDate(t *testing.T) {
	assert.Equal(t, int64(5), transactionDate(SimpleFINTransaction{Posted: 5, TransactedAt: 9}))
	assert.Equal(t, int64(9), transactionDate(SimpleFINTransaction{Posted: 0, TransactedAt: 9}))
}

func TestBoolToInt(t *testing.T) {
	assert.Equal(t, int64(1), boolToInt(true))
	assert.Equal(t, int64(0), boolToInt(false))
}
