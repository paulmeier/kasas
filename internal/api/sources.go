package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/poller"
)

// handleListSources lists the configured ingestion sources with their readiness
// and credential shape. It always responds 200 (with enabled=false when source
// management is unavailable) so the dashboard's Sources page can distinguish a
// disabled state from a routing error.
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "sources": []poller.SourceStatus{}})
		return
	}
	statuses, err := s.sources.Sources(r.Context())
	if err != nil {
		s.serverError(w, "list sources", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "sources": statuses})
}

// handleSyncSource triggers a sync of a single source by type. Like the global
// trigger it runs asynchronously (a sync may involve a network round-trip and can
// outlast the request timeout); progress is observable via GET /api/v1/sync.
func (s *Server) handleSyncSource(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeError(w, http.StatusServiceUnavailable, "source management is not available")
		return
	}
	typ := chi.URLParam(r, "type")
	if !s.sourceExists(r.Context(), typ) {
		s.writeError(w, http.StatusNotFound, "unknown source "+typ)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.sources.SyncSource(ctx, typ); err != nil {
			s.logger.Error("per-source sync failed", "source", typ, "error", err)
		}
	}()
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "sync started"})
}

// handleSetSourceCredential stores a pasted credential for a source (e.g. a
// SimpleFIN token, or a Google Drive refresh token) and reports the resulting
// connection state.
func (s *Server) handleSetSourceCredential(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeError(w, http.StatusServiceUnavailable, "source management is not available")
		return
	}
	typ := chi.URLParam(r, "type")

	var req setCredentialRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		s.writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if err := s.sources.SetCredential(r.Context(), typ, req.Token); err != nil {
		s.logger.Warn("set source credential failed", "source", typ, "error", err)
		s.writeError(w, http.StatusBadRequest, "could not set credential: "+err.Error())
		return
	}
	connected, err := s.sources.CredentialConfigured(r.Context(), typ)
	if err != nil {
		s.logger.Warn("read source credential status", "source", typ, "error", err)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"connected": connected})
}

// handleSourceOAuthStart begins a source's browser OAuth flow: it mints an
// anti-CSRF state, builds the provider consent URL, and returns it as JSON so the
// dashboard can navigate the browser there (a full-page navigation cannot carry
// the bearer token, so the admin-gated start is a normal authenticated fetch).
func (s *Server) handleSourceOAuthStart(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeError(w, http.StatusServiceUnavailable, "source management is not available")
		return
	}
	typ := chi.URLParam(r, "type")
	state, err := s.oauth.issue(typ)
	if err != nil {
		s.serverError(w, "start oauth", err)
		return
	}
	authURL, err := s.sources.OAuthStart(typ, state)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "could not start sign-in: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
}

// handleSourceOAuthCallback completes a source's OAuth flow. The provider redirects
// the browser here with no Authorization header, so the route is open; it is
// protected by verifying the state issued at start. On success it stores the
// credential and redirects back to the dashboard's Sources page.
func (s *Server) handleSourceOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.sources == nil {
		s.writeError(w, http.StatusServiceUnavailable, "source management is not available")
		return
	}
	typ := chi.URLParam(r, "type")
	q := r.URL.Query()

	if e := q.Get("error"); e != "" {
		s.redirectSources(w, r, typ, "Google sign-in was cancelled or denied.")
		return
	}
	wantType, ok := s.oauth.consume(q.Get("state"))
	if !ok || wantType != typ {
		s.redirectSources(w, r, typ, "Sign-in expired or was invalid; please try again.")
		return
	}
	code := q.Get("code")
	if code == "" {
		s.redirectSources(w, r, typ, "No authorization code was returned.")
		return
	}
	if err := s.sources.OAuthExchange(r.Context(), typ, code); err != nil {
		s.logger.Warn("source oauth exchange failed", "source", typ, "error", err)
		s.redirectSources(w, r, typ, err.Error())
		return
	}
	http.Redirect(w, r, "/sources?connected="+url.QueryEscape(typ), http.StatusFound)
}

// redirectSources sends the browser back to the Sources page with an error message
// to display.
func (s *Server) redirectSources(w http.ResponseWriter, r *http.Request, typ, msg string) {
	dest := "/sources?source=" + url.QueryEscape(typ) + "&error=" + url.QueryEscape(msg)
	http.Redirect(w, r, dest, http.StatusFound)
}

// sourceExists reports whether a source type is configured, for clean 404s on
// per-source actions.
func (s *Server) sourceExists(ctx context.Context, typ string) bool {
	statuses, err := s.sources.Sources(ctx)
	if err != nil {
		return false
	}
	for _, st := range statuses {
		if st.Type == typ {
			return true
		}
	}
	return false
}

// oauthStates tracks in-flight source OAuth flows by their anti-CSRF state value.
// It is in-memory and single-instance: a state is consumed once, on callback, and
// expires after a short window (a process restart between start and callback
// simply asks the user to retry).
type oauthStates struct {
	mu sync.Mutex
	m  map[string]oauthPending
}

type oauthPending struct {
	typ     string
	expires time.Time
}

func newOAuthStates() *oauthStates { return &oauthStates{m: make(map[string]oauthPending)} }

// issue mints a random state bound to a source type, valid for ten minutes.
func (o *oauthStates) issue(typ string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	state := base64.RawURLEncoding.EncodeToString(b)
	o.mu.Lock()
	defer o.mu.Unlock()
	o.m[state] = oauthPending{typ: typ, expires: time.Now().Add(10 * time.Minute)}
	return state, nil
}

// consume validates and removes a state, returning the bound source type. It
// reports false for an unknown or expired state.
func (o *oauthStates) consume(state string) (string, bool) {
	if state == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.m[state]
	if !ok {
		return "", false
	}
	delete(o.m, state)
	if time.Now().After(p.expires) {
		return "", false
	}
	return p.typ, true
}
