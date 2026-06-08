package csv

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestRegistered(t *testing.T) {
	assert.True(t, source.Registered(SourceType), "csv source should self-register via init()")
}

func TestFetchLocal(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "jan.csv", "Date,Description,Amount\n2024-01-15,Coffee,-4.50\n2024-01-16,Paycheck,2000.00\n")
	writeFile(t, dir, "notes.txt", "not a csv, ignore me")

	src, err := New(Options{Config: Config{Folders: []Folder{{
		Name: "Chase", Backend: "local", Path: dir, Account: "Chase Checking", Currency: "USD",
	}}}})
	require.NoError(t, err)

	batch, err := src.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	assert.Equal(t, SourceType, batch.Source)
	require.Len(t, batch.Accounts, 1)

	a := batch.Accounts[0]
	assert.Equal(t, "csv:chase-checking", a.ExternalID)
	assert.Equal(t, "Chase Checking", a.Name)
	assert.Equal(t, "USD", a.Currency)
	require.Len(t, a.Transactions, 2)
	for _, tx := range a.Transactions {
		assert.True(t, strings.HasPrefix(tx.ExternalID, "csv:"))
	}

	// Re-scanning yields identical ids (idempotent re-import).
	batch2, err := src.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch2.Accounts, 1)
	require.Len(t, batch2.Accounts[0].Transactions, 2)
	assert.Equal(t, a.Transactions[0].ExternalID, batch2.Accounts[0].Transactions[0].ExternalID)
	assert.Equal(t, a.Transactions[1].ExternalID, batch2.Accounts[0].Transactions[1].ExternalID)
}

func TestFetchLocalMissingFolderErrors(t *testing.T) {
	src, err := New(Options{Config: Config{Folders: []Folder{{
		Name: "Gone", Backend: "local", Path: filepath.Join(t.TempDir(), "does-not-exist"),
	}}}})
	require.NoError(t, err)
	_, err = src.Fetch(context.Background(), time.Time{}, "")
	require.Error(t, err) // the only folder failed
}

func TestNewValidation(t *testing.T) {
	_, err := New(Options{Config: Config{Folders: []Folder{{Name: "a", Backend: "local"}}}})
	require.Error(t, err, "local backend requires a path")

	_, err = New(Options{Config: Config{Folders: []Folder{{Name: "a", Backend: "weird", Path: "/x"}}}})
	require.Error(t, err, "unknown backend")

	_, err = New(Options{Config: Config{Folders: []Folder{{Backend: "local", Path: "/x"}}}})
	require.Error(t, err, "a name or account is required")

	src, err := New(Options{Config: Config{Folders: []Folder{{Name: "a", Backend: "local", Path: "/x"}}}})
	require.NoError(t, err)
	assert.Equal(t, "USD", src.cfg.Folders[0].Currency, "currency defaults to USD")
}

func TestCredentialLocalOnly(t *testing.T) {
	src, err := New(Options{Config: Config{Folders: []Folder{{Name: "a", Backend: "local", Path: "/x"}}}})
	require.NoError(t, err)

	ok, err := src.CredentialConfigured(context.Background())
	require.NoError(t, err)
	assert.True(t, ok, "a local-only source is always ready")
	assert.Empty(t, src.Descriptor().Credentials, "no credential UI for local-only")
	assert.False(t, src.OAuthConfigured())
}

func TestCredentialGDrive(t *testing.T) {
	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	src, err := New(Options{
		Config: Config{
			Folders:            []Folder{{Name: "a", Backend: "gdrive", FolderID: "FID", Account: "A"}},
			GDriveClientID:     "cid",
			GDriveClientSecret: "secret",
			GDriveRedirectURL:  "https://kasas.example/api/v1/sources/csv/oauth/callback",
		},
		Secrets: secrets,
	})
	require.NoError(t, err)

	ok, err := src.CredentialConfigured(context.Background())
	require.NoError(t, err)
	assert.False(t, ok, "not connected before a token is stored")
	assert.True(t, src.OAuthConfigured())
	assert.Contains(t, src.AuthCodeURL("state123"), "state123")

	require.NoError(t, src.SetCredential(context.Background(), "refresh-xyz"))
	ok, err = src.CredentialConfigured(context.Background())
	require.NoError(t, err)
	assert.True(t, ok)

	d := src.Descriptor()
	require.Len(t, d.Credentials, 1)
	assert.Equal(t, gdriveRefreshKey, d.Credentials[0].Key)
}

// TestDriveStoreFetch exercises the Google Drive backend end-to-end against a mock
// of the OAuth token + Drive v3 endpoints, verifying token refresh, listing, and
// downloading flow through Fetch.
func TestDriveStoreFetch(t *testing.T) {
	csvBody := "Date,Description,Amount\n2024-03-01,Drive Txn,-9.99\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("/drive/v3/files", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"files":[{"id":"f1","name":"a.csv","mimeType":"text/csv"}]}`)
	})
	mux.HandleFunc("/drive/v3/files/f1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, csvBody)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	secrets := vault.NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	require.NoError(t, secrets.SetSecretValue(context.Background(), gdriveRefreshKey, "refresh-xyz"))

	src, err := New(Options{
		Config: Config{
			Folders:            []Folder{{Name: "Drive", Backend: "gdrive", FolderID: "FID", Account: "Drive Acct"}},
			GDriveClientID:     "cid",
			GDriveClientSecret: "secret",
			GDriveRedirectURL:  "https://kasas.example/cb",
		},
		Secrets: secrets,
	})
	require.NoError(t, err)
	src.tokenURLOverride = srv.URL + "/token"
	src.driveBaseOverride = srv.URL + "/drive/v3"

	batch, err := src.Fetch(context.Background(), time.Time{}, "")
	require.NoError(t, err)
	require.Len(t, batch.Accounts, 1)
	require.Len(t, batch.Accounts[0].Transactions, 1)
	assert.Equal(t, "-9.99", batch.Accounts[0].Transactions[0].Amount)
	assert.Equal(t, "Drive Txn", batch.Accounts[0].Transactions[0].Description)
}
