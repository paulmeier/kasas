// Package webhook implements the inbound-webhook ingestion source (archetype
// "webhook"): an external system PUSHes transactions to kasas by POSTing a signed
// [source.ImportBatch] to /api/v1/sources/webhook/ingest, rather than kasas pulling
// them on a schedule. It is the ingestion counterpart to the outbound dispatcher in
// internal/webhooks (note the plural — that package pushes events OUT; this one
// receives data IN), and it deliberately reuses that package's HMAC signing scheme
// so verification is symmetric: a kasas outbound webhook can feed another kasas's
// inbound webhook source unchanged.
//
// Authentication is the security boundary. The ingest endpoint bypasses the
// dashboard token (the sender has none), so every delivery must carry a valid
// HMAC-SHA256 signature over "<timestamp>.<body>" under a shared secret kasas mints
// (see [Source.RotateSecret]). The source is registered and always built, but it
// stays inert — rejecting every delivery — until an operator generates that secret,
// so an unconfigured instance exposes nothing.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
	"github.com/paulmeier/kasas/internal/webhooks"
)

// SourceType is the provenance stamp written on every transaction this source
// ingests and the key it registers under. It is recorded once at insert and never
// overwritten (nothing re-syncs a pushed transaction).
const SourceType = "webhook"

// signingSecretKey is the secret-store key holding the shared HMAC signing secret
// that inbound deliveries are verified against.
const signingSecretKey = "webhook_signing_secret"

// idNamespace prefixes every external id this source produces so pushed accounts
// and transactions never collide with another source's ids (dedup is by
// (source, external_id), but a shared namespace keeps ids self-describing).
const idNamespace = "webhook:"

// timestampTolerance bounds how far a delivery's X-Kasas-Timestamp may be from the
// server clock. It thwarts replay of an old capture while tolerating clock skew;
// re-delivery within the window is harmless anyway (the engine deduplicates).
const timestampTolerance = 5 * time.Minute

// register makes the inbound-webhook source available to the engine when this
// package is imported. It needs only the secret store.
func init() {
	source.Register(descriptor(), func(env source.Env) (source.Source, error) {
		return New(Options{Secrets: env.Secrets, Logger: env.Logger})
	})
}

func descriptor() source.Descriptor {
	return source.Descriptor{
		Type:      SourceType,
		Archetype: source.ArchetypeWebhook,
		Title:     "Inbound webhook",
		// No Credentials field: the signing secret is generated and revealed, not
		// pasted into a setup form, so the dashboard renders the webhook controls
		// (endpoint URL + generate/reveal/rotate) instead of a credential input.
	}
}

// Options configures an inbound-webhook Source.
type Options struct {
	Secrets vault.SecretStore
	Logger  *slog.Logger
}

// Source is the inbound-webhook ingestion source. It implements source.Source,
// source.Receiver, source.Credentialed, and source.WebhookSecret.
type Source struct {
	secrets vault.SecretStore
	logger  *slog.Logger
	// now is overridable in tests for deterministic timestamp-freshness checks.
	now func() time.Time
}

// New constructs an inbound-webhook source.
func New(opts Options) (*Source, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Source{secrets: opts.Secrets, logger: logger, now: time.Now}, nil
}

// Descriptor implements source.Source.
func (s *Source) Descriptor() source.Descriptor { return descriptor() }

// Receive implements source.Receiver: it authenticates an inbound delivery against
// the shared signing secret, parses its body as a neutral ImportBatch, and returns
// the normalized batch for the engine to persist. It never writes to the database.
func (s *Source) Receive(ctx context.Context, d source.Delivery) (*source.ImportBatch, error) {
	secret, err := s.storedSecret(ctx)
	if err != nil {
		return nil, fmt.Errorf("read webhook signing secret: %w", err)
	}
	// No secret means the source has not been activated; reject rather than accept
	// unauthenticated data.
	if secret == "" {
		return nil, source.ErrUnauthorizedDelivery
	}
	if err := s.verify(secret, d); err != nil {
		return nil, err // source.ErrUnauthorizedDelivery
	}

	// An empty or object-only body is a valid verification ping: authenticated, but
	// nothing to persist.
	if len(strings.TrimSpace(string(d.Body))) == 0 {
		return nil, nil
	}
	var batch source.ImportBatch
	if err := json.Unmarshal(d.Body, &batch); err != nil {
		return nil, fmt.Errorf("parse webhook payload: %w", err)
	}
	if len(batch.Accounts) == 0 {
		return nil, nil
	}
	if err := normalize(&batch); err != nil {
		return nil, err
	}
	return &batch, nil
}

