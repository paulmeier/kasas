package teller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under. It identifies the ingestion path; it
// is recorded once at insert and never overwritten on re-sync.
const SourceType = "teller"

// accessTokenKey is the secret-store key holding the runtime-managed Teller access
// tokens — one per bank enrollment, newline-joined. A single token is just a
// one-line value, so the layout is unchanged for a one-bank setup.
const accessTokenKey = "teller_access_token"

// register makes the Teller source available to the engine when this package is
// imported. The factory reads its access tokens (newline/comma-joined) and
// client-certificate paths from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		return New(Options{
			Secrets:      env.Secrets,
			AccessTokens: splitTokens(env.Opt("access_tokens")),
			Certificate:  env.Opt("certificate"),
			PrivateKey:   env.Opt("private_key"),
			Logger:       env.Logger,
		})
	})
}

func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypePull,
		Title:     "Teller",
		Credentials: []source.CredentialField{
			{Key: "access_token", Title: "Access token", Help: "A per-enrollment token from Teller Connect — one per linked bank. Add as many as you have banks; each is sent to api.teller.io as the basic-auth username."},
		},
		Config: []source.ConfigField{
			{Key: "certificate", Title: "Client certificate", Help: "Path to the Teller client-certificate PEM. Required for the development and production environments; omit for sandbox."},
			{Key: "private_key", Title: "Client private key", Help: "Path to the Teller client private-key PEM. Required alongside the certificate."},
		},
	}
}

// Options configures a Teller Source.
type Options struct {
	Secrets vault.SecretStore
	// AccessTokens are config-provided access tokens (one per bank). Tokens added at
	// runtime (via SetCredential) are unioned with these; stored ones are removable,
	// these are not.
	AccessTokens []string
	Certificate  string // path to the mTLS client-certificate PEM
	PrivateKey   string // path to the mTLS client private-key PEM
	BaseURL      string // overrides the Teller API base URL (tests only)
	Logger       *slog.Logger
}

// Source is the Teller ingestion source. It implements source.Source,
// source.Puller, source.Credentialed, and source.MultiCredentialed.
type Source struct {
	secrets      vault.SecretStore
	logger       *slog.Logger
	configTokens []string
	certPath     string
	keyPath      string
	baseURL      string

	mu     sync.Mutex // guards lazy client construction
	client *Client
}

// New constructs a Teller source. The mutual-TLS client certificate is loaded
// lazily on first fetch (not here), so a missing or malformed certificate surfaces
// as a Teller sync error rather than preventing the whole service from starting.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = apiBaseURL
	}
	return &Source{
		secrets:      opts.Secrets,
		logger:       logger,
		configTokens: dedupeTokens(opts.AccessTokens),
		certPath:     strings.TrimSpace(opts.Certificate),
		keyPath:      strings.TrimSpace(opts.PrivateKey),
		baseURL:      baseURL,
	}, nil
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Fetch implements source.Puller: it fans out over every configured access token
// (each one bank enrollment), listing that enrollment's accounts and pulling each
// account's balance (best-effort) and transactions since the window, merging all
// into one batch. cursor is unused. One enrollment that fails is logged and
// skipped; an error is returned only when every enrollment fails (or none is
// configured) so one broken bank never blocks the rest.
func (s *Source) Fetch(ctx context.Context, since time.Time, _ string) (*source.ImportBatch, error) {
	tokens, err := s.resolveTokens(ctx)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("no Teller access token configured (set teller.access_token(s) or connect a bank from the Sources page)")
	}

	client, err := s.httpClient()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	batch := &source.ImportBatch{Source: SourceType}
	seen := make(map[string]bool) // account id -> already imported (an account visible via two tokens)
	var errs []error

	for _, token := range tokens {
		accounts, aerr := client.Accounts(ctx, token)
		if aerr != nil {
			errs = append(errs, fmt.Errorf("enrollment %s: %w", maskToken(token), aerr))
			continue
		}
		for _, a := range accounts {
			id := accountID(a)
			if seen[id] {
				continue // same account through a second enrollment; keep the first
			}
			seen[id] = true

			bal, berr := client.Balance(ctx, token, a.ID)
			if berr != nil {
				// A missing balance is cosmetic; import the account anyway.
				s.logger.Warn("teller: could not fetch balance", "account", a.ID, "error", berr)
				bal = nil
			}
			acct := toImportAccount(a, bal, now)

			txns, terr := client.Transactions(ctx, token, a.ID, since)
			if terr != nil {
				errs = append(errs, fmt.Errorf("enrollment %s account %s: %w", maskToken(token), a.ID, terr))
				continue
			}
			acct.Transactions = make([]source.ImportTxn, 0, len(txns))
			for _, t := range txns {
				acct.Transactions = append(acct.Transactions, toImportTxn(t))
			}
			batch.Accounts = append(batch.Accounts, acct)
		}
	}

	if len(batch.Accounts) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, e := range errs {
		s.logger.Warn("teller: skipped an enrollment", "error", e)
	}
	return batch, nil
}

