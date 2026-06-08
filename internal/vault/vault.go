// Package vault resolves the SimpleFIN access URL from a secret store. It
// supports HashiCorp Vault (KV v2) when configured, and falls back to a local
// JSON file otherwise so the service can run with no external dependencies.
package vault

import (
	"context"
	"errors"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

// SecretStore reads and writes kasas's secrets: the SimpleFIN access URL and the
// optional dashboard access token. Both live at the same backend location (one
// JSON file, or one Vault KV path), so writes are read-modify-write to keep them
// from clobbering each other.
type SecretStore interface {
	// AccessURL returns the stored access URL, or "" if none is stored yet.
	AccessURL(ctx context.Context) (string, error)
	// SetAccessURL persists the access URL for future runs.
	SetAccessURL(ctx context.Context, accessURL string) error
	// DashboardToken returns the stored dashboard token, or "" if none is set.
	DashboardToken(ctx context.Context) (string, error)
	// SetDashboardToken persists the dashboard token; "" clears it.
	SetDashboardToken(ctx context.Context, token string) error
	// SecretValue returns an arbitrary stored secret by key, or "" if unset. It lets
	// sources beyond SimpleFIN persist their own credentials (e.g. a Google Drive
	// refresh token) in the shared store; per-source credential scoping is a planned
	// follow-up.
	SecretValue(ctx context.Context, key string) (string, error)
	// SetSecretValue persists an arbitrary secret by key; "" clears it. Writes are
	// read-modify-write so sibling secrets are preserved.
	SetSecretValue(ctx context.Context, key, value string) error
}

// dashboardTokenKey is the key under which the dashboard token is stored, both in
// the local JSON file and within the Vault KV secret.
const dashboardTokenKey = "dashboard_token"

// Config configures the Vault-backed SecretStore.
type Config struct {
	Enabled      bool
	Address      string // overrides VAULT_ADDR when set
	Token        string // overrides VAULT_TOKEN when set
	Mount        string // KV v2 mount path (e.g. "secret")
	Path         string // secret path within the mount (e.g. "kasas")
	AccessURLKey string // key within the secret holding the access URL
}

// New returns a Vault-backed SecretStore when cfg.Enabled is true, otherwise a
// file-backed store at fallbackFile.
func New(cfg Config, fallbackFile string) (SecretStore, error) {
	if !cfg.Enabled {
		return NewFileStore(fallbackFile), nil
	}
	return newVaultStore(cfg)
}

// vaultStore is a SecretStore backed by a Vault KV v2 secrets engine.
type vaultStore struct {
	kv       *vaultapi.KVv2
	path     string
	key      string // key holding the SimpleFIN access URL
	tokenKey string // key holding the dashboard token
}

func newVaultStore(cfg Config) (*vaultStore, error) {
	apiCfg := vaultapi.DefaultConfig()
	if err := apiCfg.Error; err != nil {
		return nil, fmt.Errorf("vault config: %w", err)
	}
	if cfg.Address != "" {
		apiCfg.Address = cfg.Address
	}
	client, err := vaultapi.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	if cfg.Token != "" {
		client.SetToken(cfg.Token)
	}

	mount := cfg.Mount
	if mount == "" {
		mount = "secret"
	}
	key := cfg.AccessURLKey
	if key == "" {
		key = "simplefin_access_url"
	}
	return &vaultStore{kv: client.KVv2(mount), path: cfg.Path, key: key, tokenKey: dashboardTokenKey}, nil
}

func (s *vaultStore) AccessURL(ctx context.Context) (string, error) {
	return s.get(ctx, s.key)
}

func (s *vaultStore) SetAccessURL(ctx context.Context, accessURL string) error {
	return s.putMerged(ctx, s.key, accessURL)
}

func (s *vaultStore) DashboardToken(ctx context.Context) (string, error) {
	return s.get(ctx, s.tokenKey)
}

func (s *vaultStore) SetDashboardToken(ctx context.Context, token string) error {
	return s.putMerged(ctx, s.tokenKey, token)
}

func (s *vaultStore) SecretValue(ctx context.Context, key string) (string, error) {
	return s.get(ctx, key)
}

func (s *vaultStore) SetSecretValue(ctx context.Context, key, value string) error {
	return s.putMerged(ctx, key, value)
}

// get reads a single string value from the kasas secret, returning "" when the
// secret or the key is absent.
func (s *vaultStore) get(ctx context.Context, key string) (string, error) {
	secret, err := s.kv.Get(ctx, s.path)
	if err != nil {
		if errors.Is(err, vaultapi.ErrSecretNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("vault get %q: %w", s.path, err)
	}
	raw, ok := secret.Data[key]
	if !ok {
		return "", nil
	}
	val, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("vault secret %q key %q is not a string", s.path, key)
	}
	return val, nil
}

// putMerged sets (or, when value is "", deletes) one key within the kasas secret
// without disturbing the others. The access URL and the dashboard token share a
// single KV path, so a plain Put would clobber the sibling secret.
func (s *vaultStore) putMerged(ctx context.Context, key, value string) error {
	data := map[string]interface{}{}
	secret, err := s.kv.Get(ctx, s.path)
	switch {
	case err == nil && secret != nil:
		for k, v := range secret.Data {
			data[k] = v
		}
	case errors.Is(err, vaultapi.ErrSecretNotFound):
		// No existing secret; start from an empty map.
	case err != nil:
		return fmt.Errorf("vault get %q: %w", s.path, err)
	}

	if value == "" {
		delete(data, key)
	} else {
		data[key] = value
	}
	if _, err := s.kv.Put(ctx, s.path, data); err != nil {
		return fmt.Errorf("vault put %q: %w", s.path, err)
	}
	return nil
}
