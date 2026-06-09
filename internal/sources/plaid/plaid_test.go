package plaid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

// writePlaidError writes a Plaid error envelope with the given status.
func writePlaidError(t *testing.T, w http.ResponseWriter, status int, code, msg string) {
	t.Helper()
	w.WriteHeader(status)
	require.NoError(t, json.NewEncoder(w).Encode(map[string]string{
		"error_type":    "ITEM_ERROR",
		"error_code":    code,
		"error_message": msg,
	}))
}

// pageOf returns up to size transactions starting at offset, mimicking Plaid's
// offset pagination.
func pageOf(txns []Transaction, offset, size int) []Transaction {
	if offset >= len(txns) {
		return []Transaction{}
	}
	end := offset + size
	if end > len(txns) {
		end = len(txns)
	}
	return txns[offset:end]
}

// itemData is one linked Item's data, keyed in a test server by its access token.
type itemData struct {
	accounts      []Account
	institutionID string
	txns          []Transaction
}

// newServer routes the Plaid endpoints, selecting an Item by the access token in the
// request body — so several tokens return different accounts (the fan-out). Every
// request must carry the app credentials and the pinned API version. pageSize, when
// > 0, forces transaction pagination. An unknown token gets a Plaid error envelope.
func newServer(t *testing.T, items map[string]itemData, institutions map[string]Institution, pageSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, apiVersion, r.Header.Get("Plaid-Version"))

		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req map[string]any
		require.NoError(t, json.Unmarshal(raw, &req))
		require.NotEmpty(t, req["client_id"], "client_id must be in the body")
		require.NotEmpty(t, req["secret"], "secret must be in the body")

		token, _ := req["access_token"].(string)

		switch r.URL.Path {
		case "/accounts/get":
			it, ok := items[token]
			if !ok {
				writePlaidError(t, w, http.StatusBadRequest, "ITEM_LOGIN_REQUIRED", "the login details of this item have changed")
				return
			}
			writeJSON(t, w, AccountsResponse{
				Accounts: it.accounts,
				Item:     Item{ItemID: "item_" + token, InstitutionID: it.institutionID},
			})
		case "/transactions/get":
			it, ok := items[token]
			if !ok {
				writePlaidError(t, w, http.StatusBadRequest, "ITEM_LOGIN_REQUIRED", "the login details of this item have changed")
				return
			}
			offset := 0
			if opts, ok := req["options"].(map[string]any); ok {
				if o, ok := opts["offset"].(float64); ok {
					offset = int(o)
				}
			}
			size := pageSize
			if size <= 0 {
				size = len(it.txns)
			}
			writeJSON(t, w, transactionsResponse{
				Transactions:      pageOf(it.txns, offset, size),
				TotalTransactions: len(it.txns),
			})
		case "/institutions/get_by_id":
			id, _ := req["institution_id"].(string)
			inst, ok := institutions[id]
			if !ok {
				writePlaidError(t, w, http.StatusBadRequest, "INSTITUTION_NOT_FOUND", "unknown institution")
				return
			}
			writeJSON(t, w, institutionResponse{Institution: inst})
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
	if opts.ClientID == "" {
		opts.ClientID = "test_client"
	}
	if opts.Secret == "" {
		opts.Secret = "test_secret"
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

	t.Run("sends credentials in the body, parses accounts and item", func(t *testing.T) {
		var gotBody map[string]any
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			writeJSON(t, w, AccountsResponse{
				Accounts: []Account{{AccountID: "acc_1", Name: "Checking", Balances: Balances{Current: "100.00", ISOCurrencyCode: "USD"}}},
				Item:     Item{ItemID: "item_1", InstitutionID: "ins_1"},
			})
		}))
		defer srv.Close()

		resp, err := NewClient(srv.URL, "cid", "sec").Accounts(ctx, "access-sandbox-123")
		require.NoError(t, err)
		require.Len(t, resp.Accounts, 1)
		assert.Equal(t, "acc_1", resp.Accounts[0].AccountID)
		assert.Equal(t, "ins_1", resp.Item.InstitutionID)
		assert.Equal(t, "/accounts/get", gotPath)
		assert.Equal(t, "cid", gotBody["client_id"])
		assert.Equal(t, "sec", gotBody["secret"])
		assert.Equal(t, "access-sandbox-123", gotBody["access_token"])
	})

	t.Run("non-200 surfaces the Plaid error envelope", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writePlaidError(t, w, http.StatusBadRequest, "INVALID_ACCESS_TOKEN", "provided access token is invalid")
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "cid", "sec").Accounts(ctx, "bad")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "INVALID_ACCESS_TOKEN")
		assert.Contains(t, err.Error(), "provided access token is invalid")
		assert.Contains(t, err.Error(), "400")
	})
}

