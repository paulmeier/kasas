// Package ethereum implements Ethereum address-watching as a kasas ingestion source.
// It is a first-party [source.Puller]: given one or more Ethereum addresses, it
// fetches each address's transaction history from the Etherscan API and returns it as
// a neutral source.ImportBatch for the ingestion engine to persist.
//
// Ethereum uses the account model, so each transaction has a signed value from the
// address's perspective. The source records the address's net native-ETH balance
// change per transaction — received value, minus sent value, minus the gas the
// address paid as sender (gas applies even to a failed transaction) — rendered as an
// exact ETH decimal (received +, sent −). Etherscan needs a free API key (an
// app-level secret, shared across addresses); its V2 endpoint serves many EVM chains
// behind one key via a chain id. The base URL is overridable, and because Etherscan's
// account/txlist dialect is what Blockscout also speaks, a self-hoster can point it at
// a Blockscout instance's /api. See https://docs.etherscan.io/etherscan-v2.
package ethereum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// apiBaseURL is the default Etherscan V2 API root (multichain; the chain is selected
// by the chainid query parameter). It is overridable (Options.BaseURL / the api_url
// config) for a different Etherscan deployment or a Blockscout instance's /api.
const apiBaseURL = "https://api.etherscan.io/v2/api"

const (
	// pageSize is the per-page transaction count for txlist pagination (Etherscan
	// caps a single window at 10000 records total).
	pageSize = 1000
	// maxPages bounds forward pagination as a runaway guard.
	maxPages = 20
)

// Client talks to the Etherscan V2 (or a compatible) HTTP API. Every request is a GET
// carrying the chain id and the API key as query parameters.
type Client struct {
	httpClient *http.Client
	baseURL    string
	chainID    int
	apiKey     string
}

// NewClient returns a client for baseURL, targeting the given EVM chain id and
// authenticating with apiKey.
func NewClient(baseURL string, chainID int, apiKey string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 60 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
		chainID:    chainID,
		apiKey:     apiKey,
	}
}

// Transaction is one normal (external) transaction from Etherscan's txlist action.
// The numeric fields are decimal wei strings, preserved verbatim so the exact value
// survives (parsed with math/big, never a float).
type Transaction struct {
	Hash            string `json:"hash"`
	From            string `json:"from"`
	To              string `json:"to"`
	Value           string `json:"value"`     // wei
	Gas             string `json:"gas"`       // gas limit
	GasPrice        string `json:"gasPrice"`  // wei per gas
	GasUsed         string `json:"gasUsed"`   // gas actually used
	TimeStamp       string `json:"timeStamp"` // unix seconds
	IsError         string `json:"isError"`   // "1" if the tx reverted
	TxReceiptStatus string `json:"txreceipt_status"`
	BlockNumber     string `json:"blockNumber"`
}

// apiResponse is the Etherscan envelope. Result is left raw because it is an array for
// list actions and a string for scalar actions (balance, block number) and errors.
type apiResponse struct {
	Status  string          `json:"status"` // "1" ok, "0" error or empty
	Message string          `json:"message"`
	Result  json.RawMessage `json:"result"`
}

