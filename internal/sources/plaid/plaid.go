package plaid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under. It identifies the ingestion path; it is
// recorded once at insert and never overwritten on re-sync.
const SourceType = "plaid"

// accessTokenKey is the secret-store key holding the runtime-managed Plaid access
// tokens — one per linked Item (bank), newline-joined. A single token is just a
// one-line value, so the layout is unchanged for a one-bank setup.
const accessTokenKey = "plaid_access_token"

// register makes the Plaid source available to the engine when this package is
// imported. The factory reads the app credentials, environment, country codes, and
// access tokens from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		return New(Options{
			Secrets:      env.Secrets,
			ClientID:     env.Opt("client_id"),
			Secret:       env.Opt("secret"),
			Environment:  env.Opt("environment"),
			CountryCodes: splitList(env.Opt("country_codes")),
			AccessTokens: splitList(env.Opt("access_tokens")),
			Logger:       env.Logger,
		})
	})
}

func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypePull,
		Title:     "Plaid",
		Credentials: []source.CredentialField{
			{Key: "access_token", Title: "Access token", Help: "A per-Item access token from Plaid Link — one per linked bank. Add as many as you have banks; kasas fans out over all of them. Obtain it by exchanging a Link public_token via Plaid's /item/public_token/exchange."},
		},
		Config: []source.ConfigField{
			{Key: "client_id", Title: "Client ID", Help: "Your Plaid client_id from the Plaid Dashboard. Set via plaid.client_id / KASAS_PLAID_CLIENT_ID.", Required: true},
			{Key: "secret", Title: "Secret", Help: "Your Plaid secret for the selected environment. Set via plaid.secret / KASAS_PLAID_SECRET.", Required: true},
			{Key: "environment", Title: "Environment", Help: "Plaid environment: sandbox (default), development, or production. Determines the API host."},
			{Key: "country_codes", Title: "Country codes", Help: "Comma-separated ISO-3166-1 alpha-2 codes used to resolve institution names (default US)."},
		},
	}
}

// Options configures a Plaid Source.
type Options struct {
	Secrets vault.SecretStore
	// ClientID and Secret are the app-level Plaid credentials (one pair per
	// environment), shared across every linked Item.
	ClientID string
	Secret   string
	// Environment selects the Plaid host: sandbox (default), development, or production.
	Environment string
	// CountryCodes scopes institution-name lookups; defaults to US when empty.
	CountryCodes []string
	// AccessTokens are config-provided access tokens (one per linked Item). Tokens
	// added at runtime (via SetCredential) are unioned with these; stored ones are
	// removable, these are not.
	AccessTokens []string
	BaseURL      string // overrides the Plaid API host (tests only)
	Logger       *slog.Logger
}

// Source is the Plaid ingestion source. It implements source.Source, source.Puller,
// source.Credentialed, and source.MultiCredentialed.
type Source struct {
	secrets      vault.SecretStore
	logger       *slog.Logger
	clientID     string
	secret       string
	countryCodes []string
	configTokens []string
	client       *Client
}

