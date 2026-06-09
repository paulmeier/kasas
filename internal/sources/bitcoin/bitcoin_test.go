package bitcoin

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

// Valid, well-known addresses used across the source-level tests (they pass the
// structural validation that book.Resolve applies).
const (
	addrBech32 = "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"
	addrLegacy = "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"
)

// --- helpers ---

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

// addrData is one address's canned chain data, keyed in the test server by address.
type addrData struct {
	fundedSat int64
	spentSat  int64
	mempool   []Tx
	confirmed []Tx // newest-first, as Esplora returns them
}

// newServer routes the Esplora endpoints the client uses, selecting an address from
// the path. pageSize, when > 0, forces chain pagination. An unknown address 404s.
func newServer(t *testing.T, data map[string]addrData, pageSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		rest := strings.TrimPrefix(r.URL.Path, "/address/")
		parts := strings.Split(rest, "/")
		addr := parts[0]
		d, ok := data[addr]
		if !ok {
			http.Error(w, "Invalid Bitcoin address", http.StatusBadRequest)
			return
		}

		switch {
		case len(parts) == 1: // /address/{a} -> stats
			writeJSON(t, w, map[string]any{
				"chain_stats": map[string]int64{"funded_txo_sum": d.fundedSat, "spent_txo_sum": d.spentSat},
			})
		case len(parts) >= 3 && parts[1] == "txs" && parts[2] == "mempool":
			writeJSON(t, w, d.mempool)
		case len(parts) >= 3 && parts[1] == "txs" && parts[2] == "chain":
			fromID := ""
			if len(parts) >= 4 {
				fromID = parts[3]
			}
			writeJSON(t, w, confirmedAfter(d.confirmed, fromID, pageSize))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

// confirmedAfter returns up to size transactions starting after fromID (or from the
// start when empty), mimicking Esplora's backward chain pagination.
func confirmedAfter(all []Tx, fromID string, size int) []Tx {
	start := 0
	if fromID != "" {
		for i, tx := range all {
			if tx.TxID == fromID {
				start = i + 1
				break
			}
		}
	}
	if size <= 0 {
		size = len(all)
	}
	if start >= len(all) {
		return []Tx{}
	}
	end := start + size
	if end > len(all) {
		end = len(all)
	}
	return all[start:end]
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

func acctIDs(accts []source.ImportAccount) []string {
	ids := make([]string, len(accts))
	for i, a := range accts {
		ids[i] = a.ExternalID
	}
	return ids
}

// --- client ---

func TestBalance(t *testing.T) {
	ctx := context.Background()
	data := map[string]addrData{"a": {fundedSat: 500000, spentSat: 150000}}
	srv := newServer(t, data, 0)
	defer srv.Close()

	bal, err := NewClient(srv.URL).Balance(ctx, "a")
	require.NoError(t, err)
	assert.Equal(t, int64(350000), bal, "confirmed balance is funded minus spent")
}

func TestBalanceError(t *testing.T) {
	srv := newServer(t, map[string]addrData{}, 0) // any address is unknown -> 400
	defer srv.Close()

	_, err := NewClient(srv.URL).Balance(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Invalid Bitcoin address")
}

func TestTransactionsMempoolThenChain(t *testing.T) {
	ctx := context.Background()
	data := map[string]addrData{
		"a": {
			mempool: []Tx{{TxID: "pending", Status: TxStatus{Confirmed: false}}},
			confirmed: []Tx{
				{TxID: "c1", Status: TxStatus{Confirmed: true, BlockTime: 1700002000}},
				{TxID: "c2", Status: TxStatus{Confirmed: true, BlockTime: 1700001000}},
			},
		},
	}
	srv := newServer(t, data, 0)
	defer srv.Close()

	got, err := NewClient(srv.URL).Transactions(ctx, "a", time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "pending", got[0].TxID, "mempool transactions come first")
	assert.Equal(t, "c1", got[1].TxID)
	assert.Equal(t, "c2", got[2].TxID)
}

func TestTransactionsPaginatesChain(t *testing.T) {
	ctx := context.Background()
	confirmed := []Tx{
		{TxID: "c1", Status: TxStatus{Confirmed: true, BlockTime: 1700005000}},
		{TxID: "c2", Status: TxStatus{Confirmed: true, BlockTime: 1700004000}},
		{TxID: "c3", Status: TxStatus{Confirmed: true, BlockTime: 1700003000}},
		{TxID: "c4", Status: TxStatus{Confirmed: true, BlockTime: 1700002000}},
		{TxID: "c5", Status: TxStatus{Confirmed: true, BlockTime: 1700001000}},
	}
	srv := newServer(t, map[string]addrData{"a": {confirmed: confirmed}}, 2) // small pages
	defer srv.Close()

	got, err := NewClient(srv.URL).Transactions(ctx, "a", time.Time{})
	require.NoError(t, err)
	require.Len(t, got, 5, "walks every chain page")
	assert.Equal(t, "c5", got[4].TxID)
}

func TestTransactionsStopsAtSince(t *testing.T) {
	ctx := context.Background()
	confirmed := []Tx{
		{TxID: "new", Status: TxStatus{Confirmed: true, BlockTime: 1700005000}},
		{TxID: "old", Status: TxStatus{Confirmed: true, BlockTime: 1600000000}},
	}
	srv := newServer(t, map[string]addrData{"a": {confirmed: confirmed}}, 0)
	defer srv.Close()

	since := time.Unix(1700000000, 0)
	got, err := NewClient(srv.URL).Transactions(ctx, "a", since)
	require.NoError(t, err)
	require.Len(t, got, 1, "transactions before the window are dropped")
	assert.Equal(t, "new", got[0].TxID)
}

// --- mapping ---

func TestNetForAddress(t *testing.T) {
	addr := "me"
	t.Run("received credits the output value", func(t *testing.T) {
		tx := Tx{Vout: []Vout{{Address: addr, Value: 150000}, {Address: "other", Value: 49000}}}
		assert.Equal(t, int64(150000), netForAddress(tx, addr))
	})
	t.Run("sent debits the spent input value", func(t *testing.T) {
		tx := Tx{
			Vin:  []Vin{{Prevout: Vout{Address: addr, Value: 150000}}},
			Vout: []Vout{{Address: "merchant", Value: 140000}},
		}
		assert.Equal(t, int64(-150000), netForAddress(tx, addr))
	})
	t.Run("net of inputs and change outputs", func(t *testing.T) {
		tx := Tx{
			Vin:  []Vin{{Prevout: Vout{Address: addr, Value: 200000}}},
			Vout: []Vout{{Address: "merchant", Value: 120000}, {Address: addr, Value: 70000}},
		}
		assert.Equal(t, int64(-130000), netForAddress(tx, addr), "spent 200000, got 70000 change back")
	})
}

func TestToImportTxn(t *testing.T) {
	tx := Tx{
		TxID:   "deadbeef",
		Vout:   []Vout{{Address: "me", Value: 150000}},
		Status: TxStatus{Confirmed: true, BlockTime: 1700000000},
	}
	got := toImportTxn("me", tx, 9999)
	assert.Equal(t, "bitcoin:me:deadbeef", got.ExternalID, "id is namespaced by source and address")
	assert.Equal(t, "0.0015", got.Amount)
	assert.Equal(t, "Received", got.Description)
	assert.Equal(t, int64(1700000000), got.Date)
	assert.False(t, got.Pending)

	t.Run("unconfirmed is pending and dated at fetch time", func(t *testing.T) {
		got := toImportTxn("me", Tx{TxID: "p", Vin: []Vin{{Prevout: Vout{Address: "me", Value: 1000}}}}, 9999)
		assert.True(t, got.Pending)
		assert.Equal(t, int64(9999), got.Date)
		assert.Equal(t, "Sent", got.Description)
		assert.Equal(t, "-0.00001", got.Amount)
	})
}

func TestToImportAccount(t *testing.T) {
	got := toImportAccount(addrLegacy, 350000, true, 1700000000)
	assert.Equal(t, "bitcoin:"+addrLegacy, got.ExternalID)
	assert.Equal(t, "bitcoin:org:bitcoin", got.Org.ID)
	assert.Equal(t, "Bitcoin", got.Org.Name)
	assert.Equal(t, "BTC", got.Currency)
	assert.Equal(t, "0.0035", got.Balance)
	assert.Equal(t, int64(1700000000), got.BalanceDate)
	assert.Equal(t, labelBTC(addrLegacy), got.Name)

	t.Run("no balance leaves it unknown", func(t *testing.T) {
		got := toImportAccount(addrLegacy, 0, false, 1700000000)
		assert.Empty(t, got.Balance)
		assert.Zero(t, got.BalanceDate)
	})
}

// --- address validation ---

func TestNormalizeBTC(t *testing.T) {
	t.Run("accepts and canonicalizes", func(t *testing.T) {
		got, err := normalizeBTC("  " + strings.ToUpper(addrBech32) + "  ")
		require.NoError(t, err)
		assert.Equal(t, addrBech32, got, "bech32 is trimmed and lowercased")

		got, err = normalizeBTC(addrLegacy)
		require.NoError(t, err)
		assert.Equal(t, addrLegacy, got, "legacy case is preserved")
	})
	t.Run("rejects junk", func(t *testing.T) {
		for _, bad := range []string{"", "   ", "hello", "bc1!", "1" + strings.Repeat("0", 5), "0xabc"} {
			_, err := normalizeBTC(bad)
			assert.Error(t, err, bad)
		}
	})
}

// --- source: fetch ---

func TestSourceFetch(t *testing.T) {
	data := map[string]addrData{
		addrLegacy: {
			fundedSat: 500000,
			spentSat:  150000,
			confirmed: []Tx{
				{TxID: "recv", Vout: []Vout{{Address: addrLegacy, Value: 150000}}, Status: TxStatus{Confirmed: true, BlockTime: 1700000000}},
				{TxID: "sent", Vin: []Vin{{Prevout: Vout{Address: addrLegacy, Value: 150000}}}, Vout: []Vout{{Address: "merchant", Value: 140000}}, Status: TxStatus{Confirmed: true, BlockTime: 1699990000}},
			},
		},
	}
	srv := newServer(t, data, 0)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addrLegacy}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)

	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	a := batch.Accounts[0]
	assert.Equal(t, "bitcoin:"+addrLegacy, a.ExternalID)
	assert.Equal(t, "0.0035", a.Balance)
	require.Len(t, a.Transactions, 2)
	assert.Equal(t, "bitcoin:"+addrLegacy+":recv", a.Transactions[0].ExternalID)
	assert.Equal(t, "0.0015", a.Transactions[0].Amount)
	assert.Equal(t, "-0.0015", a.Transactions[1].Amount)
}

func TestSourceFetchFanOutAndSharedTx(t *testing.T) {
	// One transaction sends from addrLegacy to addrBech32; both are watched, so each
	// records its own perspective under a per-address id.
	shared := Tx{
		TxID:   "shared",
		Vin:    []Vin{{Prevout: Vout{Address: addrLegacy, Value: 100000}}},
		Vout:   []Vout{{Address: addrBech32, Value: 95000}},
		Status: TxStatus{Confirmed: true, BlockTime: 1700000000},
	}
	data := map[string]addrData{
		addrLegacy: {confirmed: []Tx{shared}},
		addrBech32: {confirmed: []Tx{shared}},
	}
	srv := newServer(t, data, 0)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addrLegacy, addrBech32}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 2)
	assert.ElementsMatch(t, []string{"bitcoin:" + addrLegacy, "bitcoin:" + addrBech32}, acctIDs(batch.Accounts))

	byAcct := map[string]source.ImportTxn{}
	for _, acct := range batch.Accounts {
		require.Len(t, acct.Transactions, 1)
		byAcct[acct.ExternalID] = acct.Transactions[0]
	}
	assert.Equal(t, "bitcoin:"+addrLegacy+":shared", byAcct["bitcoin:"+addrLegacy].ExternalID)
	assert.Equal(t, "-0.001", byAcct["bitcoin:"+addrLegacy].Amount, "sender sees an outflow")
	assert.Equal(t, "0.00095", byAcct["bitcoin:"+addrBech32].Amount, "recipient sees an inflow")
}

