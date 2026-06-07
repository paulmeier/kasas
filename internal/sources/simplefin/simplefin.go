package simplefin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under. It identifies the ingestion path; it
// is recorded once at insert and never overwritten on re-sync.
const SourceType = "simplefin"

// register makes the SimpleFIN source available to the engine when this package
// is imported. The factory reads its access URL / setup token from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		return New(Options{
			Secrets:         env.Secrets,
			ConfigAccessURL: env.Opt("access_url"),
			SetupToken:      env.Opt("setup_token"),
			Logger:          env.Logger,
		}), nil
	})
}

func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypePull,
		Title:     "SimpleFIN",
		Credentials: []source.CredentialField{
			{Key: "setup_token", Title: "Setup token", Help: "One-time base64 token from your SimpleFIN bridge; claimed for an access URL on first use."},
			{Key: "access_url", Title: "Access URL", Help: "A ready SimpleFIN access URL (with embedded credentials). Used directly if set."},
		},
	}
}

// Options configures a SimpleFIN Source.
type Options struct {
	Secrets         vault.SecretStore
	ConfigAccessURL string // simplefin.access_url from config, if any
	SetupToken      string // simplefin.setup_token from config, if any
	Logger          *slog.Logger
}

// Source is the SimpleFIN ingestion source. It implements source.Source,
// source.Puller, and source.Credentialed.
type Source struct {
	client     *Client
	secrets    vault.SecretStore
	logger     *slog.Logger
	configURL  string
	setupToken string
}

// New constructs a SimpleFIN source.
func New(opts Options) *Source {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{
		client:     NewClient(),
		secrets:    opts.Secrets,
		logger:     logger,
		configURL:  opts.ConfigAccessURL,
		setupToken: opts.SetupToken,
	}
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Fetch implements source.Puller: it resolves the access URL, fetches accounts
// and transactions from the bridge, and maps them to a neutral batch. since
// bounds the lookback window; SimpleFIN re-fetches a window rather than using a
// cursor, so cursor is ignored and the returned batch carries none.
func (s *Source) Fetch(ctx context.Context, since time.Time, _ string) (*source.ImportBatch, error) {
	accessURL, err := s.resolveAccessURL(ctx)
	if err != nil {
		return nil, err
	}
	if accessURL == "" {
		return nil, errors.New("no SimpleFIN access URL configured (set simplefin.setup_token or simplefin.access_url)")
	}

	set, err := s.client.Fetch(ctx, accessURL, since)
	if err != nil {
		return nil, err
	}
	for _, e := range set.Errors {
		s.logger.Warn("simplefin reported an error", "error", e)
	}
	return toImportBatch(set), nil
}

// toImportBatch projects a SimpleFIN AccountSet into the neutral ImportBatch the
// engine persists, doing SimpleFIN-specific normalization (stable org id,
// posted-vs-transacted date) here so the engine sees only universal fields.
func toImportBatch(set *AccountSet) *source.ImportBatch {
	batch := &source.ImportBatch{
		Source:   SourceType,
		Accounts: make([]source.ImportAccount, 0, len(set.Accounts)),
	}
	for _, a := range set.Accounts {
		acct := source.ImportAccount{
			ExternalID: a.ID,
			Org: source.ImportOrg{
				ID:     a.Org.StableOrgID(),
				Domain: a.Org.Domain,
				Name:   a.Org.Name,
				URL:    a.Org.SfinURL,
			},
			Name:         a.Name,
			Currency:     a.Currency,
			Balance:      a.Balance,
			BalanceDate:  a.BalanceDate,
			Transactions: make([]source.ImportTxn, 0, len(a.Transactions)),
		}
		for _, t := range a.Transactions {
			acct.Transactions = append(acct.Transactions, source.ImportTxn{
				ExternalID:  t.ID,
				Amount:      t.Amount,
				Date:        transactionDate(t),
				Description: t.Description,
				Payee:       t.Payee,
				Memo:        t.Memo,
				Pending:     t.Pending,
			})
		}
		batch.Accounts = append(batch.Accounts, acct)
	}
	return batch
}

// CredentialConfigured implements source.Credentialed: it reports whether an
// access URL is currently stored (i.e. a sync can run).
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	stored, err := s.secrets.AccessURL(ctx)
	if err != nil {
		return false, fmt.Errorf("read stored access URL: %w", err)
	}
	return stored != "", nil
}

// SetCredential implements source.Credentialed: it stores a SimpleFIN credential
// so the next sync uses it, no restart required. input may be a ready access URL
// (http/https, stored verbatim) or a base64 setup token (claimed for an access
// URL first; the claim is a one-time exchange). Mirrors resolveAccessURL so the
// UI-driven and config-driven paths behave identically.
func (s *Source) SetCredential(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("SimpleFIN credential is empty")
	}

	accessURL := input
	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		s.logger.Info("claiming SimpleFIN setup token")
		claimed, err := s.client.Claim(ctx, input)
		if err != nil {
			return fmt.Errorf("claim setup token: %w", err)
		}
		accessURL = claimed
	}

	if err := s.secrets.SetAccessURL(ctx, accessURL); err != nil {
		return fmt.Errorf("store access URL: %w", err)
	}
	return nil
}

// resolveAccessURL determines the SimpleFIN access URL, preferring an already
// stored value, then a directly configured URL, then claiming a setup token.
// Resolved URLs are persisted so the (one-time) setup token is consumed once.
func (s *Source) resolveAccessURL(ctx context.Context) (string, error) {
	stored, err := s.secrets.AccessURL(ctx)
	if err != nil {
		return "", fmt.Errorf("read stored access URL: %w", err)
	}
	if stored != "" {
		return stored, nil
	}

	if s.configURL != "" {
		if err := s.secrets.SetAccessURL(ctx, s.configURL); err != nil {
			s.logger.Warn("failed to persist configured access URL", "error", err)
		}
		return s.configURL, nil
	}

	if s.setupToken != "" {
		s.logger.Info("claiming SimpleFIN setup token")
		url, err := s.client.Claim(ctx, s.setupToken)
		if err != nil {
			return "", fmt.Errorf("claim setup token: %w", err)
		}
		if err := s.secrets.SetAccessURL(ctx, url); err != nil {
			s.logger.Warn("failed to persist claimed access URL", "error", err)
		}
		return url, nil
	}

	return "", nil
}

// Compile-time checks that Source satisfies the engine's contracts.
var (
	_ source.Source       = (*Source)(nil)
	_ source.Puller       = (*Source)(nil)
	_ source.Credentialed = (*Source)(nil)
)
