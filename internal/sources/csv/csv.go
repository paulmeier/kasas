// Package csv is a kasas ingestion source that imports transactions from CSV
// files in a folder — a local directory or a Google Drive folder. It is a file
// source (archetype "file") triggered like a pull: the engine calls Fetch on the
// sync schedule, and Fetch scans the configured folders, parses any CSV files it
// finds, and returns them as a neutral source.ImportBatch.
//
// Files are re-scanned in full on every sync; each transaction is given a
// content-hash id (see parse.go) so re-importing an unchanged row is idempotent —
// the engine deduplicates by (source, external id). One source instance fans out
// over N configured folder profiles, each mapping to one account with its own
// column mapping and backend.
package csv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under.
const SourceType = "csv"

// gdriveRefreshKey is the secret-store key holding the Google Drive OAuth refresh
// token (shared across all gdrive folders, since they use one Google account).
const gdriveRefreshKey = "gdrive_refresh_token"

const (
	backendLocal  = "local"
	backendGDrive = "gdrive"
)

// Config is the CSV source's configuration, decoded from JSON the engine passes
// via Env.Options["config"] (so the registry's flat string options can carry a
// structured config). The Drive OAuth app credentials are shared by all gdrive
// folders.
type Config struct {
	Folders            []Folder `json:"folders"`
	GDriveClientID     string   `json:"gdrive_client_id"`
	GDriveClientSecret string   `json:"gdrive_client_secret"`
	// GDriveRedirectURL is the absolute callback URL registered with Google; it
	// must point at this kasas instance's OAuth callback endpoint. Required for the
	// browser Connect flow (otherwise paste a refresh token directly).
	GDriveRedirectURL string `json:"gdrive_redirect_url"`
}

// Folder is one import profile: a backend location plus how to map its CSV
// columns onto kasas's universal transaction fields. Each folder maps to one
// account.
type Folder struct {
	Name     string  `json:"name"`      // display name + id basis (defaults to Account)
	Backend  string  `json:"backend"`   // "local" (default) or "gdrive"
	Path     string  `json:"path"`      // directory path (local backend)
	FolderID string  `json:"folder_id"` // Drive folder id (gdrive backend)
	Account  string  `json:"account"`   // account name the rows belong to
	Org      string  `json:"org"`       // institution name (defaults to the account name)
	Currency string  `json:"currency"`  // ISO code (defaults to USD)
	Mapping  Mapping `json:"mapping"`
}

// Mapping declares how a folder's CSV columns map to transaction fields. Every
// field is optional: a column may be given by header name or 0-based index, and
// omitted columns are auto-detected from common header names. Provide either a
// single signed AmountColumn or a DebitColumn/CreditColumn pair.
type Mapping struct {
	HasHeader         *bool  `json:"has_header"` // default true
	Delimiter         string `json:"delimiter"`  // default ","
	DateColumn        string `json:"date_column"`
	DateFormat        string `json:"date_format"` // Go time layout; default tries common ones
	AmountColumn      string `json:"amount_column"`
	DebitColumn       string `json:"debit_column"`
	CreditColumn      string `json:"credit_column"`
	DescriptionColumn string `json:"description_column"`
	PayeeColumn       string `json:"payee_column"`
	MemoColumn        string `json:"memo_column"`
}

// Options configures a CSV Source.
type Options struct {
	Config  Config
	Secrets vault.SecretStore
	Logger  *slog.Logger
}

// Source is the CSV ingestion source. It implements source.Source, source.Puller,
// source.Credentialed, and source.OAuthCredentialed.
type Source struct {
	cfg     Config
	secrets vault.SecretStore
	logger  *slog.Logger

	// Test seams: when set, override the Google OAuth token endpoint and Drive API
	// base URL so the Drive backend can be exercised against an httptest server.
	tokenURLOverride  string
	driveBaseOverride string
}

// register makes the CSV source available to the engine when this package is
// imported. The factory decodes its folder/Drive config from the env.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		var cfg Config
		if raw := env.Opt("config"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
				return nil, fmt.Errorf("csv: parse config: %w", err)
			}
		}
		return New(Options{Config: cfg, Secrets: env.Secrets, Logger: env.Logger})
	})
}

// descriptor is the static, type-level descriptor used for registration and the
// "available sources" listing. An instance's Descriptor reports config-aware
// credential fields.
func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypeFile,
		Title:     "CSV files",
		Config: []source.ConfigField{{
			Key:   "folders",
			Title: "Folder profiles",
			Help:  "Configured via the [csv] config block: one folder per account, local or Google Drive.",
		}},
	}
}

// New constructs a CSV source, applying defaults and validating each folder.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg := opts.Config
	for i := range cfg.Folders {
		f := &cfg.Folders[i]
		if f.Backend == "" {
			f.Backend = backendLocal
		}
		if f.Currency == "" {
			f.Currency = "USD"
		}
		if strings.TrimSpace(f.Name) == "" {
			f.Name = f.Account
		}
		if strings.TrimSpace(f.Name) == "" {
			return nil, fmt.Errorf("csv: folder %d: a name or account is required", i)
		}
		switch f.Backend {
		case backendLocal:
			if strings.TrimSpace(f.Path) == "" {
				return nil, fmt.Errorf("csv: folder %q: path is required for the local backend", f.Name)
			}
		case backendGDrive:
			if strings.TrimSpace(f.FolderID) == "" {
				return nil, fmt.Errorf("csv: folder %q: folder_id is required for the gdrive backend", f.Name)
			}
		default:
			return nil, fmt.Errorf("csv: folder %q: unknown backend %q (want local or gdrive)", f.Name, f.Backend)
		}
	}
	return &Source{cfg: cfg, secrets: opts.Secrets, logger: logger}, nil
}

