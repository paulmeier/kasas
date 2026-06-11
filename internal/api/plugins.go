package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// net:fetch (ADR 0002): NetAllow is the manifest-declared egress allowlist;
	// NetGrants is the subset of those hosts the operator has granted private/LAN
	// access to. Both empty/omitted for a plugin without net:fetch.
	NetAllow  []string `json:"net_allow,omitempty"`
	NetGrants []string `json:"net_grants,omitempty"`

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
		NetAllow:     s.NetAllow,
		NetGrants:    s.NetGrants,
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

// enablePluginRequest is the OPTIONAL JSON body of POST /plugins/{id}/enable. It
// carries the operator's net:fetch private-host grants — the subset of the
// plugin's declared [net].allow hosts it may reach on a private/LAN address.
// Absent (or on disable) leaves the stored grants untouched.
type enablePluginRequest struct {
	NetGrants []string `json:"net_grants"`
}

func (s *Server) setPluginEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	// Grants only apply when enabling, and the body is optional: a missing/empty body
	// means "no change" (nil), which is the common case and what older clients send.
	var grants []string
	if enabled && r.Body != nil && r.ContentLength != 0 {
		var req enablePluginRequest
		if derr := json.NewDecoder(r.Body).Decode(&req); derr != nil && !errors.Is(derr, io.EOF) {
			s.writeError(w, http.StatusBadRequest, "invalid request body: "+derr.Error())
			return
		}
		grants = req.NetGrants
	}
	st, err := s.pluginMgr.SetEnabled(r.Context(), id, enabled, grants)
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

// UninstallResultDTO is the JSON result of uninstalling a plugin: whether the
// plugin's OnUninstall cleanup hook ran and any error it produced. A hook error
// does not fail the uninstall (the plugin is still removed); it is reported so the
// operator knows the plugin's self-cleanup may have been incomplete.
type UninstallResultDTO struct {
	Name        string `json:"name"`
	Uninstalled bool   `json:"uninstalled"`
	HookRan     bool   `json:"hook_ran"`
	HookError   string `json:"hook_error,omitempty"`
}

func toUninstallResultDTO(r plugins.UninstallResult) UninstallResultDTO {
	return UninstallResultDTO{
		Name:        r.Name,
		Uninstalled: true,
		HookRan:     r.HookRan,
		HookError:   r.HookError,
	}
}

func (s *Server) handleUninstallPlugin(w http.ResponseWriter, r *http.Request) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	res, err := s.pluginMgr.Uninstall(r.Context(), id)
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin system is disabled")
	case errors.Is(err, plugins.ErrPluginNotFound):
		s.writeError(w, http.StatusNotFound, "plugin not found")
	case err != nil:
		s.serverError(w, "uninstall plugin", err)
	default:
		s.writeJSON(w, http.StatusOK, toUninstallResultDTO(res))
	}
}

// EgressEntryDTO is one recorded plugin net:fetch attempt (ADR 0002 #2 logging).
type EgressEntryDTO struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	URL        string    `json:"url"`
	Status     int       `json:"status"`
	Bytes      int64     `json:"bytes"`
	DurationMs int64     `json:"duration_ms"`
	Error      string    `json:"error,omitempty"`
}

type pluginEgressOutput struct {
	Enabled bool             `json:"enabled"` // false when the plugin system is disabled
	Entries []EgressEntryDTO `json:"entries"`
}

func toEgressDTOs(in []plugins.EgressEntry) []EgressEntryDTO {
	out := make([]EgressEntryDTO, len(in))
	for i, e := range in {
		out[i] = EgressEntryDTO{
			Time: e.Time, Method: e.Method, Host: e.Host, URL: e.URL,
			Status: e.Status, Bytes: e.Bytes, DurationMs: e.DurationMs, Error: e.Error,
		}
	}
	return out
}