// accountID is the namespaced primary-key id for a Teller account. The engine uses
// it verbatim as the key, so the source-type prefix keeps it from colliding with
// another source's ids.
func accountID(a Account) string { return SourceType + ":" + a.ID }

// toImportAccount maps a Teller account (and its balance, if fetched) into a
// neutral ImportAccount.
func toImportAccount(a Account, bal *Balance, now int64) source.ImportAccount {
	acct := source.ImportAccount{
		ExternalID: accountID(a),
		Org:        toImportOrg(a.Institution),
		Name:       accountName(a),
		Currency:   a.Currency,
	}
	if bal != nil {
		acct.Balance = bal.Ledger
		acct.BalanceDate = now
	}
	return acct
}

// toImportOrg maps a Teller institution into a neutral org, namespacing the id.
func toImportOrg(inst Institution) source.ImportOrg {
	id := strings.TrimSpace(inst.ID)
	if id == "" {
		id = slug(inst.Name)
	}
	return source.ImportOrg{ID: SourceType + ":org:" + id, Name: inst.Name}
}

// toImportTxn maps a Teller transaction into a neutral ImportTxn. The amount is
// passed through verbatim (Teller signs outflows negative, matching kasas); the
// cleaned counterparty becomes the payee while the raw bank text stays the
// description.
func toImportTxn(t Transaction) source.ImportTxn {
	return source.ImportTxn{
		ExternalID:  SourceType + ":" + t.ID,
		Amount:      t.Amount,
		Date:        transactionDate(t),
		Description: t.Description,
		Payee:       strings.TrimSpace(t.Details.Counterparty.Name),
		Pending:     t.Status == "pending",
	}
}

// transactionDate parses Teller's YYYY-MM-DD date into unix seconds at UTC
// midnight. An unparseable date yields 0 (kasas's lenient default).
func transactionDate(t Transaction) int64 {
	d, err := time.Parse(dateLayout, t.Date)
	if err != nil {
		return 0
	}
	return d.UTC().Unix()
}

// accountName picks a human label for an account, falling back from the bank's
// name to its subtype/type so the account is never unnamed.
func accountName(a Account) string {
	for _, candidate := range []string{a.Name, a.Subtype, a.Type} {
		if c := strings.TrimSpace(candidate); c != "" {
			return c
		}
	}
	return "Account"
}

// CredentialConfigured implements source.Credentialed: it reports whether at least
// one access token is available (stored or from config) so a sync can run.
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	tokens, err := s.resolveTokens(ctx)
	if err != nil {
		return false, err
	}
	return len(tokens) > 0, nil
}

// SetCredential implements source.Credentialed by ADDING a bank: the pasted access
// token is appended to the stored set (deduplicated) rather than replacing it, so
// each call connects one more enrollment. Use RemoveCredential to disconnect one.
func (s *Source) SetCredential(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("an access token is required")
	}
	stored, err := s.storedTokens(ctx)
	if err != nil {
		return err
	}
	return s.setStoredTokens(ctx, append(stored, input))
}

