// Package auth gates kasas's HTTP surfaces (the REST API, the web dashboard, and
// the MCP-over-HTTP server) behind a single shared access token.
//
// A token supplied via config/env is authoritative; otherwise a token generated
// from the dashboard and saved to the secret store applies. When neither is set,
// the surfaces are unauthenticated.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/paulmeier/kasas/internal/vault"
)

// Token source labels, reported by Source.
const (
	SourceConfig = "config" // from config file / KASAS_DASHBOARD_TOKEN (authoritative)
	SourceStored = "stored" // generated/saved via the dashboard, in the secret store
	SourceNone   = "none"   // no token set anywhere — surfaces are unauthenticated
)

const (
	// minTokenLen is the floor for a caller-supplied custom token, to rule out
	// trivially guessable values. Generated tokens are far longer.
	minTokenLen = 16
	// tokenBytes is the entropy of a generated token (32 bytes → 43 base64url chars).
	tokenBytes = 32
)

// ErrConfigManaged is returned by Generate/SetToken/ClearToken when the active
// token comes from config/env. That token is authoritative and shadows the secret
// store, so writing one there would have no effect.
var ErrConfigManaged = errors.New("dashboard token is managed via configuration (dashboard.token / KASAS_DASHBOARD_TOKEN); remove it to manage the token here")

// Guard is the source of truth for the dashboard access token. A config/env token
// is authoritative; otherwise the token in the secret store applies. The stored
// value is cached in memory — the Guard is its only writer — so Required/Valid/
// Source need no per-request store reads.
type Guard struct {
	configToken string
	secrets     vault.SecretStore

	mu     sync.RWMutex
	stored string // cached stored token (empty when none)
}

// New constructs a Guard, loading any stored token once. A real store read error
// is returned so startup fails loudly rather than silently running unsecured (or
// locked out). A missing secret reads as empty, which is not an error.
func New(configToken string, secrets vault.SecretStore) (*Guard, error) {
	g := &Guard{configToken: strings.TrimSpace(configToken), secrets: secrets}
	if secrets != nil {
		stored, err := secrets.DashboardToken(context.Background())
		if err != nil {
			return nil, fmt.Errorf("load dashboard token: %w", err)
		}
		g.stored = strings.TrimSpace(stored)
	}
	return g, nil
}

// active returns the effective token and its source. The caller holds g.mu.
func (g *Guard) active() (token, source string) {
	if g.configToken != "" {
		return g.configToken, SourceConfig
	}
	if g.stored != "" {
		return g.stored, SourceStored
	}
	return "", SourceNone
}

// Required reports whether a token is active, i.e. requests must authenticate.
func (g *Guard) Required() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	tok, _ := g.active()
	return tok != ""
}

// Source reports where the active token comes from: "config", "stored", or "none".
func (g *Guard) Source() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, src := g.active()
	return src
}

// Valid reports whether presented matches the active token. The comparison is
// constant-time over SHA-256 digests, so neither the token value nor its length
// leaks through timing. Returns false when no token is active (callers should gate
// on Required first) or when presented is empty.
func (g *Guard) Valid(presented string) bool {
	g.mu.RLock()
	tok, _ := g.active()
	g.mu.RUnlock()

	if tok == "" || presented == "" {
		return false
	}
	want := sha256.Sum256([]byte(tok))
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// Generate creates a strong random token, stores it, and returns it (shown to the
// user once). It fails with ErrConfigManaged when the active token is config-managed.
func (g *Guard) Generate(ctx context.Context) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	if err := g.SetToken(ctx, token); err != nil {
		return "", err
	}
	return token, nil
}

// SetToken stores a caller-supplied token (at least minTokenLen characters). It
// fails with ErrConfigManaged when the active token is config-managed.
func (g *Guard) SetToken(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.configToken != "" {
		return ErrConfigManaged
	}
	if len(token) < minTokenLen {
		return fmt.Errorf("token must be at least %d characters", minTokenLen)
	}
	if g.secrets == nil {
		return errors.New("no secret store configured")
	}
	if err := g.secrets.SetDashboardToken(ctx, token); err != nil {
		return err
	}
	g.stored = token
	return nil
}

// ClearToken removes the stored token, disabling auth when no config token is set.
// It fails with ErrConfigManaged when the active token is config-managed.
func (g *Guard) ClearToken(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.configToken != "" {
		return ErrConfigManaged
	}
	if g.secrets == nil {
		return errors.New("no secret store configured")
	}
	if err := g.secrets.SetDashboardToken(ctx, ""); err != nil {
		return err
	}
	g.stored = ""
	return nil
}

// randomToken returns a URL-safe base64 token with tokenBytes of entropy.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
