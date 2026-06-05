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
	AccessURL string `json:"simplefin_access_url"`
}

func (f *FileStore) AccessURL(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read secrets %q: %w", f.path, err)
	}
	var s fileSecrets
	if err := json.Unmarshal(data, &s); err != nil {
		return "", fmt.Errorf("parse secrets %q: %w", f.path, err)
	}
	return s.AccessURL, nil
}

func (f *FileStore) SetAccessURL(ctx context.Context, accessURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if dir := filepath.Dir(f.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create secrets dir %q: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(fileSecrets{AccessURL: accessURL}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	// Write atomically and restrict permissions: the access URL embeds
	// credentials for the SimpleFIN bridge.
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write secrets %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, f.path); err != nil {
		return fmt.Errorf("replace secrets %q: %w", f.path, err)
	}
	return nil
}