// handleGetPluginEgress returns the recent net:fetch egress log for one plugin
// (read tier — it is observability, not a mutation). Like the other plugin reads
// it is nil-safe: a disabled plugin system reports an empty, disabled log rather
// than a routing error, so the dashboard renders a clean state.
func (s *Server) handleGetPluginEgress(w http.ResponseWriter, r *http.Request) {
	id, err := pluginIDParam(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid plugin id")
		return
	}
	entries, err := s.pluginMgr.EgressLog(r.Context(), id, egressPageLimit(r))
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeJSON(w, http.StatusOK, pluginEgressOutput{Enabled: false, Entries: []EgressEntryDTO{}})
	case errors.Is(err, plugins.ErrPluginNotFound):
		s.writeError(w, http.StatusNotFound, "plugin not found")
	case err != nil:
		s.serverError(w, "plugin egress log", err)
	default:
		s.writeJSON(w, http.StatusOK, pluginEgressOutput{Enabled: true, Entries: toEgressDTOs(entries)})
	}
}

// egressPageLimit reads an optional ?limit= (default 100), bounded by the ring's
// own cap inside the manager.
func egressPageLimit(r *http.Request) int {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 100
}

// --- MCP tool input/output types + handlers (registered in mcp.go) ---

type listPluginsOutput struct {
	Enabled bool        `json:"enabled"` // false when the plugin system is disabled
	Plugins []PluginDTO `json:"plugins"`
}

type pluginIDInput struct {
	ID int64 `json:"id" jsonschema:"the id of the plugin"`
}

// enablePluginInput is the input to the enable_plugin MCP tool: a plugin id plus
// the OPTIONAL net:fetch private-host grants (a subset of the plugin's declared
// [net].allow hosts the operator approves for private/LAN access). Omitting
// net_grants leaves the stored grants untouched.
type enablePluginInput struct {
	ID        int64    `json:"id" jsonschema:"the id of the plugin"`
	NetGrants []string `json:"net_grants,omitempty" jsonschema:"optional: for a net:fetch plugin, the subset of its declared [net].allow hosts to grant private/LAN access to (e.g. [\"paperless.lan\"]); omit to leave grants unchanged"`
}

// pluginEgressInput is the input to the plugin_egress_log MCP tool.
type pluginEgressInput struct {
	ID    int64 `json:"id" jsonschema:"the id of the plugin"`
	Limit int   `json:"limit,omitempty" jsonschema:"maximum number of egress entries to return (default 100, newest first)"`
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

func (s *Server) mcpEnablePlugin(ctx context.Context, _ *mcp.CallToolRequest, in enablePluginInput) (*mcp.CallToolResult, PluginDTO, error) {
	return s.mcpSetPluginEnabled(ctx, in.ID, true, in.NetGrants)
}

func (s *Server) mcpDisablePlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, PluginDTO, error) {
	return s.mcpSetPluginEnabled(ctx, in.ID, false, nil)
}

func (s *Server) mcpSetPluginEnabled(ctx context.Context, id int64, enabled bool, netGrants []string) (*mcp.CallToolResult, PluginDTO, error) {
	st, err := s.pluginMgr.SetEnabled(ctx, id, enabled, netGrants)
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, PluginDTO{}, fmt.Errorf("plugin %d not found", id)
	}
	if err != nil {
		return nil, PluginDTO{}, err
	}
	return &mcp.CallToolResult{}, toPluginDTO(st), nil
}

func (s *Server) mcpPluginEgressLog(ctx context.Context, _ *mcp.CallToolRequest, in pluginEgressInput) (*mcp.CallToolResult, pluginEgressOutput, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	entries, err := s.pluginMgr.EgressLog(ctx, in.ID, limit)
	if errors.Is(err, plugins.ErrDisabled) {
		return &mcp.CallToolResult{}, pluginEgressOutput{Enabled: false, Entries: []EgressEntryDTO{}}, nil
	}
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, pluginEgressOutput{}, fmt.Errorf("plugin %d not found", in.ID)
	}
	if err != nil {
		return nil, pluginEgressOutput{}, err
	}
	return &mcp.CallToolResult{}, pluginEgressOutput{Enabled: true, Entries: toEgressDTOs(entries)}, nil
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

func (s *Server) mcpUninstallPlugin(ctx context.Context, _ *mcp.CallToolRequest, in pluginIDInput) (*mcp.CallToolResult, UninstallResultDTO, error) {
	res, err := s.pluginMgr.Uninstall(ctx, in.ID)
	if errors.Is(err, plugins.ErrPluginNotFound) {
		return nil, UninstallResultDTO{}, fmt.Errorf("plugin %d not found", in.ID)
	}
	if err != nil {
		return nil, UninstallResultDTO{}, err
	}
	return &mcp.CallToolResult{}, toUninstallResultDTO(res), nil
}
