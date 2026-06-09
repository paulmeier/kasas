package teller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// --- helpers ---

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// paginate returns up to size items strictly after fromID from a newest-first
// list, mimicking Teller's backward (from_id) pagination.
func paginate(txns []Transaction, fromID string, size int) []Transaction {
	start := 0
	if fromID != "" {
		for i, t := range txns {
			if t.ID == fromID {
				start = i + 1
				break
			}
		}
	}
	if start > len(txns) {
		start = len(txns)
	}
	end := start + size
	if end > len(txns) {
		end = len(txns)
	}
	return txns[start:end]
}

// bankData is one enrollment's data, keyed in a test server by its access token.
type bankData struct {
	accounts []Account
	balances map[string]*Balance      // account id -> balance
	txns     map[string][]Transaction // account id -> transactions (newest-first)
}

// newServer routes the Teller endpoints, selecting a bank by the access token in
// the basic-auth username — so several tokens return different accounts (the
// fan-out). An unknown token gets 401, and the password must be empty.
func newServer(t *testing.T, banks map[string]bankData, pageSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, pass, ok := r.BasicAuth()
		require.True(t, ok, "request must carry basic auth")
		assert.Empty(t, pass, "basic-auth password is empty")

		bank, known := banks[token]
		if !known {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"unknown token"}}`))
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.URL.Path == "/accounts":
			writeJSON(t, w, bank.accounts)
		case len(parts) == 3 && parts[0] == "accounts" && parts[2] == "balances":
			bal := bank.balances[parts[1]]
			if bal == nil {
				http.Error(w, "no balance", http.StatusNotFound)
				return
			}
			writeJSON(t, w, bal)
		case len(parts) == 3 && parts[0] == "accounts" && parts[2] == "transactions":
			writeJSON(t, w, paginate(bank.txns[parts[1]], r.URL.Query().Get("from_id"), pageSize))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func newSource(t *testing.T, opts Options) (*Source, vault.SecretStore) {
	t.Helper()
	if opts.Secrets == nil {
		opts.Secrets = vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	}
	s, err := New(opts)
	require.NoError(t, err)
	return s, opts.Secrets
}

func unix(t *testing.T, date string) int64 {
	t.Helper()
	d, err := time.Parse(dateLayout, date)
	require.NoError(t, err)
	return d.UTC().Unix()
}

func unixTime(t *testing.T, date string) time.Time {
	t.Helper()
	d, err := time.Parse(dateLayout, date)
	require.NoError(t, err)
	return d.UTC()
}

func txnIDs(txns []Transaction) []string {
	ids := make([]string, len(txns))
	for i, t := range txns {
		ids[i] = t.ID
	}
	return ids
}

func acctIDs(accts []source.ImportAccount) []string {
	ids := make([]string, len(accts))
	for i, a := range accts {
		ids[i] = a.ExternalID
	}
	return ids
}

// --- client ---

func TestAccounts(t *testing.T) {
	ctx := context.Background()

	t.Run("sends token as basic-auth username, parses response", func(t *testing.T) {
		var gotUser, gotPass, gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotUser, gotPass, _ = r.BasicAuth()
			gotPath = r.URL.Path
			writeJSON(t, w, []Account{{ID: "acc_1", Name: "Checking", Currency: "USD", Institution: Institution{ID: "chase", Name: "Chase"}}})
		}))
		defer srv.Close()

		accs, err := NewClient(srv.URL, nil).Accounts(ctx, "secret_token")
		require.NoError(t, err)
		require.Len(t, accs, 1)
		assert.Equal(t, "acc_1", accs[0].ID)
		assert.Equal(t, "/accounts", gotPath)
		assert.Equal(t, "secret_token", gotUser)
		assert.Empty(t, gotPass)
	})

	t.Run("non-200 surfaces the Teller error envelope", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad token"}}`))
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, nil).Accounts(ctx, "t")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bad token")
		assert.Contains(t, err.Error(), "401")
	})
}

func TestBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/accounts/acc_1/balances", r.URL.Path)
		writeJSON(t, w, Balance{AccountID: "acc_1", Ledger: "28575.02", Available: "28000.00"})
	}))
	defer srv.Close()

	bal, err := NewClient(srv.URL, nil).Balance(context.Background(), "t", "acc_1")
	require.NoError(t, err)
	assert.Equal(t, "28575.02", bal.Ledger)
	assert.Equal(t, "28000.00", bal.Available)
}

