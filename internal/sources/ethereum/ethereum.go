package ethereum

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/sources/onchain"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source ingests
// and the key it registers under. It identifies the ingestion path; it is recorded
// once at insert and never overwritten on re-sync.
const SourceType = "ethereum"

// addressKey is the secret-store key holding the runtime-managed watched addresses,
// newline-joined. A single address is just a one-line value.
const addressKey = "ethereum_addresses"

// ethDecimals is the number of wei per ether as a power of ten (1 ETH = 1e18 wei),
// used to render an exact ETH decimal from a wei amount.
const ethDecimals = 18

// register makes the Ethereum source available to the engine when this package is
// imported. The factory reads the Etherscan API key, chain id, an optional API URL
// override, and the watched addresses from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		chainID, _ := strconv.Atoi(strings.TrimSpace(env.Opt("chain_id")))
		return New(Options{
			Secrets:   env.Secrets,
			APIKey:    strings.TrimSpace(env.Opt("api_key")),
			ChainID:   chainID,
			Addresses: onchain.SplitList(env.Opt("addresses")),
			BaseURL:   strings.TrimSpace(env.Opt("api_url")),
			Logger:    env.Logger,
		})
	})
}

func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypePull,
		Title:     "Ethereum",
		Credentials: []source.CredentialField{
			{Key: "address", Title: "Ethereum address", Help: "An Ethereum address (0x…) to watch. Add as many as you like; kasas records each transaction's net ETH change to the address as a ledger entry (received +, sent −, gas included)."},
		},
		Config: []source.ConfigField{
			{Key: "api_key", Title: "Etherscan API key", Help: "A free Etherscan API key (https://etherscan.io/myapikey). Required to enable the source. Set via ethereum.api_key / KASAS_ETHEREUM_API_KEY.", Required: true},
			{Key: "api_url", Title: "API URL", Help: "Etherscan V2 API base URL (default https://api.etherscan.io/v2/api). A Blockscout instance's /api endpoint also works for self-hosting."},
			{Key: "chain_id", Title: "Chain ID", Help: "EVM chain id (default 1 = Ethereum mainnet). Etherscan V2 serves many chains with one key, e.g. 8453 = Base, 42161 = Arbitrum."},
		},
	}
}

// Options configures an Ethereum Source.
type Options struct {
	Secrets vault.SecretStore
	// APIKey is the app-level Etherscan API key, shared across every watched address.
	APIKey string
	// ChainID selects the EVM chain (default 1 = Ethereum mainnet).
	ChainID int
	// Addresses are config-provided addresses to watch. Addresses added at runtime (via
	// SetCredential) are unioned with these; stored ones are removable, these are not.
	Addresses []string
	BaseURL   string // overrides the Etherscan API base URL (default + tests)
	Logger    *slog.Logger
}

// Source is the Ethereum ingestion source. It implements source.Source, source.Puller,
// source.Credentialed, and source.MultiCredentialed (the latter two via the shared
// onchain.AddressBook, where each watched address is one credential entry).
type Source struct {
	logger  *slog.Logger
	client  *Client
	book    *onchain.AddressBook
	chainID int
}

// New constructs an Ethereum source. The HTTP client has no failure mode of its own;
// a missing API key surfaces as an Ethereum sync error rather than preventing the whole
// service from starting.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = apiBaseURL
	}
	chainID := opts.ChainID
	if chainID <= 0 {
		chainID = 1
	}
	return &Source{
		logger:  logger,
		client:  NewClient(baseURL, chainID, opts.APIKey),
		book:    onchain.NewAddressBook(opts.Secrets, addressKey, opts.Addresses, normalizeETH, labelETH),
		chainID: chainID,
	}, nil
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Fetch implements source.Puller: it fans out over every watched address, building one
// account per address (with its ETH balance, best-effort) and one transaction per
// normal (external) transaction, valued at the address's net wei delta. The lookback
// window is mapped to a start block once (best-effort) so old addresses aren't re-walked
// from genesis. cursor is unused. An address whose history can't be read is logged and
// skipped; an error is returned only when every address fails (or none is configured).
func (s *Source) Fetch(ctx context.Context, since time.Time, _ string) (*source.ImportBatch, error) {
	if s.client.apiKey == "" {
		return nil, errors.New("etherscan API key required (set ethereum.api_key / KASAS_ETHEREUM_API_KEY)")
	}
	addrs, err := s.book.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("no Ethereum address configured (set ethereum.address(es) or add one from the Sources page)")
	}

	startBlock := s.startBlock(ctx, since)
	now := time.Now().Unix()
	batch := &source.ImportBatch{Source: SourceType}
	var errs []error

	for _, addr := range addrs {
		txns, terr := s.client.Transactions(ctx, addr, startBlock)
		if terr != nil {
			errs = append(errs, fmt.Errorf("address %s: %w", labelETH(addr), terr))
			continue
		}

		// Balance is cosmetic; import the account (and its transactions) even if it fails.
		var balance *big.Int
		if bal, berr := s.client.Balance(ctx, addr); berr == nil {
			balance = bal
		} else {
			s.logger.Warn("ethereum: could not fetch balance", "address", addr, "error", berr)
		}
		acct := toImportAccount(addr, balance, now)

		seen := make(map[string]bool, len(txns))
		for _, t := range txns {
			if seen[t.Hash] {
				continue
			}
			seen[t.Hash] = true
			acct.Transactions = append(acct.Transactions, toImportTxn(addr, t))
		}
		batch.Accounts = append(batch.Accounts, acct)
	}

	if len(batch.Accounts) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, e := range errs {
		s.logger.Warn("ethereum: skipped an address", "error", e)
	}
	return batch, nil
}