func TestTransactionsPagination(t *testing.T) {
	ctx := context.Background()
	txns := []Transaction{
		{TransactionID: "t1", AccountID: "acc_1", Amount: "1.00", Date: "2024-06-10"},
		{TransactionID: "t2", AccountID: "acc_1", Amount: "2.00", Date: "2024-06-09"},
		{TransactionID: "t3", AccountID: "acc_1", Amount: "3.00", Date: "2024-06-08"},
		{TransactionID: "t4", AccountID: "acc_1", Amount: "4.00", Date: "2024-06-07"},
		{TransactionID: "t5", AccountID: "acc_1", Amount: "5.00", Date: "2024-06-06"},
	}
	items := map[string]itemData{"tok": {txns: txns}}

	t.Run("walks pages by offset until total is reached", func(t *testing.T) {
		srv := newServer(t, items, nil, 2) // small pages force pagination
		defer srv.Close()

		got, err := NewClient(srv.URL, "c", "s").Transactions(ctx, "tok", time.Time{})
		require.NoError(t, err)
		require.Len(t, got, 5)
		assert.Equal(t, "t1", got[0].TransactionID)
		assert.Equal(t, "t5", got[4].TransactionID)
	})

	t.Run("sends the lookback window as start_date", func(t *testing.T) {
		var gotStart, gotEnd string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(raw, &req)
			gotStart, _ = req["start_date"].(string)
			gotEnd, _ = req["end_date"].(string)
			writeJSON(t, w, transactionsResponse{Transactions: []Transaction{}, TotalTransactions: 0})
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "c", "s").Transactions(ctx, "tok", time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC))
		require.NoError(t, err)
		assert.Equal(t, "2024-01-15", gotStart)
		assert.NotEmpty(t, gotEnd)
	})

	t.Run("zero since uses the epoch start for a full history", func(t *testing.T) {
		var gotStart string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(raw, &req)
			gotStart, _ = req["start_date"].(string)
			writeJSON(t, w, transactionsResponse{Transactions: []Transaction{}, TotalTransactions: 0})
		}))
		defer srv.Close()

		_, err := NewClient(srv.URL, "c", "s").Transactions(ctx, "tok", time.Time{})
		require.NoError(t, err)
		assert.Equal(t, epochStart, gotStart)
	})
}

func TestInstitution(t *testing.T) {
	ctx := context.Background()
	t.Run("resolves the name and sends country codes", func(t *testing.T) {
		var gotCodes []any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "/institutions/get_by_id", r.URL.Path)
			raw, _ := io.ReadAll(r.Body)
			var req map[string]any
			_ = json.Unmarshal(raw, &req)
			gotCodes, _ = req["country_codes"].([]any)
			writeJSON(t, w, institutionResponse{Institution: Institution{InstitutionID: "ins_1", Name: "Chase"}})
		}))
		defer srv.Close()

		inst, err := NewClient(srv.URL, "c", "s").Institution(ctx, "ins_1", []string{"US", "CA"})
		require.NoError(t, err)
		assert.Equal(t, "Chase", inst.Name)
		assert.Equal(t, []any{"US", "CA"}, gotCodes)
	})

	t.Run("empty institution id is an error without a call", func(t *testing.T) {
		_, err := NewClient("http://unused.invalid", "c", "s").Institution(ctx, "", nil)
		require.Error(t, err)
	})
}

// --- mapping ---

func TestNegateAmount(t *testing.T) {
	assert.Equal(t, "-12.34", negateAmount("12.34"), "outflow positive -> negative")
	assert.Equal(t, "5.00", negateAmount("-5.00"), "inflow negative -> positive")
	assert.Equal(t, "-12.34", negateAmount("+12.34"), "an explicit plus sign is stripped, then negated")
	assert.Equal(t, "0", negateAmount("0"), "zero stays unsigned")
	assert.Equal(t, "0.00", negateAmount("0.00"), "decimal zero stays unsigned")
	assert.Equal(t, "", negateAmount("  "), "blank stays blank")
}