// New constructs a Plaid source. The HTTP client is built here (it has no failure
// mode of its own); a missing/invalid credential surfaces as a Plaid sync error
// rather than preventing the whole service from starting.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		b, err := baseURLFor(opts.Environment)
		if err != nil {
			return nil, err
		}
		baseURL = b
	}
	cc := normalizeCountryCodes(opts.CountryCodes)
	if len(cc) == 0 {
		cc = []string{"US"}
	}
	clientID := strings.TrimSpace(opts.ClientID)
	secret := strings.TrimSpace(opts.Secret)
	return &Source{
		secrets:      opts.Secrets,
		logger:       logger,
		clientID:     clientID,
		secret:       secret,
		countryCodes: cc,
		configTokens: dedupeTokens(opts.AccessTokens),
		client:       NewClient(baseURL, clientID, secret),
	}, nil
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Fetch implements source.Puller: it fans out over every configured access token
// (each one linked Item/bank), listing that Item's accounts (with balances) and its
// transactions since the window, merging all into one batch. cursor is unused. The
// institution name is resolved once per institution (best-effort). An Item whose
// accounts can't be read is logged and skipped; an error is returned only when every
// Item fails (or none is configured) so one broken bank never blocks the rest.
func (s *Source) Fetch(ctx context.Context, since time.Time, _ string) (*source.ImportBatch, error) {
	if s.clientID == "" || s.secret == "" {
		return nil, errors.New("plaid client_id and secret are required (set plaid.client_id / plaid.secret)")
	}
	tokens, err := s.resolveTokens(ctx)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("no Plaid access token configured (set plaid.access_token(s) or connect a bank from the Sources page)")
	}

	now := time.Now().Unix()
	batch := &source.ImportBatch{Source: SourceType}
	seen := make(map[string]bool)        // namespaced account id -> already imported
	instNames := make(map[string]string) // institution id -> resolved name (cached per fetch)
	var errs []error

	for _, token := range tokens {
		acctResp, aerr := s.client.Accounts(ctx, token)
		if aerr != nil {
			errs = append(errs, fmt.Errorf("item %s: %w", maskToken(token), aerr))
			continue
		}

		instID := acctResp.Item.InstitutionID
		instName := s.resolveInstitutionName(ctx, instID, instNames)

		// Build this Item's accounts, keyed by raw account id so transactions can be
		// attached to them; preserve order for a stable batch.
		acctByID := make(map[string]*source.ImportAccount)
		var order []string
		for _, a := range acctResp.Accounts {
			id := accountID(a)
			if seen[id] {
				continue // same account seen through another token; keep the first
			}
			seen[id] = true
			ia := toImportAccount(a, instID, instName, now)
			acctByID[a.AccountID] = &ia
			order = append(order, a.AccountID)
		}

		// Transactions cover the whole Item; group them onto their accounts. A failure
		// here is non-fatal: import the accounts (so balances refresh) and let the next
		// sync pick up the transactions (e.g. after Plaid's PRODUCT_NOT_READY clears).
		txns, terr := s.client.Transactions(ctx, token, since)
		if terr != nil {
			s.logger.Warn("plaid: could not fetch transactions; importing accounts only", "item", maskToken(token), "error", terr)
		} else {
			for _, t := range txns {
				if ia, ok := acctByID[t.AccountID]; ok {
					ia.Transactions = append(ia.Transactions, toImportTxn(t))
				}
			}
		}

		for _, aid := range order {
			batch.Accounts = append(batch.Accounts, *acctByID[aid])
		}
	}

	if len(batch.Accounts) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, e := range errs {
		s.logger.Warn("plaid: skipped an item", "error", e)
	}
	return batch, nil
}

// resolveInstitutionName returns the display name for an institution id, looking it
// up once (best-effort) and caching the result (including a failed lookup, as "") so
// repeated items of the same institution cost one call. An empty id yields "".
func (s *Source) resolveInstitutionName(ctx context.Context, instID string, cache map[string]string) string {
	if instID == "" {
		return ""
	}
	if name, ok := cache[instID]; ok {
		return name
	}
	name := ""
	if inst, err := s.client.Institution(ctx, instID, s.countryCodes); err == nil {
		name = inst.Name
	} else {
		s.logger.Warn("plaid: could not resolve institution name", "institution_id", instID, "error", err)
	}
	cache[instID] = name
	return name
}

// accountID is the namespaced primary-key id for a Plaid account. The engine uses it
// verbatim as the key, so the source-type prefix keeps it from colliding with another
// source's ids.
func accountID(a Account) string { return SourceType + ":" + a.AccountID }

// toImportAccount maps a Plaid account into a neutral ImportAccount, stamping the
// institution and the balance (when present) at the sync time.
func toImportAccount(a Account, instID, instName string, now int64) source.ImportAccount {
	acct := source.ImportAccount{
		ExternalID: accountID(a),
		Org:        toImportOrg(instID, instName),
		Name:       accountName(a),
		Currency:   currencyOf(a.Balances.ISOCurrencyCode, a.Balances.UnofficialCurrencyCode),
	}
	if bal := strings.TrimSpace(string(a.Balances.Current)); bal != "" {
		acct.Balance = bal
		acct.BalanceDate = now
	}
	return acct
}

// toImportOrg maps a Plaid institution into a neutral org, namespacing the id and
// falling back from the id to a slug of the name (then "unknown") so it is stable,
// and from the name to the id so it is never blank.
func toImportOrg(instID, instName string) source.ImportOrg {
	id := strings.TrimSpace(instID)
	name := strings.TrimSpace(instName)
	if id == "" {
		switch {
		case name != "":
			id = slug(name)
		default:
			id = "unknown"
		}
	}
	if name == "" {
		name = strings.TrimSpace(instID)
	}
	return source.ImportOrg{ID: SourceType + ":org:" + id, Name: name}
}