func TestTransactionsPagination(t *testing.T) {
	ctx := context.Background()
	txns := []Transaction{ // newest-first, as Teller returns them
		{ID: "txn_1", Date: "2024-06-10", Amount: "-1.00"},
		{ID: "txn_2", Date: "2024-06-05", Amount: "-2.00"},
		{ID: "txn_3", Date: "2024-06-01", Amount: "-3.00"},
		{ID: "txn_4", Date: "2024-05-20", Amount: "-4.00"},
		{ID: "txn_5", Date: "2024-05-10", Amount: "-5.00"},
	}
	banks := map[string]bankData{"tok": {txns: map[string][]Transaction{"acc_1": txns}}}

	t.Run("pages backward and stops at the since cutoff", func(t *testing.T) {
		srv := newServer(t, banks, 2) // small pages force pagination
		defer srv.Close()

		got, err := NewClient(srv.URL, nil).Transactions(ctx, "tok", "acc_1", unixTime(t, "2024-06-01"))
		require.NoError(t, err)
		assert.Equal(t, []string{"txn_1", "txn_2", "txn_3"}, txnIDs(got))
	})

	t.Run("zero since fetches the full history", func(t *testing.T) {
		srv := newServer(t, banks, 2)
		defer srv.Close()

		got, err := NewClient(srv.URL, nil).Transactions(ctx, "tok", "acc_1", time.Time{})
		require.NoError(t, err)
		assert.Len(t, got, 5)
	})

	t.Run("sends a count hint", func(t *testing.T) {
		var gotCount string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotCount = r.URL.Query().Get("count")
			writeJSON(t, w, []Transaction{})
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, nil).Transactions(ctx, "tok", "acc_1", time.Time{})
		require.NoError(t, err)
		assert.Equal(t, "100", gotCount)
	})
}

// --- mapping ---

func TestToImportTxn(t *testing.T) {
	got := toImportTxn(Transaction{
		ID:          "txn_x",
		Amount:      "-12.34",
		Date:        "2024-06-10",
		Description: "ATM Withdrawal",
		Status:      "pending",
		Details:     TransactionDetails{Counterparty: Counterparty{Name: "Starbucks", Type: "organization"}},
	})
	assert.Equal(t, "teller:txn_x", got.ExternalID, "id is namespaced")
	assert.Equal(t, "-12.34", got.Amount, "amount passes through verbatim")
	assert.Equal(t, "ATM Withdrawal", got.Description)
	assert.Equal(t, "Starbucks", got.Payee, "payee is the cleaned counterparty")
	assert.Equal(t, unix(t, "2024-06-10"), got.Date)
	assert.True(t, got.Pending)
}

func TestToImportAccount(t *testing.T) {
	a := Account{ID: "acc_1", Name: "Platinum Card", Currency: "USD", Institution: Institution{ID: "security_cu", Name: "Security Credit Union"}}
	got := toImportAccount(a, &Balance{Ledger: "100.50"}, 1700000000)
	assert.Equal(t, "teller:acc_1", got.ExternalID)
	assert.Equal(t, "teller:org:security_cu", got.Org.ID)
	assert.Equal(t, "Security Credit Union", got.Org.Name)
	assert.Equal(t, "Platinum Card", got.Name)
	assert.Equal(t, "USD", got.Currency)
	assert.Equal(t, "100.50", got.Balance)
	assert.Equal(t, int64(1700000000), got.BalanceDate)

	t.Run("nil balance leaves balance unknown", func(t *testing.T) {
		got := toImportAccount(a, nil, 1700000000)
		assert.Empty(t, got.Balance)
		assert.Zero(t, got.BalanceDate)
	})

	t.Run("org id falls back to a slug of the name", func(t *testing.T) {
		got := toImportAccount(Account{ID: "acc_2", Institution: Institution{Name: "My Bank"}}, nil, 0)
		assert.Equal(t, "teller:org:my-bank", got.Org.ID)
	})
}

func TestAccountName(t *testing.T) {
	assert.Equal(t, "Checking", accountName(Account{Name: "Checking", Subtype: "checking"}))
	assert.Equal(t, "checking", accountName(Account{Subtype: "checking", Type: "depository"}))
	assert.Equal(t, "depository", accountName(Account{Type: "depository"}))
	assert.Equal(t, "Account", accountName(Account{}))
}

func TestTransactionDate(t *testing.T) {
	assert.Equal(t, unix(t, "2023-07-13"), transactionDate(Transaction{Date: "2023-07-13"}))
	assert.Zero(t, transactionDate(Transaction{Date: "not-a-date"}))
}

// --- token helpers ---

func TestSplitTokens(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, splitTokens("a\nb\nc"))
	assert.Equal(t, []string{"a", "b"}, splitTokens(" a , b "), "comma or newline, trimmed")
	assert.Equal(t, []string{"a"}, splitTokens("a\n\na\n"), "dedupes and drops empties")
	assert.Empty(t, splitTokens("   "))
}

