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

type fileSecrets struct {
	AccessURL      string `json:"simplefin_access_url"`
	DashboardToken string `json:"dashboard_token,omitempty"`
}

// load reads the whole secrets file. A missing file is not an error (it yields
// the zero value). The caller must hold f.mu.
func (f *FileStore) load() (fileSecrets, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return fileSecrets{}, nil
	}
	if err != nil {
		return fileSecrets{}, fmt.Errorf("read secrets %q: %w", f.path, err)
	}
	var s fileSecrets
	if err := json.Unmarshal(data, &s); err != nil {
		return fileSecrets{}, fmt.Errorf("parse secrets %q: %w", f.path, err)
	}
	return s, nil
}

// save writes the whole secrets file atomically with owner-only permissions (it
// holds credentials). The caller must hold f.mu.
func (f *FileStore) save(s fileSecrets) error {
	if dir := filepath.Dir(f.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create secrets dir %q: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(s, "", "  ")
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

func (f *FileStore) AccessURL(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.load()
	if err != nil {
		return "", err
	}
	return s.AccessURL, nil
}

// SetAccessURL persists the access URL, preserving any stored dashboard token
// (the two secrets share one file, so it is read-modify-write).
func (f *FileStore) SetAccessURL(ctx context.Context, accessURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.load()
	if err != nil {
		return err
	}
	s.AccessURL = accessURL
	return f.save(s)
}

func (f *FileStore) DashboardToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.load()
	if err != nil {
		return "", err
	}
	return s.DashboardToken, nil
}

// SetDashboardToken persists the dashboard token (empty clears it), preserving
// the stored SimpleFIN access URL.
func (f *FileStore) SetDashboardToken(ctx context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, err := f.load()
	if err != nil {
		return err
	}
	s.DashboardToken = token
	return f.save(s)
}