// ListCredentials implements source.MultiCredentialed: it returns one masked entry
// per configured token — those declared in config (not removable) and those added
// at runtime (removable) — never revealing the token itself.
func (s *Source) ListCredentials(ctx context.Context) ([]source.CredentialEntry, error) {
	stored, err := s.storedTokens(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]source.CredentialEntry, 0, len(s.configTokens)+len(stored))
	seen := make(map[string]bool)
	add := func(token string, removable bool) {
		id := tokenID(token)
		if seen[id] {
			return
		}
		seen[id] = true
		entries = append(entries, source.CredentialEntry{ID: id, Label: maskToken(token), Removable: removable})
	}
	for _, t := range s.configTokens {
		add(t, false)
	}
	for _, t := range stored {
		add(t, true)
	}
	return entries, nil
}

// RemoveCredential implements source.MultiCredentialed: it disconnects the bank
// whose token has the given id, from the runtime-added set. A token declared in
// config is not removable here (edit the config file instead).
func (s *Source) RemoveCredential(ctx context.Context, id string) error {
	stored, err := s.storedTokens(ctx)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(stored))
	found := false
	for _, t := range stored {
		if tokenID(t) == id {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return fmt.Errorf("no removable Teller credential with id %q", id)
	}
	return s.setStoredTokens(ctx, kept)
}

// resolveTokens returns the effective access tokens: the union of config-declared
// and runtime-stored tokens, deduplicated (config first for a stable order).
func (s *Source) resolveTokens(ctx context.Context) ([]string, error) {
	stored, err := s.storedTokens(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]string, 0, len(s.configTokens)+len(stored))
	all = append(all, s.configTokens...)
	all = append(all, stored...)
	return dedupeTokens(all), nil
}

// storedTokens reads the runtime-managed token set from the secret store.
func (s *Source) storedTokens(ctx context.Context) ([]string, error) {
	raw, err := s.secrets.SecretValue(ctx, accessTokenKey)
	if err != nil {
		return nil, fmt.Errorf("read stored Teller access tokens: %w", err)
	}
	return splitTokens(raw), nil
}

// setStoredTokens persists the runtime-managed token set, deduplicated.
func (s *Source) setStoredTokens(ctx context.Context, tokens []string) error {
	return s.secrets.SetSecretValue(ctx, accessTokenKey, joinTokens(dedupeTokens(tokens)))
}

// httpClient lazily builds and caches the HTTP client, loading the mutual-TLS
// client certificate when configured. Both the certificate and key must be given
// together; either alone is a configuration error.
func (s *Source) httpClient() (*Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}

	var cert *tls.Certificate
	switch {
	case s.certPath != "" && s.keyPath != "":
		loaded, err := tls.LoadX509KeyPair(s.certPath, s.keyPath)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		cert = &loaded
	case s.certPath != "" || s.keyPath != "":
		return nil, errors.New("both certificate and private_key are required for mutual TLS (or set neither for the sandbox)")
	}

	s.client = NewClient(s.baseURL, cert)
	return s.client, nil
}

// splitTokens parses a newline- or comma-separated token list into a deduplicated
// slice, trimming whitespace and dropping empties. Teller tokens are single-line
// opaque strings, so either separator is unambiguous.
func splitTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	return dedupeTokens(fields)
}

// joinTokens renders a token set for storage (newline-separated).
func joinTokens(tokens []string) string { return strings.Join(tokens, "\n") }

// dedupeTokens trims, drops empties, and removes duplicates, preserving order.
func dedupeTokens(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	seen := make(map[string]bool)
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// tokenID derives a stable, non-reversible id for a token (for removal by id).
func tokenID(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])[:12]
}

// maskToken renders a token for display without revealing it: the last four
// characters behind a bullet mask.
func maskToken(token string) string {
	token = strings.TrimSpace(token)
	if n := len(token); n > 4 {
		return "••••" + token[n-4:]
	}
	return "••••"
}

// slug lowercases s and reduces runs of non-alphanumeric characters to single
// dashes, for a stable id fallback from a display name.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// Compile-time checks that Source satisfies the engine's contracts.
var (
	_ source.Source            = (*Source)(nil)
	_ source.Puller            = (*Source)(nil)
	_ source.Credentialed      = (*Source)(nil)
	_ source.MultiCredentialed = (*Source)(nil)
)