func TestToImportTxn(t *testing.T) {
	got := toImportTxn(Transaction{
		TransactionID: "txn_x",
		Amount:        "12.34", // Plaid: positive = outflow
		Date:          "2024-06-10",
		Name:          "STARBUCKS STORE 123",
		MerchantName:  "Starbucks",
		Pending:       true,
	})
	assert.Equal(t, "plaid:txn_x", got.ExternalID, "id is namespaced")
	assert.Equal(t, "-12.34", got.Amount, "outflow is negated to kasas's convention")
	assert.Equal(t, "STARBUCKS STORE 123", got.Description, "raw name is the description")
	assert.Equal(t, "Starbucks", got.Payee, "cleaned merchant is the payee")
	assert.Equal(t, unix(t, "2024-06-10"), got.Date)
	assert.True(t, got.Pending)

	t.Run("a refund (Plaid-negative) becomes a positive inflow", func(t *testing.T) {
		got := toImportTxn(Transaction{TransactionID: "r", Amount: "-50.00", Date: "2024-06-10"})
		assert.Equal(t, "50.00", got.Amount)
	})
}

func TestToImportAccount(t *testing.T) {
	a := Account{AccountID: "acc_1", Name: "Plaid Checking", Balances: Balances{Current: "110.00", ISOCurrencyCode: "USD"}}
	got := toImportAccount(a, "ins_1", "Chase", 1700000000)
	assert.Equal(t, "plaid:acc_1", got.ExternalID)
	assert.Equal(t, "plaid:org:ins_1", got.Org.ID)
	assert.Equal(t, "Chase", got.Org.Name)
	assert.Equal(t, "Plaid Checking", got.Name)
	assert.Equal(t, "USD", got.Currency)
	assert.Equal(t, "110.00", got.Balance, "balance passes through verbatim (not negated)")
	assert.Equal(t, int64(1700000000), got.BalanceDate)

	t.Run("empty balance leaves balance unknown", func(t *testing.T) {
		got := toImportAccount(Account{AccountID: "a", Balances: Balances{ISOCurrencyCode: "USD"}}, "ins_1", "Chase", 1700000000)
		assert.Empty(t, got.Balance)
		assert.Zero(t, got.BalanceDate)
	})
}

func TestToImportOrg(t *testing.T) {
	t.Run("uses the institution id and resolved name", func(t *testing.T) {
		org := toImportOrg("ins_109508", "Chase")
		assert.Equal(t, "plaid:org:ins_109508", org.ID)
		assert.Equal(t, "Chase", org.Name)
	})
	t.Run("name falls back to the id when unresolved", func(t *testing.T) {
		org := toImportOrg("ins_109508", "")
		assert.Equal(t, "plaid:org:ins_109508", org.ID)
		assert.Equal(t, "ins_109508", org.Name)
	})
	t.Run("id falls back to a slug of the name", func(t *testing.T) {
		org := toImportOrg("", "My Bank")
		assert.Equal(t, "plaid:org:my-bank", org.ID)
		assert.Equal(t, "My Bank", org.Name)
	})
	t.Run("id falls back to unknown when nothing is known", func(t *testing.T) {
		org := toImportOrg("", "")
		assert.Equal(t, "plaid:org:unknown", org.ID)
	})
}

func TestAccountName(t *testing.T) {
	assert.Equal(t, "Checking", accountName(Account{Name: "Checking", Subtype: "checking"}))
	assert.Equal(t, "Plaid Gold Checking", accountName(Account{OfficialName: "Plaid Gold Checking", Subtype: "checking"}))
	assert.Equal(t, "checking", accountName(Account{Subtype: "checking", Type: "depository"}))
	assert.Equal(t, "depository", accountName(Account{Type: "depository"}))
	assert.Equal(t, "Account", accountName(Account{}))
}

func TestCurrencyOf(t *testing.T) {
	assert.Equal(t, "USD", currencyOf("USD", ""))
	assert.Equal(t, "BTC", currencyOf("", "BTC"), "falls back to the unofficial code")
	assert.Empty(t, currencyOf("", ""))
}

func TestTransactionDate(t *testing.T) {
	assert.Equal(t, unix(t, "2024-06-10"), transactionDate("2024-06-10"))
	assert.Zero(t, transactionDate("not-a-date"))
}

// --- environment ---

func TestBaseURLFor(t *testing.T) {
	cases := map[string]string{
		"":            "https://sandbox.plaid.com",
		"sandbox":     "https://sandbox.plaid.com",
		"SANDBOX":     "https://sandbox.plaid.com",
		"development": "https://development.plaid.com",
		"production":  "https://production.plaid.com",
	}
	for env, want := range cases {
		got, err := baseURLFor(env)
		require.NoError(t, err, env)
		assert.Equal(t, want, got, env)
	}

	_, err := baseURLFor("staging")
	require.Error(t, err, "an unknown environment is rejected")
}