// verify checks the delivery's HMAC signature and timestamp freshness using the
// same construction internal/webhooks uses to sign outbound deliveries. Every
// failure mode collapses to source.ErrUnauthorizedDelivery so the handler leaks
// nothing about which check failed.
func (s *Source) verify(secret string, d source.Delivery) error {
	tsHeader := d.Header.Get(webhooks.HeaderTimestamp)
	sigHeader := d.Header.Get(webhooks.HeaderSignature)
	if tsHeader == "" || sigHeader == "" {
		return source.ErrUnauthorizedDelivery
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return source.ErrUnauthorizedDelivery
	}
	delta := s.now().Unix() - ts
	if delta < 0 {
		delta = -delta
	}
	if delta > int64(timestampTolerance.Seconds()) {
		return source.ErrUnauthorizedDelivery
	}
	// Sign over the exact raw body bytes and the exact timestamp string the sender
	// sent, then compare in constant time.
	expected := "sha256=" + webhooks.Sign(secret, tsHeader, d.Body)
	if !hmac.Equal([]byte(expected), []byte(sigHeader)) {
		return source.ErrUnauthorizedDelivery
	}
	return nil
}

// normalize stamps the batch with this source and namespaces every account, org,
// and transaction id so pushed data is idempotent and collision-free. A
// transaction the sender did not key gets a deterministic content hash, so
// re-delivering the same transaction maps to the same row.
func normalize(batch *source.ImportBatch) error {
	batch.Source = SourceType
	for i := range batch.Accounts {
		acct := &batch.Accounts[i]
		raw := strings.TrimSpace(acct.ExternalID)
		if raw == "" {
			raw = slug(acct.Name)
		}
		if raw == "" {
			return fmt.Errorf("webhook payload: account %d needs an external_id or name", i)
		}
		acct.ExternalID = idNamespace + raw

		// An explicit org id/domain is used verbatim; otherwise derive a stable id
		// from the org name, falling back to the account's own id.
		orgRaw := firstNonEmpty(acct.Org.ID, acct.Org.Domain, slug(acct.Org.Name), raw)
		acct.Org.ID = idNamespace + "org:" + orgRaw

		for j := range acct.Transactions {
			t := &acct.Transactions[j]
			if id := strings.TrimSpace(t.ExternalID); id != "" {
				t.ExternalID = idNamespace + id
			} else {
				t.ExternalID = idNamespace + contentHash(acct.ExternalID, *t)
			}
		}
	}
	return nil
}

// contentHash derives a stable id for a transaction the sender did not key, from
// the fields that identify it within its account.
func contentHash(accountID string, t source.ImportTxn) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		accountID, t.Date, t.Amount, t.Description, t.Payee, t.Memo)
	return hex.EncodeToString(h.Sum(nil))
}

// CredentialConfigured implements source.Credentialed: the source is ready (and
// reported "connected") once a signing secret has been generated or set.
func (s *Source) CredentialConfigured(ctx context.Context) (bool, error) {
	secret, err := s.storedSecret(ctx)
	if err != nil {
		return false, err
	}
	return secret != "", nil
}

// SetCredential implements source.Credentialed: it stores a caller-supplied signing
// secret. This is the power-user path (REST/MCP) for matching a secret a sender
// already has; the dashboard uses RotateSecret to mint one instead.
func (s *Source) SetCredential(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("webhook signing secret cannot be empty")
	}
	return s.setSecret(ctx, input)
}

// RevealSecret implements source.WebhookSecret: it returns the current signing
// secret (or "" when none has been generated yet) for the operator to copy into
// the sender.
func (s *Source) RevealSecret(ctx context.Context) (string, error) {
	return s.storedSecret(ctx)
}

// RotateSecret implements source.WebhookSecret: it mints a fresh signing secret,
// stores it, and returns it. The previous secret stops verifying immediately.
func (s *Source) RotateSecret(ctx context.Context) (string, error) {
	secret, err := webhooks.GenerateSecret()
	if err != nil {
		return "", err
	}
	if err := s.setSecret(ctx, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Source) storedSecret(ctx context.Context) (string, error) {
	return s.secrets.SecretValue(ctx, signingSecretKey)
}

func (s *Source) setSecret(ctx context.Context, value string) error {
	return s.secrets.SetSecretValue(ctx, signingSecretKey, value)
}

// firstNonEmpty returns the first trimmed-nonempty argument, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// slug lowercases s and reduces runs of non-alphanumeric characters to single
// dashes, trimming leading/trailing dashes — for stable ids from display names.
func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

var (
	_ source.Source        = (*Source)(nil)
	_ source.Receiver      = (*Source)(nil)
	_ source.Credentialed  = (*Source)(nil)
	_ source.WebhookSecret = (*Source)(nil)
)
