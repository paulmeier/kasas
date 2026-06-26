package webhook

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
	"github.com/paulmeier/kasas/internal/webhooks"
)

// fixedNow is the deterministic clock the tests sign and verify against.
var fixedNow = time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

func newSource(t *testing.T) *Source {
	t.Helper()
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	s, err := New(Options{Secrets: secrets})
	require.NoError(t, err)
	s.now = func() time.Time { return fixedNow }
	return s
}

// signed builds a delivery signed exactly as kasas's outbound dispatcher signs, at
// the given timestamp.
func signed(secret string, ts time.Time, body string) source.Delivery {
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	h := http.Header{}
	h.Set(webhooks.HeaderTimestamp, tsStr)
	h.Set(webhooks.HeaderSignature, "sha256="+webhooks.Sign(secret, tsStr, []byte(body)))
	return source.Delivery{Header: h, Body: []byte(body)}
}

const samplePayload = `{
  "accounts": [{
    "external_id": "acct1",
    "name": "Pushed checking",
    "currency": "USD",
    "balance": "100.00",
    "org": {"name": "Acme"},
    "transactions": [
      {"external_id": "tx1", "amount": "-9.99", "date": 1750000000, "payee": "Coffee"},
      {"amount": "-1.50", "date": 1750000100, "payee": "Tip"}
    ]
  }]
}`

func TestReceiveValidDelivery(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, err := s.RotateSecret(ctx)
	require.NoError(t, err)
	require.True(t, len(secret) > 0)

	batch, err := s.Receive(ctx, signed(secret, fixedNow, samplePayload))
	require.NoError(t, err)
	require.NotNil(t, batch)

	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)
	acct := batch.Accounts[0]
	assert.Equal(t, "webhook:acct1", acct.ExternalID)
	assert.Equal(t, "webhook:org:acme", acct.Org.ID)
	require.Len(t, acct.Transactions, 2)
	// A sender-keyed transaction is namespaced; an unkeyed one gets a content hash.
	assert.Equal(t, "webhook:tx1", acct.Transactions[0].ExternalID)
	assert.True(t, len(acct.Transactions[1].ExternalID) > len(idNamespace))
	assert.Contains(t, acct.Transactions[1].ExternalID, idNamespace)
}

func TestReceiveUnkeyedTransactionIsIdempotent(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)

	first, err := s.Receive(ctx, signed(secret, fixedNow, samplePayload))
	require.NoError(t, err)
	second, err := s.Receive(ctx, signed(secret, fixedNow, samplePayload))
	require.NoError(t, err)
	// Re-delivering the same body yields the same synthesized id, so the engine
	// deduplicates rather than creating a second row.
	assert.Equal(t,
		first.Accounts[0].Transactions[1].ExternalID,
		second.Accounts[0].Transactions[1].ExternalID,
	)
}

func TestReceiveRejectsWhenNoSecret(t *testing.T) {
	s := newSource(t)
	// No secret generated yet: even a structurally fine request is rejected.
	_, err := s.Receive(context.Background(), signed("whsec_whatever", fixedNow, samplePayload))
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

func TestReceiveRejectsBadSignature(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)

	d := signed(secret, fixedNow, samplePayload)
	d.Body = []byte(samplePayload + " ") // tamper the body after signing
	_, err := s.Receive(ctx, d)
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

func TestReceiveRejectsWrongSecret(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	_, _ = s.RotateSecret(ctx)
	// Sign with a secret the source does not hold.
	_, err := s.Receive(ctx, signed("whsec_other", fixedNow, samplePayload))
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

func TestReceiveRejectsStaleTimestamp(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)
	// Signed an hour ago, well outside the freshness window.
	_, err := s.Receive(ctx, signed(secret, fixedNow.Add(-time.Hour), samplePayload))
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

func TestReceiveRejectsMissingHeaders(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)
	_, err := s.Receive(ctx, source.Delivery{Header: http.Header{}, Body: []byte(samplePayload)})
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
	_ = secret
}

func TestReceiveMalformedBodyIsNotUnauthorized(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)
	_, err := s.Receive(ctx, signed(secret, fixedNow, "{not json"))
	require.Error(t, err)
	// A well-authenticated but unparseable body is a 400, not a 401.
	assert.NotErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

func TestReceivePingIsNoOp(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)

	for _, body := range []string{"", "   ", "{}", `{"accounts":[]}`} {
		batch, err := s.Receive(ctx, signed(secret, fixedNow, body))
		require.NoError(t, err, "body %q", body)
		assert.Nil(t, batch, "body %q should be an accepted no-op", body)
	}
}

func TestSecretLifecycle(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()

	configured, err := s.CredentialConfigured(ctx)
	require.NoError(t, err)
	assert.False(t, configured)

	revealed, err := s.RevealSecret(ctx)
	require.NoError(t, err)
	assert.Empty(t, revealed)

	first, err := s.RotateSecret(ctx)
	require.NoError(t, err)
	configured, _ = s.CredentialConfigured(ctx)
	assert.True(t, configured)
	revealed, _ = s.RevealSecret(ctx)
	assert.Equal(t, first, revealed)

	// Rotating replaces the secret, and the old one stops verifying.
	second, err := s.RotateSecret(ctx)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	_, err = s.Receive(ctx, signed(first, fixedNow, samplePayload))
	assert.ErrorIs(t, err, source.ErrUnauthorizedDelivery)
	batch, err := s.Receive(ctx, signed(second, fixedNow, samplePayload))
	require.NoError(t, err)
	assert.NotNil(t, batch)
}

func TestSetCredentialPastedSecret(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()

	assert.Error(t, s.SetCredential(ctx, "   "))

	require.NoError(t, s.SetCredential(ctx, "whsec_pasted"))
	revealed, _ := s.RevealSecret(ctx)
	assert.Equal(t, "whsec_pasted", revealed)
	batch, err := s.Receive(ctx, signed("whsec_pasted", fixedNow, samplePayload))
	require.NoError(t, err)
	assert.NotNil(t, batch)
}

func TestReceiveAccountWithoutIDFallsBackToName(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)

	body := `{"accounts":[{"name":"My Wallet","transactions":[{"amount":"5.00","date":1}]}]}`
	batch, err := s.Receive(ctx, signed(secret, fixedNow, body))
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 1)
	assert.Equal(t, "webhook:my-wallet", batch.Accounts[0].ExternalID)
}

func TestReceiveAccountWithoutIDOrNameErrors(t *testing.T) {
	s := newSource(t)
	ctx := context.Background()
	secret, _ := s.RotateSecret(ctx)

	body := `{"accounts":[{"transactions":[{"amount":"5.00","date":1}]}]}`
	_, err := s.Receive(ctx, signed(secret, fixedNow, body))
	require.Error(t, err)
	assert.NotErrorIs(t, err, source.ErrUnauthorizedDelivery)
}

// Ensure the compile-time capability assertions hold (also documents intent).
func TestImplementsCapabilities(t *testing.T) {
	var s any = &Source{}
	_, ok := s.(source.Receiver)
	assert.True(t, ok)
	_, ok = s.(source.WebhookSecret)
	assert.True(t, ok)
	_, ok = s.(source.Credentialed)
	assert.True(t, ok)
}