func TestNewRejectsUnknownEnvironment(t *testing.T) {
	_, err := New(Options{Environment: "staging"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown plaid environment")
}

// --- token helpers ---

func TestSplitTokens(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, splitTokens("a\nb\nc"))
	assert.Equal(t, []string{"a", "b"}, splitTokens(" a , b "), "comma or newline, trimmed")
	assert.Equal(t, []string{"a"}, splitTokens("a\n\na\n"), "dedupes and drops empties")
	assert.Empty(t, splitTokens("   "))
}

func TestNormalizeCountryCodes(t *testing.T) {
	assert.Equal(t, []string{"US", "CA"}, normalizeCountryCodes([]string{"us", " ca ", "US"}), "uppercased, trimmed, deduped")
	assert.Empty(t, normalizeCountryCodes([]string{"", "  "}))
}

func TestMaskAndTokenID(t *testing.T) {
	assert.Equal(t, "••••cd34", maskToken("access-sandbox-abcd34"))
	assert.Equal(t, "••••", maskToken("abc"))
	id := tokenID("access-sandbox-abc")
	assert.Len(t, id, 12)
	assert.Equal(t, id, tokenID("access-sandbox-abc"), "stable")
	assert.NotEqual(t, id, tokenID("access-sandbox-xyz"), "distinct per token")
}

// --- source: fetch (single + fan-out) ---

func TestSourceFetch(t *testing.T) {
	items := map[string]itemData{
		"tok": {
			institutionID: "ins_1",
			accounts: []Account{
				{AccountID: "acc_1", Name: "Plaid Checking", Balances: Balances{Current: "110.00", ISOCurrencyCode: "USD"}},
			},
			txns: []Transaction{
				{TransactionID: "t1", AccountID: "acc_1", Amount: "12.34", Date: "2024-06-10", Name: "Coffee", MerchantName: "Starbucks", Pending: false},
				{TransactionID: "t2", AccountID: "acc_1", Amount: "-20.00", Date: "2024-06-05", Name: "Refund", Pending: true},
			},
		},
	}
	institutions := map[string]Institution{"ins_1": {InstitutionID: "ins_1", Name: "Chase"}}
	srv := newServer(t, items, institutions, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"tok"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)

	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	a := batch.Accounts[0]
	assert.Equal(t, "plaid:acc_1", a.ExternalID)
	assert.Equal(t, "plaid:org:ins_1", a.Org.ID)
	assert.Equal(t, "Chase", a.Org.Name, "institution name is resolved")
	assert.Equal(t, "Plaid Checking", a.Name)
	assert.Equal(t, "110.00", a.Balance)
	assert.Positive(t, a.BalanceDate)
	require.Len(t, a.Transactions, 2)
	assert.Equal(t, "plaid:t1", a.Transactions[0].ExternalID)
	assert.Equal(t, "-12.34", a.Transactions[0].Amount, "outflow negated")
	assert.Equal(t, "Starbucks", a.Transactions[0].Payee)
	assert.False(t, a.Transactions[0].Pending)
	assert.Equal(t, "20.00", a.Transactions[1].Amount, "inflow negated to positive")
	assert.True(t, a.Transactions[1].Pending)
}

func TestSourceFetchFanOut(t *testing.T) {
	items := map[string]itemData{
		"chase_tok": {
			institutionID: "ins_chase",
			accounts:      []Account{{AccountID: "acc_1", Name: "Chase Checking", Balances: Balances{Current: "1000.00", ISOCurrencyCode: "USD"}}},
			txns:          []Transaction{{TransactionID: "t1", AccountID: "acc_1", Amount: "1.00", Date: "2024-06-10"}},
		},
		"amex_tok": {
			institutionID: "ins_amex",
			accounts:      []Account{{AccountID: "acc_2", Name: "Amex Card", Balances: Balances{Current: "-200.00", ISOCurrencyCode: "USD"}}},
			txns:          []Transaction{{TransactionID: "t2", AccountID: "acc_2", Amount: "50.00", Date: "2024-06-09"}},
		},
	}
	institutions := map[string]Institution{
		"ins_chase": {Name: "Chase"},
		"ins_amex":  {Name: "American Express"},
	}
	srv := newServer(t, items, institutions, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"chase_tok", "amex_tok"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 2, "accounts from both Items are merged")
	assert.ElementsMatch(t, []string{"plaid:acc_1", "plaid:acc_2"}, acctIDs(batch.Accounts))
}

func TestSourceFetchToleratesOneBadItem(t *testing.T) {
	items := map[string]itemData{
		"good_tok": {
			institutionID: "ins_1",
			accounts:      []Account{{AccountID: "acc_1", Name: "Checking", Balances: Balances{ISOCurrencyCode: "USD"}}},
			txns:          []Transaction{{TransactionID: "t1", AccountID: "acc_1", Amount: "1.00", Date: "2024-06-10"}},
		},
		// "bad_tok" is absent -> ITEM_LOGIN_REQUIRED.
	}
	srv := newServer(t, items, nil, 100)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"good_tok", "bad_tok"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err, "a single failing Item must not fail the whole sync")
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "plaid:acc_1", batch.Accounts[0].ExternalID)
}