// toImportTxn maps a Plaid transaction into a neutral ImportTxn. Plaid signs OUTFLOWS
// POSITIVE — the opposite of kasas — so the amount is negated; the cleaned merchant
// becomes the payee while the raw transaction name stays the description.
func toImportTxn(t Transaction) source.ImportTxn {
	return source.ImportTxn{
		ExternalID:  SourceType + ":" + t.TransactionID,
		Amount:      negateAmount(string(t.Amount)),
		Date:        transactionDate(t.Date),
		Description: t.Name,
		Payee:       strings.TrimSpace(t.MerchantName),
		Pending:     t.Pending,
	}
}

// transactionDate parses Plaid's YYYY-MM-DD date into unix seconds at UTC midnight.
// An unparseable date yields 0 (kasas's lenient default).
func transactionDate(date string) int64 {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return 0
	}
	return d.UTC().Unix()
}

// accountName picks a human label for an account, falling back from the bank's name
// to the official name, then subtype/type, so the account is never unnamed.
func accountName(a Account) string {
	for _, candidate := range []string{a.Name, a.OfficialName, a.Subtype, a.Type} {
		if c := strings.TrimSpace(candidate); c != "" {
			return c
		}
	}
	return "Account"
}

// currencyOf prefers the ISO currency code, falling back to Plaid's unofficial code
// (used for some non-standard currencies).
func currencyOf(iso, unofficial string) string {
	if c := strings.TrimSpace(iso); c != "" {
		return c
	}
	return strings.TrimSpace(unofficial)
}

// negateAmount flips the sign of a decimal amount string, mapping Plaid's
// outflow-positive convention to kasas's outflow-negative one. It operates on the
// string to preserve the exact value (no float round-trip): a leading sign is added
// or removed, and a zero amount stays unsigned (no "-0").
func negateAmount(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "-") {
		return s[1:]
	}
	s = strings.TrimPrefix(s, "+")
	if isZeroAmount(s) {
		return s
	}
	return "-" + s
}

// isZeroAmount reports whether s is a numeric zero (only zeros, dots, commas), so
// negating it does not produce a "-0".
func isZeroAmount(s string) bool {
	for _, r := range s {
		if r != '0' && r != '.' && r != ',' {
			return false
		}
	}
	return true
}

// CredentialConfigured implements source.Credentialed: it reports whether at least
// one access token is available (stored or from config) so a sync can run. The
// app-level client_id/secret are assumed present (the source is only built with them).
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	tokens, err := s.resolveTokens(ctx)
	if err != nil {
		return false, err
	}
	return len(tokens) > 0, nil
}

// SetCredential implements source.Credentialed by ADDING a bank: the pasted access
// token is appended to the stored set (deduplicated) rather than replacing it, so
// each call connects one more Item. Use RemoveCredential to disconnect one.
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
// per configured token — those declared in config (not removable) and those added at
// runtime (removable) — never revealing the token itself.
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

// RemoveCredential implements source.MultiCredentialed: it disconnects the Item whose
// token has the given id, from the runtime-added set. A token declared in config is
// not removable here (edit the config file instead).
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
		return fmt.Errorf("no removable Plaid credential with id %q", id)
	}
	return s.setStoredTokens(ctx, kept)
}

// resolveTokens returns the effective access tokens: the union of config-declared and
// runtime-stored tokens, deduplicated (config first for a stable order).
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
		return nil, fmt.Errorf("read stored Plaid access tokens: %w", err)
	}
	return splitTokens(raw), nil
}

// setStoredTokens persists the runtime-managed token set, deduplicated.
func (s *Source) setStoredTokens(ctx context.Context, tokens []string) error {
	return s.secrets.SetSecretValue(ctx, accessTokenKey, joinTokens(dedupeTokens(tokens)))
}

// splitTokens parses a newline- or comma-separated token list into a deduplicated
// slice, trimming whitespace and dropping empties. Plaid tokens are single-line
// opaque strings, so either separator is unambiguous.
func splitTokens(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' })
	return dedupeTokens(fields)
}

// splitList parses a comma/newline-separated list (for config options passed through
// the registry's flat string map). It is splitTokens by another name, used for
// country codes and the access-token option.
func splitList(s string) []string { return splitTokens(s) }

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

// normalizeCountryCodes uppercases and trims country codes, dropping empties and
// duplicates while preserving order (Plaid wants ISO-3166-1 alpha-2, e.g. "US").
func normalizeCountryCodes(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, c := range in {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
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
