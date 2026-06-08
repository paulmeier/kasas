package csv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"encoding/json"

	"golang.org/x/oauth2"
)

// Google OAuth + Drive endpoints. The OAuth flow uses only golang.org/x/oauth2
// (no Google SDK): the endpoint URLs are hardcoded rather than imported from
// golang.org/x/oauth2/google to keep dependencies minimal.
const (
	driveScope     = "https://www.googleapis.com/auth/drive.readonly"
	driveAPIBase   = "https://www.googleapis.com/drive/v3"
	googleAuthURL  = "https://accounts.google.com/o/oauth2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
)

// endpoint returns the OAuth endpoint, honoring the test token-URL override.
func (s *Source) endpoint() oauth2.Endpoint {
	tokenURL := googleTokenURL
	if s.tokenURLOverride != "" {
		tokenURL = s.tokenURLOverride
	}
	return oauth2.Endpoint{AuthURL: googleAuthURL, TokenURL: tokenURL}
}

// driveBaseURL returns the Drive v3 API base, honoring the test override.
func (s *Source) driveBaseURL() string {
	if s.driveBaseOverride != "" {
		return s.driveBaseOverride
	}
	return driveAPIBase
}

// oauthConfig builds the OAuth config from the source's Drive app credentials.
func (s *Source) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     s.cfg.GDriveClientID,
		ClientSecret: s.cfg.GDriveClientSecret,
		Endpoint:     s.endpoint(),
		RedirectURL:  s.cfg.GDriveRedirectURL,
		Scopes:       []string{driveScope},
	}
}

// OAuthConfigured implements source.OAuthCredentialed: the browser flow is
// offered only when a Drive folder is configured and the OAuth client (id, secret,
// and registered redirect URL) is fully set.
func (s *Source) OAuthConfigured() bool {
	return s.hasGDrive() &&
		s.cfg.GDriveClientID != "" &&
		s.cfg.GDriveClientSecret != "" &&
		s.cfg.GDriveRedirectURL != ""
}

// AuthCodeURL implements source.OAuthCredentialed: it builds the Google consent
// URL, requesting offline access (a refresh token) and forcing the consent prompt
// so a refresh token is returned even on re-authorization.
func (s *Source) AuthCodeURL(state string) string {
	return s.oauthConfig().AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)
}

// ExchangeCode implements source.OAuthCredentialed: it exchanges the callback's
// authorization code for tokens and persists the refresh token for future syncs.
func (s *Source) ExchangeCode(ctx context.Context, code string) error {
	tok, err := s.oauthConfig().Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange Google authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("no refresh token returned by Google; revoke kasas's access in your Google account, then reconnect")
	}
	return s.secrets.SetSecretValue(ctx, gdriveRefreshKey, tok.RefreshToken)
}

// driveStore builds a FileStore over a Google Drive folder. It reads the stored
// refresh token and wraps an *http.Client that auto-refreshes access tokens via
// the OAuth config.
func (s *Source) driveStore(ctx context.Context, f Folder) (FileStore, error) {
	if s.cfg.GDriveClientID == "" || s.cfg.GDriveClientSecret == "" {
		return nil, errors.New("missing Google Drive client id/secret (set csv.gdrive_client_id and csv.gdrive_client_secret)")
	}
	refresh, err := s.secrets.SecretValue(ctx, gdriveRefreshKey)
	if err != nil {
		return nil, fmt.Errorf("read Google Drive token: %w", err)
	}
	if refresh == "" {
		return nil, errors.New("not connected to Google Drive (connect it from the Sources page, or paste a refresh token)")
	}
	client := s.oauthConfig().Client(ctx, &oauth2.Token{RefreshToken: refresh})
	return &driveStore{httpClient: client, folderID: f.FolderID, baseURL: s.driveBaseURL()}, nil
}

// driveStore is a FileStore backed by the Google Drive v3 REST API.
type driveStore struct {
	httpClient *http.Client // OAuth-wrapped; refreshes access tokens automatically
	folderID   string
	baseURL    string
}

// driveFile is the subset of a Drive file resource the store needs.
type driveFile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
}

// driveList is the Drive files.list response.
type driveList struct {
	NextPageToken string      `json:"nextPageToken"`
	Files         []driveFile `json:"files"`
}

// List returns the CSV files directly in the configured folder, paging through the
// Drive API. Files are matched by a .csv extension or a text/csv mime type.
func (g *driveStore) List(ctx context.Context) ([]FileRef, error) {
	var out []FileRef
	pageToken := ""
	for {
		u, err := url.Parse(g.baseURL + "/files")
		if err != nil {
			return nil, fmt.Errorf("drive list: %w", err)
		}
		q := u.Query()
		q.Set("q", fmt.Sprintf("'%s' in parents and trashed=false", g.folderID))
		q.Set("fields", "nextPageToken,files(id,name,mimeType)")
		q.Set("pageSize", "1000")
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		u.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		resp, err := g.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("drive list: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("drive list: read body: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("drive list: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var lr driveList
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("drive list: decode: %w", err)
		}
		for _, f := range lr.Files {
			if isCSV(f.Name) || f.MimeType == "text/csv" {
				out = append(out, FileRef{ID: f.ID, Name: f.Name})
			}
		}
		if lr.NextPageToken == "" {
			break
		}
		pageToken = lr.NextPageToken
	}
	return out, nil
}

// Open downloads one Drive file's content (alt=media). The caller closes it.
func (g *driveStore) Open(ctx context.Context, ref FileRef) (io.ReadCloser, error) {
	u := g.baseURL + "/files/" + url.PathEscape(ref.ID) + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drive download %q: %w", ref.Name, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("drive download %q: status %d: %s", ref.Name, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return resp.Body, nil
}