func TestSourceFetchAllItemsFail(t *testing.T) {
	srv := newServer(t, map[string]itemData{}, nil, 100) // every token is unknown
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"bad_1", "bad_2"}})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
}

func TestSourceFetchTransactionsFailureIsNonFatal(t *testing.T) {
	// A server that serves accounts but errors on /transactions/get.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts/get":
			writeJSON(t, w, AccountsResponse{
				Accounts: []Account{{AccountID: "acc_1", Name: "Checking", Balances: Balances{Current: "5.00", ISOCurrencyCode: "USD"}}},
				Item:     Item{InstitutionID: "ins_1"},
			})
		case "/transactions/get":
			writePlaidError(t, w, http.StatusBadRequest, "PRODUCT_NOT_READY", "the requested product is not yet ready")
		case "/institutions/get_by_id":
			writeJSON(t, w, institutionResponse{Institution: Institution{Name: "Chase"}})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, AccessTokens: []string{"tok"}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err, "a transactions failure imports accounts only, not a hard error")
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "5.00", batch.Accounts[0].Balance)
	assert.Empty(t, batch.Accounts[0].Transactions)
}

func TestSourceFetchWithoutCredential(t *testing.T) {
	s, _ := newSource(t, Options{BaseURL: "http://unused.invalid"})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Plaid access token")
}

func TestSourceFetchWithoutAppCredentials(t *testing.T) {
	// Construct directly so the app credentials stay empty (the helper defaults them).
	s, err := New(Options{
		BaseURL:      "http://unused.invalid",
		Secrets:      vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		AccessTokens: []string{"tok"},
	})
	require.NoError(t, err)
	_, err = s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "client_id and secret")
}

// --- source: credentials (add / list / remove / resolve) ---

func TestSetCredentialAppends(t *testing.T) {
	ctx := context.Background()
	s, store := newSource(t, Options{})

	configured, err := s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, configured)

	require.NoError(t, s.SetCredential(ctx, "  tok_a  "))
	require.NoError(t, s.SetCredential(ctx, "tok_b"))

	stored, err := store.SecretValue(ctx, accessTokenKey)
	require.NoError(t, err)
	assert.Equal(t, "tok_a\ntok_b", stored, "tokens accumulate (trimmed), not replaced")

	configured, err = s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.True(t, configured)

	require.NoError(t, s.SetCredential(ctx, "tok_a"), "re-adding is a no-op")
	stored, _ = store.SecretValue(ctx, accessTokenKey)
	assert.Equal(t, "tok_a\ntok_b", stored)

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

// --- registration ---

func TestRegisteredAndConstructable(t *testing.T) {
	assert.True(t, source.Registered(SourceType), "importing the package registers the source")

	s, err := source.New(SourceType, source.Env{
		Secrets: vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		Options: map[string]string{
			"client_id":     "cid",
			"secret":        "sec",
			"environment":   "sandbox",
			"country_codes": "US,CA",
			"access_tokens": "x\ny",
		},
	})
	require.NoError(t, err)

	_, isPuller := s.(source.Puller)
	assert.True(t, isPuller, "the plaid source is a Puller")
	_, isCred := s.(source.Credentialed)
	assert.True(t, isCred, "the plaid source is Credentialed")
	_, isMulti := s.(source.MultiCredentialed)
	assert.True(t, isMulti, "the plaid source is MultiCredentialed")
	assert.Equal(t, SourceType, s.Descriptor().Type)
}