// Transactions returns an address's normal transactions from startBlock onward,
// oldest-first, paging forward until a short page. The engine deduplicates by
// (source, external id), so overlap across syncs is harmless.
func (c *Client) Transactions(ctx context.Context, address string, startBlock int64) ([]Transaction, error) {
	var all []Transaction
	for page := 1; page <= maxPages; page++ {
		params := url.Values{}
		params.Set("module", "account")
		params.Set("action", "txlist")
		params.Set("address", address)
		params.Set("startblock", strconv.FormatInt(startBlock, 10))
		params.Set("endblock", "99999999")
		params.Set("page", strconv.Itoa(page))
		params.Set("offset", strconv.Itoa(pageSize))
		params.Set("sort", "asc")

		var batch []Transaction
		if err := c.call(ctx, params, &batch); err != nil {
			return nil, fmt.Errorf("list transactions: %w", err)
		}
		all = append(all, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	return all, nil
}

// Balance returns an address's native-ETH balance in wei.
func (c *Client) Balance(ctx context.Context, address string) (*big.Int, error) {
	params := url.Values{}
	params.Set("module", "account")
	params.Set("action", "balance")
	params.Set("address", address)
	params.Set("tag", "latest")

	var wei string
	if err := c.call(ctx, params, &wei); err != nil {
		return nil, fmt.Errorf("get balance: %w", err)
	}
	v, ok := new(big.Int).SetString(strings.TrimSpace(wei), 10)
	if !ok {
		return nil, fmt.Errorf("unparseable balance %q", wei)
	}
	return v, nil
}

// BlockNumberByTime returns the number of the last block at or before t, used to turn
// the lookback window into a txlist start block (so an old address isn't re-walked
// from genesis every sync).
func (c *Client) BlockNumberByTime(ctx context.Context, t time.Time) (int64, error) {
	params := url.Values{}
	params.Set("module", "block")
	params.Set("action", "getblocknobytime")
	params.Set("timestamp", strconv.FormatInt(t.Unix(), 10))
	params.Set("closest", "before")

	var num string
	if err := c.call(ctx, params, &num); err != nil {
		return 0, fmt.Errorf("get block by time: %w", err)
	}
	return strconv.ParseInt(strings.TrimSpace(num), 10, 64)
}

// call performs an API request and decodes the envelope's result into out. A status of
// "1" is success; a status of "0" with a "no transactions/records found" message is an
// empty result (not an error) so an unused address syncs cleanly; any other status is
// an error.
func (c *Client) call(ctx context.Context, params url.Values, out any) error {
	body, err := c.get(ctx, params)
	if err != nil {
		return err
	}
	var env apiResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if env.Status == "1" || noRecords(env.Message) {
		if out != nil && len(env.Result) > 0 {
			if err := json.Unmarshal(env.Result, out); err != nil {
				// A "no records" result is an empty array, which decodes fine into a slice;
				// only a genuine type mismatch is an error.
				if !noRecords(env.Message) {
					return fmt.Errorf("decode result: %w", err)
				}
			}
		}
		return nil
	}
	return apiError(env.Message, env.Result)
}

// get issues the GET and returns the response body, attaching the chain id and API key
// as query parameters. The key never appears in the body or an Etherscan error, and
// transport errors (which embed the URL) are scrubbed of the key before surfacing.
func (c *Client) get(ctx context.Context, params url.Values) ([]byte, error) {
	params.Set("chainid", strconv.Itoa(c.chainID))
	if c.apiKey != "" {
		params.Set("apikey", c.apiKey)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, c.scrub(err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, c.scrub(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("etherscan API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// scrub removes the API key from an error string (transport errors embed the request
// URL, which carries the key) so it never lands in logs.
func (c *Client) scrub(err error) error {
	if err == nil || c.apiKey == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), c.apiKey, "***"))
}

// noRecords reports whether an Etherscan message denotes an empty (but successful)
// result rather than a failure.
func noRecords(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "no transactions found") || strings.Contains(m, "no records found")
}

// apiError formats an Etherscan failure. The result is usually a human string (e.g.
// "Invalid API Key"); it never contains the key (which travels only in the request
// URL), so surfacing it is safe.
func apiError(message string, result json.RawMessage) error {
	detail := strings.TrimSpace(string(result))
	var s string
	if json.Unmarshal(result, &s) == nil && strings.TrimSpace(s) != "" {
		detail = strings.TrimSpace(s)
	}
	if detail != "" && detail != "null" && !strings.EqualFold(detail, message) {
		return fmt.Errorf("etherscan API error: %s: %s", message, detail)
	}
	return fmt.Errorf("etherscan API error: %s", message)
}