func TestMaskAndTokenID(t *testing.T) {
	assert.Equal(t, "••••cd34", maskToken("token_abcd34"))
	assert.Equal(t, "••••", maskToken("abc"))
	id := tokenID("token_abc")
	assert.Len(t, id, 12)
	assert.Equal(t, id, tokenID("token_abc"), "stable")
	assert.NotEqual(t, id, tokenID("token_xyz"), "distinct per token")
}

// --- source: fetch (single + fan-out) ---

func TestSourceFetch(t *testing.T) {
	banks := map[string]bankData{
		"test_token": {
			accounts: []Account{{ID: "acc_1", Name: "Checking", Currency: "USD", Institution: Institution{ID: "chase", Name: "Chase"}}},
			balances: map[string]*Balance{"acc_1": {Ledger: "1000.00", Available: "950.00"}},
			txns: map[string][]Transaction{"acc_1": {
				{ID: "txn_1", Amount: "-12.34", Date: "2024-06-10", Description: "Coffee", Status: "posted", Details: TransactionDetails{Counterparty: Counterparty{Name: "Starbucks"}}},
				{ID: "txn_2", Amount: "-56.78", Date: "2024-06-05", Description: "Books", Status: "pending"},
			}},
		},
	}
	srv := newServer(t, banks, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"test_token"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)

	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	a := batch.Accounts[0]
	assert.Equal(t, "teller:acc_1", a.ExternalID)
	assert.Equal(t, "teller:org:chase", a.Org.ID)
	assert.Equal(t, "Checking", a.Name)
	assert.Equal(t, "1000.00", a.Balance)
	assert.Positive(t, a.BalanceDate)
	require.Len(t, a.Transactions, 2)
	assert.Equal(t, "teller:txn_1", a.Transactions[0].ExternalID)
	assert.Equal(t, "Starbucks", a.Transactions[0].Payee)
	assert.False(t, a.Transactions[0].Pending)
	assert.True(t, a.Transactions[1].Pending)
}

func TestSourceFetchFanOut(t *testing.T) {
	banks := map[string]bankData{
		"chase_token": {
			accounts: []Account{{ID: "acc_1", Name: "Chase Checking", Currency: "USD", Institution: Institution{ID: "chase", Name: "Chase"}}},
			balances: map[string]*Balance{"acc_1": {Ledger: "1000.00"}},
			txns:     map[string][]Transaction{"acc_1": {{ID: "txn_1", Amount: "-1.00", Date: "2024-06-10", Status: "posted"}}},
		},
		"amex_token": {
			accounts: []Account{{ID: "acc_2", Name: "Amex Card", Currency: "USD", Institution: Institution{ID: "amex", Name: "American Express"}}},
			balances: map[string]*Balance{"acc_2": {Ledger: "-200.00"}},
			txns:     map[string][]Transaction{"acc_2": {{ID: "txn_2", Amount: "-50.00", Date: "2024-06-09", Status: "posted"}}},
		},
	}
	srv := newServer(t, banks, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"chase_token", "amex_token"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 2, "accounts from both enrollments are merged")
	assert.ElementsMatch(t, []string{"teller:acc_1", "teller:acc_2"}, acctIDs(batch.Accounts))
}

func TestSourceFetchToleratesOneBadEnrollment(t *testing.T) {
	banks := map[string]bankData{
		"good_token": {
			accounts: []Account{{ID: "acc_1", Name: "Checking", Currency: "USD"}},
			txns:     map[string][]Transaction{"acc_1": {{ID: "txn_1", Amount: "-1.00", Date: "2024-06-10", Status: "posted"}}},
		},
		// "bad_token" is absent -> 401.
	}
	srv := newServer(t, banks, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"good_token", "bad_token"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err, "a single failing enrollment must not fail the whole sync")
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "teller:acc_1", batch.Accounts[0].ExternalID)
}

func TestSourceFetchAllEnrollmentsFail(t *testing.T) {
	srv := newServer(t, map[string]bankData{}, 100) // every token is unknown -> 401
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"bad_1", "bad_2"}})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
}

func TestSourceFetchBalanceFailureIsNonFatal(t *testing.T) {
	banks := map[string]bankData{"test_token": {
		accounts: []Account{{ID: "acc_1", Name: "Checking", Currency: "USD"}},
		// no balances entry -> /balances 404
		txns: map[string][]Transaction{"acc_1": {{ID: "txn_1", Amount: "-1.00", Date: "2024-06-10", Status: "posted"}}},
	}}
	srv := newServer(t, banks, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"test_token"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 1)
	assert.Empty(t, batch.Accounts[0].Balance)
	assert.Len(t, batch.Accounts[0].Transactions, 1)
}

