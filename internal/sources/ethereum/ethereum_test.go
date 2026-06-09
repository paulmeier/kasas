package ethereum

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

const (
	addr1 = "0x1111111111111111111111111111111111111111"
	addr2 = "0x2222222222222222222222222222222222222222"
	other = "0x9999999999999999999999999999999999999999"
)

// wei amounts used across tests.
const (
	oneEth     = "1000000000000000000" // 1e18
	halfEth    = "500000000000000000"  // 5e17
	fifthEth   = "200000000000000000"  // 2e17
	gasUsed    = "21000"
	gasPrice   = "20000000000" // 20 gwei -> fee 420000000000000 wei = 0.00042 ETH
	gasFeeText = "0.00042"
)

// --- helpers ---

func writeEnvelope(t *testing.T, w http.ResponseWriter, status, message string, result any) {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
		"status": status, "message": message, "result": json.RawMessage(raw),
	}))
}

type ethData struct {
	balanceWei  string
	blockByTime string
	txns        []Transaction
}

// newServer routes the Etherscan endpoints the client uses, selecting an address from
// the query. It asserts the chain id and API key are present. capture, when non-nil, is
// called with every request's query (for asserting parameters like startblock).
func newServer(t *testing.T, data map[string]ethData, capture func(url.Values)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		assert.NotEmpty(t, q.Get("chainid"), "chainid must be sent")
		assert.NotEmpty(t, q.Get("apikey"), "apikey must be sent")
		if capture != nil {
			capture(q)
		}

		switch q.Get("module") + "." + q.Get("action") {
		case "account.txlist":
			d := data[strings.ToLower(q.Get("address"))]
			if len(d.txns) == 0 {
				writeEnvelope(t, w, "0", "No transactions found", []Transaction{})
				return
			}
			writeEnvelope(t, w, "1", "OK", d.txns)
		case "account.balance":
			d := data[strings.ToLower(q.Get("address"))]
			writeEnvelope(t, w, "1", "OK", d.balanceWei)
		case "block.getblocknobytime":
			// Any address-agnostic data entry can carry the mapped block.
			block := "0"
			for _, d := range data {
				if d.blockByTime != "" {
					block = d.blockByTime
					break
				}
			}
			writeEnvelope(t, w, "1", "OK", block)
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
	if opts.APIKey == "" {
		opts.APIKey = "test_key"
	}
	s, err := New(opts)
	require.NoError(t, err)
	return s, opts.Secrets
}

func acctIDs(accts []source.ImportAccount) []string {
	ids := make([]string, len(accts))
	for i, a := range accts {
		ids[i] = a.ExternalID
	}
	return ids
}

// --- client ---

func TestClientTransactionsAndKey(t *testing.T) {
	ctx := context.Background()
	var gotQuery url.Values
	data := map[string]ethData{addr1: {txns: []Transaction{{Hash: "0xabc", From: other, To: addr1, Value: oneEth, TimeStamp: "1700000000"}}}}
	srv := newServer(t, data, func(q url.Values) { gotQuery = q })
	defer srv.Close()

	got, err := NewClient(srv.URL, 8453, "secret_key").Transactions(ctx, addr1, 100)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "0xabc", got[0].Hash)
	assert.Equal(t, "8453", gotQuery.Get("chainid"), "chain id is forwarded")
	assert.Equal(t, "secret_key", gotQuery.Get("apikey"))
	assert.Equal(t, "100", gotQuery.Get("startblock"))
}

func TestClientTransactionsEmpty(t *testing.T) {
	srv := newServer(t, map[string]ethData{addr1: {}}, nil) // no txns -> "No transactions found"
	defer srv.Close()

	got, err := NewClient(srv.URL, 1, "k").Transactions(context.Background(), addr1, 0)
	require.NoError(t, err, "an empty history is not an error")
	assert.Empty(t, got)
}

func TestClientBalance(t *testing.T) {
	srv := newServer(t, map[string]ethData{addr1: {balanceWei: oneEth}}, nil)
	defer srv.Close()

	bal, err := NewClient(srv.URL, 1, "k").Balance(context.Background(), addr1)
	require.NoError(t, err)
	assert.Equal(t, oneEth, bal.String())
}

func TestClientAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(t, w, "0", "NOTOK", "Invalid API Key")
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, 1, "bad").Balance(context.Background(), addr1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid API Key")
}