// Descriptor implements source.Source. It advertises the Google Drive credential
// field only when a gdrive folder is configured, so a local-only setup shows no
// credential UI.
func (s *Source) Descriptor() source.Descriptor {
	d := descriptor()
	if s.hasGDrive() {
		d.Credentials = []source.CredentialField{{
			Key:   gdriveRefreshKey,
			Title: "Google Drive refresh token",
			Help:  "Obtained via Connect Google Drive, or pasted directly.",
		}}
	}
	return d
}

// Fetch implements source.Puller. It scans every configured folder, parses the
// CSV files it finds, and returns them as one batch. since and cursor are ignored:
// a file source ingests whatever rows the files contain (the folder defines the
// scope), and re-imports are made idempotent by content-hash ids rather than a
// cursor. A folder that fails (e.g. Drive not yet connected) is logged and
// skipped; an error is returned only when every folder fails.
func (s *Source) Fetch(ctx context.Context, _ time.Time, _ string) (*source.ImportBatch, error) {
	batch := &source.ImportBatch{Source: SourceType}
	byID := map[string]*source.ImportAccount{}
	var order []string
	var errs []error

	for _, f := range s.cfg.Folders {
		store, err := s.fileStore(ctx, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("folder %q: %w", f.Name, err))
			continue
		}
		txns, err := s.collectFolder(ctx, store, f)
		if err != nil {
			errs = append(errs, fmt.Errorf("folder %q: %w", f.Name, err))
			continue
		}
		id := accountID(f)
		acct, ok := byID[id]
		if !ok {
			a := buildAccount(f, id)
			byID[id] = &a
			acct = &a
			order = append(order, id)
		}
		acct.Transactions = append(acct.Transactions, txns...)
	}

	if len(batch.Accounts) == 0 && len(byID) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	for _, e := range errs {
		s.logger.Warn("csv: skipped a folder", "error", e)
	}
	for _, id := range order {
		batch.Accounts = append(batch.Accounts, *byID[id])
	}
	return batch, nil
}

// collectFolder lists and parses every CSV file in one folder's store, returning
// the mapped transactions. A single unreadable file is logged and skipped so one
// bad file does not lose the rest.
func (s *Source) collectFolder(ctx context.Context, store FileStore, f Folder) ([]source.ImportTxn, error) {
	files, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	var all []source.ImportTxn
	for _, ref := range files {
		rc, err := store.Open(ctx, ref)
		if err != nil {
			s.logger.Warn("csv: could not open file", "file", ref.Name, "error", err)
			continue
		}
		txns, skipped, perr := parseCSV(rc, f)
		_ = rc.Close()
		if perr != nil {
			s.logger.Warn("csv: could not parse file", "file", ref.Name, "error", perr)
			continue
		}
		if skipped > 0 {
			s.logger.Info("csv: skipped unparseable rows", "file", ref.Name, "skipped", skipped)
		}
		all = append(all, txns...)
	}
	return all, nil
}

// fileStore builds the backend reader for one folder.
func (s *Source) fileStore(ctx context.Context, f Folder) (FileStore, error) {
	switch f.Backend {
	case backendLocal:
		return &localStore{dir: f.Path}, nil
	case backendGDrive:
		return s.driveStore(ctx, f)
	default:
		return nil, fmt.Errorf("unknown backend %q", f.Backend)
	}
}

// hasGDrive reports whether any folder uses the Google Drive backend.
func (s *Source) hasGDrive() bool {
	for _, f := range s.cfg.Folders {
		if f.Backend == backendGDrive {
			return true
		}
	}
	return false
}

// CredentialConfigured implements source.Credentialed. A local-only source is
// always ready; a source with Drive folders is ready once a refresh token is
// stored.
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	if !s.hasGDrive() {
		return true, nil
	}
	tok, err := s.secrets.SecretValue(ctx, gdriveRefreshKey)
	if err != nil {
		return false, fmt.Errorf("read Google Drive token: %w", err)
	}
	return tok != "", nil
}

// SetCredential implements source.Credentialed: it stores a Google Drive refresh
// token directly (the paste fallback for the browser OAuth flow).
func (s *Source) SetCredential(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("a Google Drive refresh token is required")
	}
	return s.secrets.SetSecretValue(ctx, gdriveRefreshKey, input)
}

// accountID derives the namespaced account id for a folder from its account name
// (falling back to its profile name).
func accountID(f Folder) string {
	base := f.Account
	if strings.TrimSpace(base) == "" {
		base = f.Name
	}
	sl := slug(base)
	if sl == "" {
		sl = "unnamed"
	}
	return "csv:" + sl
}

// buildAccount assembles the ImportAccount for a folder. CSV has no balance, so it
// is left empty (unknown).
func buildAccount(f Folder, id string) source.ImportAccount {
	name := f.Account
	if strings.TrimSpace(name) == "" {
		name = f.Name
	}
	orgName := f.Org
	if strings.TrimSpace(orgName) == "" {
		orgName = name
	}
	return source.ImportAccount{
		ExternalID: id,
		Org:        source.ImportOrg{ID: "csv:org:" + slug(orgName), Name: orgName},
		Name:       name,
		Currency:   f.Currency,
	}
}

// slug lowercases s and reduces runs of non-alphanumeric characters to single
// dashes, trimming leading/trailing dashes — for stable ids from display names.
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
	_ source.OAuthCredentialed = (*Source)(nil)
)