func TestSourceFetchWithoutCredential(t *testing.T) {
	s, _ := newSource(t, Options{BaseURL: "http://unused.invalid"})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Teller access token")
}

// --- source: credentials (add / list / remove / resolve) ---

func TestSetCredentialAppends(t *testing.T) {
	ctx := context.Background()
	s, store := newSource(t, Options{})

	configured, err := s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, configured)

	require.NoError(t, s.SetCredential(ctx, "  token_a  "))
	require.NoError(t, s.SetCredential(ctx, "token_b"))

	stored, err := store.SecretValue(ctx, accessTokenKey)
	require.NoError(t, err)
	assert.Equal(t, "token_a\ntoken_b", stored, "tokens accumulate (trimmed), not replaced")

	configured, err = s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.True(t, configured)

	require.NoError(t, s.SetCredential(ctx, "token_a"), "re-adding is a no-op")
	stored, _ = store.SecretValue(ctx, accessTokenKey)
	assert.Equal(t, "token_a\ntoken_b", stored)

	require.Error(t, s.SetCredential(ctx, "   "), "an empty credential is rejected")
}

func TestListAndRemoveCredentials(t *testing.T) {
	ctx := context.Background()
	s, _ := newSource(t, Options{AccessTokens: []string{"config_tok"}})
	require.NoError(t, s.SetCredential(ctx, "run_a"))
	require.NoError(t, s.SetCredential(ctx, "run_b"))

	entries, err := s.ListCredentials(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	byID := map[string]source.CredentialEntry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	assert.False(t, byID[tokenID("config_tok")].Removable, "config token is not removable")
	assert.True(t, byID[tokenID("run_a")].Removable)
	assert.Equal(t, "••••un_a", byID[tokenID("run_a")].Label)

	require.NoError(t, s.RemoveCredential(ctx, tokenID("run_a")))
	entries, _ = s.ListCredentials(ctx)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.NotEqual(t, tokenID("run_a"), e.ID, "removed token is gone")
	}

	require.Error(t, s.RemoveCredential(ctx, tokenID("config_tok")), "a config token is not removable")
	require.Error(t, s.RemoveCredential(ctx, "deadbeefdead"), "removing an unknown id errors")
}

func TestResolveTokens(t *testing.T) {
	ctx := context.Background()

	t.Run("unions config and stored, deduped", func(t *testing.T) {
		store := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
		require.NoError(t, store.SetSecretValue(ctx, accessTokenKey, "shared\nstored_only"))
		s, _ := newSource(t, Options{Secrets: store, AccessTokens: []string{"config_only", "shared"}})

		got, err := s.resolveTokens(ctx)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"config_only", "shared", "stored_only"}, got)
	})

	t.Run("empty when none configured", func(t *testing.T) {
		s, _ := newSource(t, Options{})
		got, err := s.resolveTokens(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestMTLSCertificateValidation(t *testing.T) {
	t.Run("certificate without a key is an error", func(t *testing.T) {
		s, _ := newSource(t, Options{AccessTokens: []string{"t"}, Certificate: "/some/cert.pem"})
		_, err := s.httpClient()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "both certificate and private_key")
	})

	t.Run("an unreadable certificate is an error", func(t *testing.T) {
		s, _ := newSource(t, Options{Certificate: "/no/such/cert.pem", PrivateKey: "/no/such/key.pem"})
		_, err := s.httpClient()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load client certificate")
	})

	t.Run("no certificate yields a plain client (sandbox)", func(t *testing.T) {
		s, _ := newSource(t, Options{AccessTokens: []string{"t"}})
		c, err := s.httpClient()
		require.NoError(t, err)
		assert.NotNil(t, c)
	})
}

// --- registration ---

func TestRegisteredAndConstructable(t *testing.T) {
	assert.True(t, source.Registered(SourceType), "importing the package registers the source")

	s, err := source.New(SourceType, source.Env{
		Secrets: vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		Options: map[string]string{"access_tokens": "x\ny"},
	})
	require.NoError(t, err)

	_, isPuller := s.(source.Puller)
	assert.True(t, isPuller, "the teller source is a Puller")
	_, isCred := s.(source.Credentialed)
	assert.True(t, isCred, "the teller source is Credentialed")
	_, isMulti := s.(source.MultiCredentialed)
	assert.True(t, isMulti, "the teller source is MultiCredentialed")
	assert.Equal(t, SourceType, s.Descriptor().Type)
}