func TestClientScrubsKeyFromTransportError(t *testing.T) {
	// A client pointed at an unroutable host surfaces a transport error embedding the
	// URL; the API key must be redacted from it.
	_, err := NewClient("http://127.0.0.1:0", 1, "super_secret_key").Balance(context.Background(), addr1)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super_secret_key")
}

// --- mapping ---

func TestNetForAddress(t *testing.T) {
	t.Run("received credits the value", func(t *testing.T) {
		tx := Transaction{From: other, To: addr1, Value: halfEth}
		assert.Equal(t, halfEth, netForAddress(tx, addr1).String())
	})
	t.Run("sent debits value plus gas", func(t *testing.T) {
		tx := Transaction{From: addr1, To: other, Value: fifthEth, GasUsed: gasUsed, GasPrice: gasPrice}
		// -(2e17) - 420000000000000 = -200420000000000000
		assert.Equal(t, "-200420000000000000", netForAddress(tx, addr1).String())
	})
	t.Run("self-send nets to minus gas", func(t *testing.T) {
		tx := Transaction{From: addr1, To: addr1, Value: halfEth, GasUsed: gasUsed, GasPrice: gasPrice}
		assert.Equal(t, "-420000000000000", netForAddress(tx, addr1).String(), "value cancels, gas remains")
	})
	t.Run("failed tx moves no value but still costs the sender gas", func(t *testing.T) {
		tx := Transaction{From: addr1, To: other, Value: oneEth, GasUsed: gasUsed, GasPrice: gasPrice, IsError: "1"}
		assert.Equal(t, "-420000000000000", netForAddress(tx, addr1).String())
	})
	t.Run("failed tx where we are the recipient nets to zero", func(t *testing.T) {
		tx := Transaction{From: other, To: addr1, Value: oneEth, IsError: "1"}
		assert.Equal(t, "0", netForAddress(tx, addr1).String())
	})
}

func TestToImportTxn(t *testing.T) {
	t.Run("received", func(t *testing.T) {
		got := toImportTxn(addr1, Transaction{Hash: "0xdead", From: other, To: addr1, Value: halfEth, TimeStamp: "1700000000"})
		assert.Equal(t, "ethereum:"+addr1+":0xdead", got.ExternalID)
		assert.Equal(t, "0.5", got.Amount)
		assert.Equal(t, "Received ETH", got.Description)
		assert.Equal(t, other, got.Payee, "counterparty is the sender")
		assert.Equal(t, int64(1700000000), got.Date)
		assert.False(t, got.Pending)
	})
	t.Run("sent", func(t *testing.T) {
		got := toImportTxn(addr1, Transaction{Hash: "0xbeef", From: addr1, To: other, Value: fifthEth, GasUsed: gasUsed, GasPrice: gasPrice, TimeStamp: "1700000000"})
		assert.Equal(t, "-0.20042", got.Amount, "value plus gas")
		assert.Equal(t, "Sent ETH", got.Description)
		assert.Equal(t, other, got.Payee, "counterparty is the recipient")
	})
}

func TestToImportAccount(t *testing.T) {
	bal, _ := new(big.Int).SetString(oneEth, 10)
	got := toImportAccount(addr1, bal, 1700000000)
	assert.Equal(t, "ethereum:"+addr1, got.ExternalID)
	assert.Equal(t, "ethereum:org:ethereum", got.Org.ID)
	assert.Equal(t, "Ethereum", got.Org.Name)
	assert.Equal(t, "ETH", got.Currency)
	assert.Equal(t, "1", got.Balance)
	assert.Equal(t, int64(1700000000), got.BalanceDate)
	assert.Equal(t, labelETH(addr1), got.Name)

	t.Run("nil balance leaves it unknown", func(t *testing.T) {
		got := toImportAccount(addr1, nil, 1700000000)
		assert.Empty(t, got.Balance)
		assert.Zero(t, got.BalanceDate)
	})
}

// --- address validation ---

func TestNormalizeETH(t *testing.T) {
	t.Run("lowercases a checksummed address", func(t *testing.T) {
		got, err := normalizeETH("  0xAbC0000000000000000000000000000000000123  ")
		require.NoError(t, err)
		assert.Equal(t, "0xabc0000000000000000000000000000000000123", got)
	})
	t.Run("rejects junk", func(t *testing.T) {
		for _, bad := range []string{"", "0x123", "abc", addr1 + "00", "0xZZZ1111111111111111111111111111111111111"} {
			_, err := normalizeETH(bad)
			assert.Error(t, err, bad)
		}
	})
}

