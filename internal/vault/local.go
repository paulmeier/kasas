package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// FileStore is a SecretStore backed by a local JSON file. It is the default
// when Vault is disabled, keeping kasas dependency-free for simple setups.
type FileStore struct {
	path string
	mu   sync.Mutex
}

// NewFileStore returns a FileStore that persists secrets at path.
func NewFileStore(path string) *FileStore {
	return &FileStore{path: path}
}

// accessURLKey is the key under which the SimpleFIN access URL is stored in the
// local file. It matches the vault store's default so the two backends use the
// same name. dashboardTokenKey is shared with the vault store (see vault.go).
const accessURLKey = "simplefin_access_url"

// load reads the whole secrets file as a flat key/value map. A missing or empty
// file is not an error (it yields an empty map). The caller must hold f.mu. The
// flat-map shape is on-disk compatible with the earlier typed layout (the same
// keys), and lets sources beyond SimpleFIN store their own secrets by key.
func (f *FileStore) load() (map[string]string, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read secrets %q: %w", f.path, err)
	}
	m := map[string]string{}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse secrets %q: %w", f.path, err)
	}
	return m, nil
}

// save writes the whole secrets file atomically with owner-only permissions (it
// holds credentials). The caller must hold f.mu.
func (f *FileStore) save(m map[string]string) error {
	if dir := filepath.Dir(f.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create secrets dir %q: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write secrets %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("replace secrets %q: %w", f.path, err)
	}
	return nil
}

// get reads one key, returning "" when absent. set writes one key (empty clears
// it), preserving sibling secrets (read-modify-write). Both lock f.mu.
func (f *FileStore) get(key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return "", err
	}
	return m[key], nil
}

func (f *FileStore) set(key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, err := f.load()
	if err != nil {
		return err
	}
	if value == "" {
		delete(m, key)
	} else {
		m[key] = value
	}
	return f.save(m)
}

func (f *FileStore) AccessURL(ctx context.Context) (string, error) { return f.get(accessURLKey) }

// SetAccessURL persists the access URL, preserving any sibling secrets (the file
// holds several, so it is read-modify-write).
func (f *FileStore) SetAccessURL(ctx context.Context, accessURL string) error {
	return f.set(accessURLKey, accessURL)
}

func (f *FileStore) DashboardToken(ctx context.Context) (string, error) {
	return f.get(dashboardTokenKey)
}

// SetDashboardToken persists the dashboard token (empty clears it), preserving
// any sibling secrets.
func (f *FileStore) SetDashboardToken(ctx context.Context, token string) error {
	return f.set(dashboardTokenKey, token)
}

func (f *FileStore) SecretValue(ctx context.Context, key string) (string, error) {
	return f.get(key)
}

func (f *FileStore) SetSecretValue(ctx context.Context, key, value string) error {
	return f.set(key, value)
}
