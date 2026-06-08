package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/plugins"
)

// PluginDTO is the JSON representation of an installed plugin: its identity and
// manifest-declared hooks/capabilities, the operator-granted capabilities, whether
// it is enabled and currently loaded, and the health of its most recent run.
type PluginDTO struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Runtime      string   `json:"runtime"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description,omitempty"`
	Enabled      bool     `json:"enabled"`
	Loaded       bool     `json:"loaded"`
	OnDisk       bool     `json:"on_disk"`
	State        string   `json:"state"` // loaded | disabled | error | missing
	Hooks        []string `json:"hooks"`
	Capabilities []string `json:"capabilities"`         // requested by the manifest
	Granted      []string `json:"granted_capabilities"` // granted by the operator/DB

	LastStatus    int64      `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	LastRunAt     *time.Time `json:"last_run_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
}

func toPluginDTO(s plugins.Status) PluginDTO {
	dto := PluginDTO{
		ID:           s.ID,
		Name:         s.Name,
		Runtime:      s.Runtime,
		Version:      s.Version,
		Description:  s.Description,
		Enabled:      s.Enabled,
		Loaded:       s.Loaded,
		OnDisk:       s.OnDisk,
		State:        s.State,
		Hooks:        hooksToStrings(s.Hooks),
		Capabilities: capsToStrings(s.Requested),
		Granted:      capsToStrings(s.Granted),
		LastStatus:   s.LastStatus,
		LastError:    s.LastError,
	}
	if s.LastRunAt > 0 {
		t := unixTime(s.LastRunAt)
		dto.LastRunAt = &t
	}
	if s.LastSuccessAt > 0 {
		t := unixTime(s.LastSuccessAt)
		dto.LastSuccessAt = &t
	}
	return dto
}

func toPluginDTOs(in []plugins.Status) []PluginDTO {
	out := make([]PluginDTO, len(in))
	for i, s := range in {
		out[i] = toPluginDTO(s)
	}
	return out
}

func hooksToStrings(hs []plugins.Hook) []string {
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = string(h)
	}
	return out
}

func capsToStrings(cs []plugins.Capability) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return out
}

func pluginIDParam(r *http.Request) (int64, error) {
	return strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
}

// writePluginMutationError maps a manager error from an enable/disable/reload to
// the right status. A load/discovery failure is the operator-actionable common
// case, so it is a 400 carrying the reason.
func (s *Server) writePluginMutationError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin system is disabled")
	case errors.Is(err, plugins.ErrPluginNotFound):
		s.writeError(w, http.StatusNotFound, "plugin not found")
	default:
		s.logger.Warn("plugin "+op+" failed", "error", err)
		s.writeError(w, http.StatusBadRequest, err.Error())
	}
}

// --- REST handlers ---

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	// A nil manager (plugin system disabled) is a no-op that returns ErrDisabled;
	// report it as an empty, disabled list rather than an error so the dashboard's
	// always-present Plugins page can show a clean "disabled" state.
	statuses, err := s.pluginMgr.List(r.Context())
	if errors.Is(err, plugins.ErrDisabled) {
		s.writeJSON(w, http.StatusOK, listPluginsOutput{Enabled: false, Plugins: []PluginDTO{}})
		return
	}
	if err != nil {
		s.serverError(w, "list plugins", err)
		return
	}
	s.writeJSON(w, http.StatusOK, listPluginsOutput{Enabled: true, Plugins: toPluginDTOs(statuses)})
}

func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	st, err := s.pluginMgr.Get(r.Context(), id)
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin system is disabled")
	case errors.Is(err, plugins.ErrPluginNotFound):
		s.writeError(w, http.StatusNotFound, "plugin not found")
	case err != nil:
		s.serverError(w, "get plugin", err)
	default:
		s.writeJSON(w, http.StatusOK, toPluginDTO(st))
	}
}

func (s *Server) handleEnablePlugin(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, true)
}
func (s *Server) handleDisablePlugin(w http.ResponseWriter, r *http.Request) {
	s.setPluginEnabled(w, r, false)
}

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	st, err := s.pluginMgr.SetEnabled(r.Context(), id, enabled)
	if err != nil {
		op := "enable plugin"
		if !enabled {
			op = "disable plugin"
		}
		s.writePluginMutationError(w, op, err)
		return
	}
	s.writeJSON(w, http.StatusOK, toPluginDTO(st))
}

func (s *Server) handleReloadPlugin(w http.ResponseWriter, r *http.Request) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	st, err := s.pluginMgr.Reload(r.Context(), id)
	if err != nil {
		s.writePluginMutationError(w, "reload plugin", err)
		return
	}
	s.writeJSON(w, http.StatusOK, toPluginDTO(st))
}

// --- MCP tool input/output types + handlers (registered in mcp.go) ---

type listPluginsOutput struct {
	Enabled bool        `json:"enabled"` // false when the plugin system is disabled
	Plugins []PluginDTO `json:"plugins"`
}

type pluginIDInput struct {
	ID int64 `json:"id" jsonschema:"the id of the plugin"`
}

func (s *Server) mcpListPlugins(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listPluginsOutput, error) {
	statuses, err := s.pluginMgr.List(ctx)
	if errors.Is(err, plugins.ErrDisabled) {
		return &mcp.CallToolResult{}, listPluginsOutput{Enabled: false, Plugins: []PluginDTO{}}, nil
	}
	if err != nil {
		return nil, listPluginsOutput{}, err
	}
	return &mcp.CallToolResult{}, listPluginsOutput{Enabled: true, Plugins: toPluginDTOs(statuses)}, nil
}

func (s *Server) mcpGetPlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, PluginDTO, error) {
	st, err := s.pluginMgr.Get(ctx, in.ID)
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, PluginDTO{}, fmt.Errorf("plugin %d not found", in.ID)
	}
	if err != nil {
		return nil, PluginDTO{}, err
	}
	return &mcp.CallToolResult{}, toPluginDTO(st), nil
}

func (s *Server) mcpEnablePlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, PluginDTO, error) {
	return s.mcpSetPluginEnabled(ctx, in.ID, true)
}

func (s *Server) mcpDisablePlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, PluginDTO, error) {
	return s.mcpSetPluginEnabled(ctx, in.ID, false)
}

func (s *Server) mcpSetPluginEnabled(ctx context.Context, id int64, enabled bool) (*mcp.CallToolResult, PluginDTO, error) {
	st, err := s.pluginMgr.SetEnabled(ctx, id, enabled)
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, PluginDTO{}, fmt.Errorf("plugin %d not found", id)
	}
	if err != nil {
		return nil, PluginDTO{}, err
	}
	return &mcp.CallToolResult{}, toPluginDTO(st), nil
}

func (s *Server) mcpReloadPlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, PluginDTO, error) {
	st, err := s.pluginMgr.Reload(ctx, in.ID)
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, PluginDTO{}, fmt.Errorf("plugin %d not found", in.ID)
	}
	if err != nil {
		return nil, PluginDTO{}, err
	}
	return &mcp.CallToolResult{}, toPluginDTO(st), nil
}
