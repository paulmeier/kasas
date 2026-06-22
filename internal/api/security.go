package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/apikeys"
	"github.com/paulmeier/kasas/internal/db"
)

// apiKeyTouchInterval throttles the last-used write: a key's last_used_at is bumped
// at most this often (seconds), so verifying a key on every request does not amount
// to a write per request.
const apiKeyTouchInterval = 60

// Authenticator gates the REST API and the MCP-over-HTTP server behind the
// dashboard token. It is implemented by *auth.Guard. A nil Authenticator, or one
// reporting Required()==false, leaves those surfaces open, so the API is
// unauthenticated by default (kasas warns at startup when no token is set).
type Authenticator interface {
	// Required reports whether a token is active (requests must authenticate).
	Required() bool
	// Valid reports whether the presented token matches the active one.
	Valid(token string) bool
	// Source reports where the active token comes from: "config", "stored", or "none".
	Source() string
	// Generate mints, stores, and returns a strong random token.
	Generate(ctx context.Context) (string, error)
	// SetToken stores a caller-supplied token.
	SetToken(ctx context.Context, token string) error
	// ClearToken removes the stored token.
	ClearToken(ctx context.Context) error
}

// tokenSourceConfig is the Source() value meaning the token is config/env-managed
// and therefore not editable from the dashboard.
const tokenSourceConfig = "config"

