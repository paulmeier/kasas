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

// SecretStore reads and writes the SimpleFIN access URL.
type SecretStore interface {
	// AccessURL returns the stored access URL, or "" if none is stored yet.
	AccessURL(ctx context.Context) (string, error)
	// SetAccessURL persists the access URL for future runs.
	SetAccessURL(ctx context.Context, accessURL string) error
}

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
	kv   *vaultapi.KVv2
	path string
	key  string
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
	return &vaultStore{kv: client.KVv2(mount), path: cfg.Path, key: key}, nil
}

func (s *vaultStore) AccessURL(ctx context.Context) (string, error) {
	secret, err := s.kv.Get(ctx, s.path)
	if err != nil {
		if errors.Is(err, vaultapi.ErrSecretNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("vault get %q: %w", s.path, err)
	}
	raw, ok := secret.Data[s.key]
	if !ok {
		return "", nil
	}
	url, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("vault secret %q key %q is not a string", s.path, s.key)
	}
	return url, nil
}

func (s *vaultStore) SetAccessURL(ctx context.Context, accessURL string) error {
	_, err := s.kv.Put(ctx, s.path, map[string]interface{}{s.key: accessURL})
	if err != nil {
		return fmt.Errorf("vault put %q: %w", s.path, err)
	}
	return nil
}
