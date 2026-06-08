package csv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileRef identifies one file in a backend folder. ID is a backend-stable handle
// (a full path for local files, a file id for Drive); Name is the display name,
// used for logging and the .csv filter.
type FileRef struct {
	ID   string
	Name string
}

// FileStore lists and opens the CSV files in one configured folder, abstracting
// the backend (a local directory or a Google Drive folder) so the parse/map/hash
// logic is backend-agnostic. Implementations only read; they never mutate the
// folder.
type FileStore interface {
	// List returns the CSV files currently in the folder.
	List(ctx context.Context) ([]FileRef, error)
	// Open opens one file's contents for reading; the caller closes it.
	Open(ctx context.Context, ref FileRef) (io.ReadCloser, error)
}

// isCSV reports whether a file name looks like a CSV by extension.
func isCSV(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".csv")
}

// localStore is a FileStore over a local directory.
type localStore struct{ dir string }

// List returns the .csv files directly inside the directory (non-recursive).
func (l *localStore) List(_ context.Context) ([]FileRef, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, fmt.Errorf("read csv folder %q: %w", l.dir, err)
	}
	var out []FileRef
	for _, e := range entries {
		if e.IsDir() || !isCSV(e.Name()) {
			continue
		}
		out = append(out, FileRef{ID: filepath.Join(l.dir, e.Name()), Name: e.Name()})
	}
	return out, nil
}

// Open opens a local file. The FileRef ID is its full path.
func (l *localStore) Open(_ context.Context, ref FileRef) (io.ReadCloser, error) {
	return os.Open(ref.ID)
}