// requireToken rejects requests that do not carry the active dashboard token in an
// "Authorization: Bearer <token>" header. It is a no-op when auth is disabled (no
// Authenticator, or no token set), keeping the API open by default.
func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil || !s.auth.Required() {
			next.ServeHTTP(w, r)
			return
		}
		if !s.auth.Valid(bearerToken(r)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kasas"`)
			s.writeError(w, http.StatusUnauthorized, "missing or invalid dashboard token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireRead gates a route to any authenticated principal: the dashboard token, or a
// valid API key of any scope. Like requireToken it is a no-op when auth is disabled.
func (s *Server) requireRead(next http.Handler) http.Handler {
	return s.scopeGate(next, apikeys.ScopeRead)
}

// requireWrite gates a route to the dashboard token or a read_write API key. A
// read-only key is authenticated but rejected with 403.
func (s *Server) requireWrite(next http.Handler) http.Handler {
	return s.scopeGate(next, apikeys.ScopeReadWrite)
}

// scopeGate admits the dashboard token (which always grants full access) or an API
// key whose scope satisfies need; otherwise it rejects (401 unauthenticated, 403
// authenticated-but-under-scoped). It is a no-op when auth is disabled, keeping the
// API open by default. Admin/provisioning routes use requireToken instead, which
// never accepts an API key, so a key can never escalate to managing credentials.
func (s *Server) scopeGate(next http.Handler, need apikeys.Scope) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil || !s.auth.Required() {
			next.ServeHTTP(w, r)
			return
		}
		presented := bearerToken(r)
		if s.auth.Valid(presented) {
			next.ServeHTTP(w, r) // dashboard token: full access
			return
		}
		if key, ok := s.verifyAPIKey(r.Context(), presented); ok {
			if apikeys.Scope(key.Scope).Satisfies(need) {
				next.ServeHTTP(w, r)
				return
			}
			s.writeError(w, http.StatusForbidden, "this API key lacks the required scope ("+string(need)+")")
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="kasas"`)
		s.writeError(w, http.StatusUnauthorized, "missing or invalid credentials")
	})
}

// requireConfiguredToken gates the dangerous admin operations — loading and running
// plugin code, applying a self-update, minting API keys, managing webhooks, writing
// settings, setting source credentials, restarting. Unlike requireToken, it refuses
// the request on an UNSECURED instance (a wired Authenticator reporting
// Required()==false) with 503, so these operations are never reachable without a
// dashboard token; the operator secures the instance first via the always-open token
// bootstrap (POST /api/v1/security/token). When no Authenticator is wired at all (a
// degenerate/test configuration — production always wires one) it falls through open,
// matching requireToken/scopeGate. API keys are never accepted, only the dashboard token.
func (s *Server) requireConfiguredToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.auth == nil {
			next.ServeHTTP(w, r)
			return
		}
		if !s.auth.Required() {
			s.writeError(w, http.StatusServiceUnavailable, "this operation requires a dashboard token; set one (dashboard.token / KASAS_DASHBOARD_TOKEN) or generate it from the dashboard Settings page first")
			return
		}
		if !s.auth.Valid(bearerToken(r)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kasas"`)
			s.writeError(w, http.StatusUnauthorized, "missing or invalid dashboard token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// verifyAPIKey resolves a presented bearer to a stored API key by hashing it and
// looking it up (an O(1) unique-index hit). On a match it touches the key's
// last-used time, throttled so it is not a write per request. A miss (unknown key or
// a read error) reports false, i.e. unauthorized.
func (s *Server) verifyAPIKey(ctx context.Context, presented string) (db.ApiKey, bool) {
	if presented == "" {
		return db.ApiKey{}, false
	}
	key, err := s.store.GetApiKeyByHash(ctx, apikeys.Hash(presented))
	if err != nil {
		return db.ApiKey{}, false
	}
	if now := time.Now().Unix(); now-key.LastUsedAt >= apiKeyTouchInterval {
		if uerr := s.store.UpdateApiKeyLastUsed(ctx, db.UpdateApiKeyLastUsedParams{LastUsedAt: now, ID: key.ID}); uerr != nil {
			s.logger.Warn("update api key last_used failed", "key_id", key.ID, "error", uerr)
		}
	}
	return key, true
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header,
// returning "" when it is absent or malformed. The scheme is matched
// case-insensitively per RFC 7235.
func bearerToken(r *http.Request) string {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// authStatusResponse drives the dashboard's login gate and "unsecured" banner.
type authStatusResponse struct {
	AuthRequired  bool `json:"auth_required"`
	Authenticated bool `json:"authenticated"`
}

// handleAuthStatus reports whether a token is required and whether the caller's
// token (if any) is valid. It is intentionally unauthenticated so the dashboard
// can decide whether to show a login screen before it holds a token.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	resp := authStatusResponse{AuthRequired: false, Authenticated: true}
	if s.auth != nil && s.auth.Required() {
		resp.AuthRequired = true
		resp.Authenticated = s.auth.Valid(bearerToken(r))
	}
	s.writeJSON(w, http.StatusOK, resp)
}

// setTokenRequest optionally carries a custom token; an empty/absent token means
// "generate a strong random one".
type setTokenRequest struct {
	Token string `json:"token"`
}

// tokenResponse is returned after generating or setting a token. Token is the new
// value, shown to the user once; it is omitted for responses that mint nothing
// (e.g. clearing the token).
type tokenResponse struct {
	Token        string `json:"token,omitempty"`
	AuthRequired bool   `json:"auth_required"`
	TokenSource  string `json:"token_source"`
}

// handleSetToken generates (empty body) or sets (a custom token) the stored
// dashboard token. It is refused with 409 when the token is config-managed.
func (s *Server) handleSetToken(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeError(w, http.StatusServiceUnavailable, "token management is not available")
		return
	}
	if s.auth.Source() == tokenSourceConfig {
		s.writeError(w, http.StatusConflict, "dashboard token is managed via configuration; remove dashboard.token / KASAS_DASHBOARD_TOKEN to manage it here")
		return
	}

	var req setTokenRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var (
		token string
		err   error
	)
	if strings.TrimSpace(req.Token) == "" {
		token, err = s.auth.Generate(r.Context())
	} else {
		token = strings.TrimSpace(req.Token)
		err = s.auth.SetToken(r.Context(), token)
	}
	if err != nil {
		s.logger.Warn("set dashboard token failed", "error", err)
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, tokenResponse{
		Token:        token,
		AuthRequired: s.auth.Required(),
		TokenSource:  s.auth.Source(),
	})
}

// handleClearToken removes the stored token (disabling auth unless a config token
// is set). Refused with 409 when config-managed.
func (s *Server) handleClearToken(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		s.writeError(w, http.StatusServiceUnavailable, "token management is not available")
		return
	}
	if s.auth.Source() == tokenSourceConfig {
		s.writeError(w, http.StatusConflict, "dashboard token is managed via configuration; remove dashboard.token / KASAS_DASHBOARD_TOKEN to manage it here")
		return
	}
	if err := s.auth.ClearToken(r.Context()); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, tokenResponse{
		AuthRequired: s.auth.Required(),
		TokenSource:  s.auth.Source(),
	})
}