// startBlock maps the lookback window to a txlist start block (0 for an unbounded
// fetch). A failed lookup falls back to 0 (full history) rather than failing the sync.
func (s *Source) startBlock(ctx context.Context, since time.Time) int64 {
	if since.IsZero() {
		return 0
	}
	n, err := s.client.BlockNumberByTime(ctx, since)
	if err != nil {
		s.logger.Warn("ethereum: could not map lookback to a start block; fetching full history", "error", err)
		return 0
	}
	return n
}

// toImportAccount builds the neutral account for one watched address. The address is the
// account; Ethereum is the institution. Balance (when known) is the wei balance as ETH.
func toImportAccount(address string, balanceWei *big.Int, now int64) source.ImportAccount {
	acct := source.ImportAccount{
		ExternalID: SourceType + ":" + address,
		Org:        source.ImportOrg{ID: SourceType + ":org:ethereum", Name: "Ethereum"},
		Name:       labelETH(address),
		Currency:   "ETH",
	}
	if balanceWei != nil {
		acct.Balance = onchain.DecimalFromBaseUnits(balanceWei, ethDecimals)
		acct.BalanceDate = now
	}
	return acct
}

// toImportTxn maps one transaction into a neutral ImportTxn from the address's
// perspective: the amount is the address's net wei delta (received − sent − gas paid as
// sender) rendered as exact ETH, and the id is namespaced by address so a transaction
// touching two watched addresses yields one correct row per address.
func toImportTxn(address string, t Transaction) source.ImportTxn {
	net := netForAddress(t, address)
	sent := strings.EqualFold(t.From, address)
	description, payee := "Received ETH", t.From
	if sent {
		description, payee = "Sent ETH", t.To
	}
	return source.ImportTxn{
		ExternalID:  SourceType + ":" + address + ":" + t.Hash,
		Amount:      onchain.DecimalFromBaseUnits(net, ethDecimals),
		Date:        txTimestamp(t.TimeStamp),
		Description: description,
		Payee:       payee,
	}
}

// netForAddress computes an address's net wei change in a transaction: the value
// received (as recipient) minus the value sent (as sender) minus the gas the address
// paid as sender. A reverted transaction (isError/txreceipt_status) moves no value but
// the sender still pays gas.
func netForAddress(t Transaction, address string) *big.Int {
	net := new(big.Int)
	failed := t.IsError == "1" || t.TxReceiptStatus == "0"
	value := wei(t.Value)

	if !failed && strings.EqualFold(t.To, address) {
		net.Add(net, value)
	}
	if strings.EqualFold(t.From, address) {
		if !failed {
			net.Sub(net, value)
		}
		gas := new(big.Int).Mul(wei(t.GasUsed), wei(t.GasPrice))
		net.Sub(net, gas)
	}
	return net
}

// wei parses a decimal wei string into a big.Int, treating empty/invalid as zero
// (kasas's lenient numeric handling).
func wei(s string) *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return new(big.Int)
	}
	return v
}

// txTimestamp parses Etherscan's unix-seconds string into an int64, or 0 if unparseable.
func txTimestamp(s string) int64 {
	ts, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return ts
}

// labelETH renders an Ethereum address as a readable, middle-truncated label.
func labelETH(addr string) string { return onchain.TruncateMiddle(addr, 6, 4) }

// CredentialConfigured implements source.Credentialed.
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	return s.book.Configured(ctx)
}

// SetCredential implements source.Credentialed by ADDING one watched address (the
// pasted value), validated and appended to the set rather than replacing it.
func (s *Source) SetCredential(ctx context.Context, input string) error {
	return s.book.Add(ctx, input)
}

// ListCredentials implements source.MultiCredentialed: one entry per watched address.
func (s *Source) ListCredentials(ctx context.Context) ([]source.CredentialEntry, error) {
	return s.book.List(ctx)
}

// RemoveCredential implements source.MultiCredentialed: stop watching one address by id.
func (s *Source) RemoveCredential(ctx context.Context, id string) error {
	return s.book.Remove(ctx, id)
}

// Compile-time checks that Source satisfies the engine's contracts.
var (
	_ source.Source            = (*Source)(nil)
	_ source.Puller            = (*Source)(nil)
	_ source.Credentialed      = (*Source)(nil)
	_ source.MultiCredentialed = (*Source)(nil)
)
