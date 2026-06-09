package bitcoin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/sources/onchain"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under. It identifies the ingestion path; it is
// recorded once at insert and never overwritten on re-sync.
const SourceType = "bitcoin"

// addressKey is the secret-store key holding the runtime-managed watched addresses,
// newline-joined. A single address is just a one-line value.
const addressKey = "bitcoin_addresses"

// btcDecimals is the number of satoshis per bitcoin as a power of ten (1 BTC = 1e8
// sat), used to render an exact BTC decimal from a satoshi amount.
const btcDecimals = 8

// register makes the Bitcoin source available to the engine when this package is
// imported. The factory reads the watched addresses and an optional API URL override
// from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		return New(Options{
			Secrets:   env.Secrets,
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
		Title:     "Bitcoin",
		Credentials: []source.CredentialField{
			{Key: "address", Title: "Bitcoin address", Help: "A Bitcoin address to watch — legacy (1…), P2SH (3…), or bech32 (bc1…). Add as many as you like; kasas records each on-chain transaction touching the address as a ledger entry (received +, sent −)."},
		},
		Config: []source.ConfigField{
			{Key: "api_url", Title: "API URL", Help: "mempool.space-compatible (Esplora) API base URL. Defaults to https://mempool.space/api; point it at your own mempool.space / Esplora instance to self-host."},
		},
	}
}

// Options configures a Bitcoin Source.
type Options struct {
	Secrets vault.SecretStore
	// Addresses are config-provided addresses to watch. Addresses added at runtime (via
	// SetCredential) are unioned with these; stored ones are removable, these are not.
	Addresses []string
	BaseURL   string // overrides the mempool.space/Esplora API base URL (default + tests)
	Logger    *slog.Logger
}

// Source is the Bitcoin ingestion source. It implements source.Source, source.Puller,
// source.Credentialed, and source.MultiCredentialed (the latter two via the shared
// onchain.AddressBook, where each watched address is one credential entry).
type Source struct {
	logger *slog.Logger
	client *Client
	book   *onchain.AddressBook
}

// New constructs a Bitcoin source. The HTTP client has no failure mode of its own, so
// a bad address or an unreachable node surfaces as a Bitcoin sync error rather than
// preventing the whole service from starting.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = apiBaseURL
	}
	return &Source{
		logger: logger,
		client: NewClient(baseURL),
		book:   onchain.NewAddressBook(opts.Secrets, addressKey, opts.Addresses, normalizeBTC, labelBTC),
	}, nil
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Fetch implements source.Puller: it fans out over every watched address, building one
// account per address (with its confirmed balance, best-effort) and one transaction per
// on-chain transaction touching it, valued at the address's net satoshi delta. cursor is
// unused. An address whose history can't be read is logged and skipped; an error is
// returned only when every address fails (or none is configured) so one bad address
// never blocks the rest.
func (s *Source) Fetch(ctx context.Context, since time.Time, _ string) (*source.ImportBatch, error) {
	addrs, err := s.book.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errors.New("no Bitcoin address configured (set bitcoin.address(es) or add one from the Sources page)")
	}

	now := time.Now().Unix()
	batch := &source.ImportBatch{Source: SourceType}
	var errs []error

	for _, addr := range addrs {
		txs, terr := s.client.Transactions(ctx, addr, since)
		if terr != nil {
			errs = append(errs, fmt.Errorf("address %s: %w", labelBTC(addr), terr))
			continue
		}

		// Balance is cosmetic; import the account (and its transactions) even if it fails.
		bal, berr := s.client.Balance(ctx, addr)
		if berr != nil {
			s.logger.Warn("bitcoin: could not fetch balance", "address", addr, "error", berr)
		}
		acct := toImportAccount(addr, bal, berr == nil, now)

		seen := make(map[string]bool, len(txs)) // a tx confirming mid-fetch can appear twice
		for _, tx := range txs {
			if seen[tx.TxID] {
				continue
			}
			seen[tx.TxID] = true
			acct.Transactions = append(acct.Transactions, toImportTxn(addr, tx, now))
		}
		batch.Accounts = append(batch.Accounts, acct)
	}

	if len(batch.Accounts) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, e := range errs {
		s.logger.Warn("bitcoin: skipped an address", "error", e)
	}
	return batch, nil
}

// toImportAccount builds the neutral account for one watched address. The address is
// the account; the chain is the institution. Balance (when known) is the confirmed
// satoshi balance rendered as BTC.
func toImportAccount(address string, balanceSat int64, hasBalance bool, now int64) source.ImportAccount {
	acct := source.ImportAccount{
		ExternalID: SourceType + ":" + address,
		Org:        source.ImportOrg{ID: SourceType + ":org:bitcoin", Name: "Bitcoin"},
		Name:       labelBTC(address),
		Currency:   "BTC",
	}
	if hasBalance {
		acct.Balance = onchain.DecimalFromBaseUnits(big.NewInt(balanceSat), btcDecimals)
		acct.BalanceDate = now
	}
	return acct
}

// toImportTxn maps one transaction into a neutral ImportTxn from the address's
// perspective: the amount is the address's net satoshi delta (received − spent)
// rendered as exact BTC, and the id is namespaced by address so a transaction touching
// two watched addresses yields one correct row per address.
func toImportTxn(address string, tx Tx, now int64) source.ImportTxn {
	net := netForAddress(tx, address)
	return source.ImportTxn{
		ExternalID:  SourceType + ":" + address + ":" + tx.TxID,
		Amount:      onchain.DecimalFromBaseUnits(big.NewInt(net), btcDecimals),
		Date:        txDate(tx, now),
		Description: txDirection(net),
		Pending:     !tx.Status.Confirmed,
	}
}

// netForAddress computes an address's net change in a transaction, in satoshis:
// the value of outputs paying it minus the value of inputs spending from it.
func netForAddress(tx Tx, address string) int64 {
	var net int64
	for _, vout := range tx.Vout {
		if vout.Address == address {
			net += vout.Value
		}
	}
	for _, vin := range tx.Vin {
		if vin.Prevout.Address == address {
			net -= vin.Prevout.Value
		}
	}
	return net
}

// txDirection is a one-word human description of a net delta.
func txDirection(net int64) string {
	switch {
	case net > 0:
		return "Received"
	case net < 0:
		return "Sent"
	default:
		return "Self-transfer"
	}
}

// txDate is a transaction's date: its confirmation block time, or the fetch time for a
// still-unconfirmed (mempool) transaction.
func txDate(tx Tx, now int64) int64 {
	if tx.Status.BlockTime > 0 {
		return tx.Status.BlockTime
	}
	return now
}

// labelBTC renders a Bitcoin address as a readable, middle-truncated label.
func labelBTC(addr string) string { return onchain.TruncateMiddle(addr, 8, 6) }

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
