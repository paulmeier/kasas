// Package bitcoin implements Bitcoin address-watching as a kasas ingestion source.
// It is a first-party [source.Puller]: given one or more Bitcoin addresses, it
// fetches each address's on-chain history from a mempool.space / Esplora API and
// returns it as a neutral source.ImportBatch for the ingestion engine to persist.
//
// Bitcoin uses the UTXO model, so a transaction has no single signed amount for an
// address; the source computes the address's net delta per transaction —
// Σ(outputs paying the address) − Σ(inputs spending from the address) — in
// satoshis, then renders it as an exact BTC decimal (received +, sent −, matching
// kasas's outflow-negative convention). The API needs no key, and its base URL is
// overridable so a self-hoster can point at their own node. See
// https://mempool.space/docs/api/rest and the Esplora HTTP API it implements.
package bitcoin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiBaseURL is the default mempool.space API root. It is overridable (Options.BaseURL
// / the api_url config) so the source can talk to a self-hosted mempool.space or
// Esplora instance.
const apiBaseURL = "https://mempool.space/api"

// maxPages bounds backward pagination of confirmed transactions as a runaway guard.
// With a non-zero since window the block-time cutoff stops paging long before this;
// it only matters for an unbounded fetch of an extremely long address history. Each
// page is 25 confirmed transactions (Esplora's fixed page size).
const maxPages = 200

// Client talks to a mempool.space / Esplora HTTP API.
type Client struct {
	httpClient *http.Client
	baseURL    string
}

// NewClient returns a client for baseURL (e.g. https://mempool.space/api).
func NewClient(baseURL string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
	}
}

// Tx is one Bitcoin transaction as returned by the Esplora API. Only the fields the
// source needs are decoded: the id, the inputs and outputs (to compute an address's
// net delta), and the confirmation status (for pending and dating).
type Tx struct {
	TxID   string   `json:"txid"`
	Vin    []Vin    `json:"vin"`
	Vout   []Vout   `json:"vout"`
	Status TxStatus `json:"status"`
}

// Vin is a transaction input; its prevout describes the output being spent (which
// address it paid and how much).
type Vin struct {
	Prevout Vout `json:"prevout"`
}

// Vout is a transaction output: the address it pays and the value in satoshis.
type Vout struct {
	Address string `json:"scriptpubkey_address"`
	Value   int64  `json:"value"`
}

// TxStatus is a transaction's confirmation status. An unconfirmed (mempool)
// transaction has Confirmed=false and no BlockTime.
type TxStatus struct {
	Confirmed bool  `json:"confirmed"`
	BlockTime int64 `json:"block_time"`
}

// addressStats is the /address/{a} response; chain_stats carries the confirmed
// funded/spent satoshi sums, whose difference is the confirmed balance.
type addressStats struct {
	ChainStats struct {
		FundedTxoSum int64 `json:"funded_txo_sum"`
		SpentTxoSum  int64 `json:"spent_txo_sum"`
	} `json:"chain_stats"`
}

// Balance returns an address's confirmed balance in satoshis (funded − spent).
func (c *Client) Balance(ctx context.Context, address string) (int64, error) {
	var stats addressStats
	if err := c.get(ctx, "/address/"+url.PathEscape(address), &stats); err != nil {
		return 0, fmt.Errorf("get address: %w", err)
	}
	return stats.ChainStats.FundedTxoSum - stats.ChainStats.SpentTxoSum, nil
}

// Transactions returns an address's transactions on or after since (zero means
// everything available). It returns the unconfirmed (mempool) transactions first,
// then walks confirmed transactions newest-first, paging backward via the Esplora
// chain endpoint until it reaches a transaction before the window (or runs out). The
// engine deduplicates by (source, external id), so a re-fetched window is harmless.
func (c *Client) Transactions(ctx context.Context, address string, since time.Time) ([]Tx, error) {
	esc := url.PathEscape(address)

	// Pending transactions have no block time; always include them (they are current).
	all, err := c.addressTxs(ctx, "/address/"+esc+"/txs/mempool")
	if err != nil {
		return nil, err
	}

	lastSeen := ""
	for page := 0; page < maxPages; page++ {
		path := "/address/" + esc + "/txs/chain"
		if lastSeen != "" {
			path += "/" + url.PathEscape(lastSeen)
		}
		batch, err := c.addressTxs(ctx, path)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		stop := false
		for _, tx := range batch {
			if !since.IsZero() && tx.Status.BlockTime > 0 && tx.Status.BlockTime < since.Unix() {
				stop = true
				break
			}
			all = append(all, tx)
		}
		next := batch[len(batch)-1].TxID
		if stop || next == lastSeen {
			break
		}
		lastSeen = next
	}
	return all, nil
}

// addressTxs fetches and decodes one Esplora address-transactions page.
func (c *Client) addressTxs(ctx context.Context, path string) ([]Tx, error) {
	var txs []Tx
	if err := c.get(ctx, path, &txs); err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	return txs, nil
}

// get performs a GET and decodes the JSON body into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return apiError(resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// apiError formats a non-200 response. Esplora returns a plain-text message (e.g.
// "Invalid Bitcoin address"), which carries no secret, so surfacing it is safe.
func apiError(status int, body []byte) error {
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	return fmt.Errorf("mempool.space API error (status %d): %s", status, msg)
}