// --- source: fetch ---

func TestSourceFetch(t *testing.T) {
	data := map[string]ethData{
		addr1: {
			balanceWei: oneEth,
			txns: []Transaction{
				{Hash: "0xrecv", From: other, To: addr1, Value: halfEth, TimeStamp: "1700000000"},
				{Hash: "0xsent", From: addr1, To: other, Value: fifthEth, GasUsed: gasUsed, GasPrice: gasPrice, TimeStamp: "1699990000"},
			},
		},
	}
	srv := newServer(t, data, nil)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addr1}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)

	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	a := batch.Accounts[0]
	assert.Equal(t, "ethereum:"+addr1, a.ExternalID)
	assert.Equal(t, "1", a.Balance)
	require.Len(t, a.Transactions, 2)
	assert.Equal(t, "ethereum:"+addr1+":0xrecv", a.Transactions[0].ExternalID)
	assert.Equal(t, "0.5", a.Transactions[0].Amount)
	assert.Equal(t, "-0.20042", a.Transactions[1].Amount)
}

func TestSourceFetchMapsSinceToStartBlock(t *testing.T) {
	var startBlocks []string
	data := map[string]ethData{addr1: {balanceWei: oneEth, blockByTime: "18000000", txns: []Transaction{{Hash: "0x1", From: other, To: addr1, Value: oneEth, TimeStamp: "1700000000"}}}}
	srv := newServer(t, data, func(q url.Values) {
		if q.Get("action") == "txlist" {
			startBlocks = append(startBlocks, q.Get("startblock"))
		}
	})
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addr1}})
	_, err := s.Fetch(context.Background(), time.Unix(1690000000, 0), "")
	require.NoError(t, err)
	require.NotEmpty(t, startBlocks)
	assert.Equal(t, "18000000", startBlocks[0], "the lookback is mapped to a start block")
}

func TestSourceFetchFanOut(t *testing.T) {
	data := map[string]ethData{
		addr1: {balanceWei: oneEth, txns: []Transaction{{Hash: "0xa", From: other, To: addr1, Value: halfEth, TimeStamp: "1700000000"}}},
		addr2: {balanceWei: fifthEth, txns: []Transaction{{Hash: "0xb", From: addr2, To: other, Value: fifthEth, GasUsed: gasUsed, GasPrice: gasPrice, TimeStamp: "1700000000"}}},
	}
	srv := newServer(t, data, nil)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addr1, addr2}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 2)
	assert.ElementsMatch(t, []string{"ethereum:" + addr1, "ethereum:" + addr2}, acctIDs(batch.Accounts))
}

func TestSourceFetchWithoutAddress(t *testing.T) {
	s, _ := newSource(t, Options{BaseURL: "http://unused.invalid"})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Ethereum address")
}

func TestSourceFetchWithoutAPIKey(t *testing.T) {
	s, err := New(Options{
		BaseURL:   "http://unused.invalid",
		Secrets:   vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		Addresses: []string{addr1},
	})
	require.NoError(t, err)
	_, err = s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "API key")
}

// --- source: credentials (addresses) ---

func TestCredentialAddListRemove(t *testing.T) {
	ctx := context.Background()
	s, _ := newSource(t, Options{Addresses: []string{addr1}})

	require.NoError(t, s.SetCredential(ctx, strings.ToUpper("0xABC0000000000000000000000000000000000123")))
	require.Error(t, s.SetCredential(ctx, "not-an-address"))

	entries, err := s.ListCredentials(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	var runtimeID string
	for _, e := range entries {
		if e.Removable {
			runtimeID = e.ID
		}
	}
	require.NotEmpty(t, runtimeID)
	require.NoError(t, s.RemoveCredential(ctx, runtimeID))
	entries, _ = s.ListCredentials(ctx)
	assert.Len(t, entries, 1)
}

// --- registration ---

func TestRegisteredAndConstructable(t *testing.T) {
	assert.True(t, source.Registered(SourceType))

	s, err := source.New(SourceType, source.Env{
		Secrets: vault.NewFileStore(filepath.Join(t.TempDir(), "s.json")),
		Options: map[string]string{"api_key": "k", "chain_id": "1", "addresses": addr1},
	})
	require.NoError(t, err)

	_, isPuller := s.(source.Puller)
	assert.True(t, isPuller)
	_, isMulti := s.(source.MultiCredentialed)
	assert.True(t, isMulti)
	assert.Equal(t, SourceType, s.Descriptor().Type)
}
