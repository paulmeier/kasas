package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/apikeys"
	"github.com/paulmeier/kasas/internal/db"
)

// ApiKeyDTO is the JSON representation of an API key. The secret is never included
// except in the create response (Key, shown exactly once); list/get return only
// metadata. LastUsedAt is null until the key is first presented.
type ApiKeyDTO struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Key        string     `json:"key,omitempty"`
}

func toApiKeyDTO(k db.ApiKey) ApiKeyDTO {
	dto := ApiKeyDTO{
		ID:        k.ID,
		Name:      k.Name,
		Prefix:    k.Prefix,
		Scope:     k.Scope,
		CreatedAt: unixTime(k.CreatedAt),
	}
	if k.LastUsedAt > 0 {
		t := unixTime(k.LastUsedAt)
		dto.LastUsedAt = &t
	}
	return dto
}

func toApiKeyDTOs(in []db.ApiKey) []ApiKeyDTO {
	out := make([]ApiKeyDTO, len(in))
	for i, k := range in {
		out[i] = toApiKeyDTO(k)
	}
	return out
}

// apiKeyInput is the create request body (and the MCP create tool input). Name is
// optional; scope defaults to read (least privilege) when omitted.
type apiKeyInput struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

// createApiKey validates the scope, mints a key, stores only its hash, and returns
// the stored row plus the full secret (shown to the caller once). Shared by REST and
// MCP. Validation failures are validationError so handlers map them to 400.
func (s *Server) createApiKey(ctx context.Context, in apiKeyInput) (db.ApiKey, string, error) {
	scope, err := apikeys.ParseScope(in.Scope)
	if err != nil {
		return db.ApiKey{}, "", validationError{err}
	}
	full, prefix, hash, err := apikeys.Generate()
	if err != nil {
		return db.ApiKey{}, "", err
	}
	key, err := s.store.InsertApiKey(ctx, db.InsertApiKeyParams{
		Name:       strings.TrimSpace(in.Name),
		Prefix:     prefix,
		KeyHash:    hash,
		Scope:      string(scope),
		CreatedAt:  time.Now().Unix(),
		LastUsedAt: 0,
	})
	if err != nil {
		return db.ApiKey{}, "", err
	}
	return key, full, nil
}

// revokeApiKey deletes a key by id, reporting whether one was removed (so callers
// map a miss to 404 / a not-found tool error). Shared by REST and MCP.
func (s *Server) revokeApiKey(ctx context.Context, id int64) (bool, error) {
	n, err := s.store.DeleteApiKey(ctx, id)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// --- REST handlers ---

func (s *Server) handleCreateApiKey(w http.ResponseWriter, r *http.Request) {
	var in apiKeyInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	if err := dec.Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key, full, err := s.createApiKey(r.Context(), in)
	if err != nil {
		if isValidationError(err) {
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "create api key", err)
		return
	}
	dto := toApiKeyDTO(key)
	dto.Key = full
	s.writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) handleListApiKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListApiKeys(r.Context())
	if err != nil {
		s.serverError(w, "list api keys", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"api_keys": toApiKeyDTOs(keys)})
}

func (s *Server) handleRevokeApiKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid api key id")
		return
	}
	revoked, err := s.revokeApiKey(r.Context(), id)
	if err != nil {
		s.serverError(w, "revoke api key", err)
		return
	}
	if !revoked {
		s.writeError(w, http.StatusNotFound, "api key not found")
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true})
}

// --- MCP tool input/output types + handlers (registered in mcp.go) ---

type listApiKeysOutput struct {
	APIKeys []ApiKeyDTO `json:"api_keys"`
}

type createApiKeyInput struct {
	Name  string `json:"name,omitempty" jsonschema:"optional human-readable name for the key"`
	Scope string `json:"scope,omitempty" jsonschema:"access level: read (GET only) or read_write (GET + mutations); defaults to read"`
}

type revokeApiKeyInput struct {
	ID int64 `json:"id" jsonschema:"the id of the API key to revoke"`
}

type revokeApiKeyOutput struct {
	ID      int64 `json:"id"`
	Revoked bool  `json:"revoked"`
}

func (s *Server) mcpListApiKeys(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listApiKeysOutput, error) {
	keys, err := s.store.ListApiKeys(ctx)
	if err != nil {
		return nil, listApiKeysOutput{}, err
	}
	return &mcp.CallToolResult{}, listApiKeysOutput{APIKeys: toApiKeyDTOs(keys)}, nil
}

func (s *Server) mcpCreateApiKey(ctx context.Context, _ *mcp.CallToolRequest, in createApiKeyInput) (*mcp.CallToolResult, ApiKeyDTO, error) {
	key, full, err := s.createApiKey(ctx, apiKeyInput(in))
	if err != nil {
		return nil, ApiKeyDTO{}, err
	}
	dto := toApiKeyDTO(key)
	dto.Key = full // the full secret, returned once
	return &mcp.CallToolResult{}, dto, nil
}

func (s *Server) mcpRevokeApiKey(ctx context.Context, _ *mcp.CallToolRequest, in revokeApiKeyInput) (*mcp.CallToolResult, revokeApiKeyOutput, error) {
	revoked, err := s.revokeApiKey(ctx, in.ID)
	if err != nil {
		return nil, revokeApiKeyOutput{}, err
	}
	if !revoked {
		return nil, revokeApiKeyOutput{}, fmt.Errorf("api key %d not found", in.ID)
	}
	return &mcp.CallToolResult{}, revokeApiKeyOutput{ID: in.ID, Revoked: true}, nil
}