func TestSourceFetchToleratesOneBadAddress(t *testing.T) {
	data := map[string]addrData{
		addrLegacy: {confirmed: []Tx{{TxID: "c1", Vout: []Vout{{Address: addrLegacy, Value: 1000}}, Status: TxStatus{Confirmed: true, BlockTime: 1700000000}}}},
		// addrBech32 is absent -> the server 400s for it.
	}
	srv := newServer(t, data, 0)
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addrLegacy, addrBech32}})
	batch, err := s.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err, "one failing address must not fail the whole sync")
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "bitcoin:"+addrLegacy, batch.Accounts[0].ExternalID)
}

func TestSourceFetchAllAddressesFail(t *testing.T) {
	srv := newServer(t, map[string]addrData{}, 0) // every address is unknown
	defer srv.Close()

	s, _ := newSource(t, Options{BaseURL: srv.URL, Addresses: []string{addrLegacy}})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
}

func TestSourceFetchWithoutAddress(t *testing.T) {
	s, _ := newSource(t, Options{BaseURL: "http://unused.invalid"})
	_, err := s.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no Bitcoin address")
}

// --- source: credentials (addresses) ---

func TestCredentialAddListRemove(t *testing.T) {
	ctx := context.Background()
	s, _ := newSource(t, Options{Addresses: []string{addrLegacy}})

	ok, err := s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.True(t, ok, "a config address is enough to be configured")

	require.NoError(t, s.SetCredential(ctx, addrBech32))
	require.Error(t, s.SetCredential(ctx, "not-an-address"), "an invalid address is rejected")

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
		Options: map[string]string{"addresses": addrLegacy + "\n" + addrBech32},
	})
	require.NoError(t, err)

	_, isPuller := s.(source.Puller)
	assert.True(t, isPuller)
	_, isMulti := s.(source.MultiCredentialed)
	assert.True(t, isMulti)
	assert.Equal(t, SourceType, s.Descriptor().Type)
}
