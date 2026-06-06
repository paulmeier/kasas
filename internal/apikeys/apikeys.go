// Package apikeys mints and verifies kasas API keys: per-consumer credentials for
// programmatic REST access, distinct from the single admin dashboard token.
//
// A key is a random secret shown to the operator exactly once at creation. kasas
// stores only its SHA-256 hash, so a database leak never exposes usable
// credentials; verification hashes the presented bearer and looks it up by that
// hash. Each key carries a Scope (read or read_write) so external integrations get
// least-privilege access. The package is pure (no database or HTTP dependency) so
// it is trivially testable, mirroring internal/labels; the data-layer CRUD lives in
// the api layer.
package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// keyPrefix is the human-identifiable prefix on every key. It makes a leaked key
	// recognizable (to a person, a log scrubber, or a secret scanner) and namespaces
	// it to kasas.
	keyPrefix = "kasas_"
	// secretBytes is the entropy of the random part of a key (32 bytes → 43 base64url
	// characters), far beyond brute-force reach.
	secretBytes = 32
	// displayPrefixLen is how many leading characters of the full key are kept as the
	// non-secret `prefix` shown in listings: long enough to tell keys apart, far too
	// short to recover the rest (it reveals 8 of 43 random base64 chars).
	displayPrefixLen = len(keyPrefix) + 8
)

// Scope is the access level a key grants.
type Scope string

const (
	ScopeRead      Scope = "read"       // GET endpoints only
	ScopeReadWrite Scope = "read_write" // GET + mutations (labels, rules, sync)
)

// ParseScope validates and normalizes a scope string. An empty string defaults to
// read (least privilege).
func ParseScope(s string) (Scope, error) {
	switch Scope(strings.TrimSpace(s)) {
	case "", ScopeRead:
		return ScopeRead, nil
	case ScopeReadWrite:
		return ScopeReadWrite, nil
	default:
		return "", fmt.Errorf("invalid scope %q (want %q or %q)", s, ScopeRead, ScopeReadWrite)
	}
}

// Satisfies reports whether a key with scope s may access an endpoint requiring
// need. read_write satisfies both read and read_write; read satisfies only read.
func (s Scope) Satisfies(need Scope) bool {
	if s == ScopeReadWrite {
		return true
	}
	return need == ScopeRead
}

// Generate mints a new API key. It returns the full secret (shown to the operator
// once and never stored), the non-secret display prefix kept for identification,
// and the SHA-256 hash that is persisted and matched at verification time.
func Generate() (full, prefix, hash string, err error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate api key: %w", err)
	}
	full = keyPrefix + base64.RawURLEncoding.EncodeToString(b)
	return full, full[:displayPrefixLen], Hash(full), nil
}

// Hash returns the hex-encoded SHA-256 of a key. It is used both to store a new key
// and to look a presented one up, so the plaintext secret is never persisted.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
