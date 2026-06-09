package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paulmeier/kasas/internal/plugins"
)

// RegistryPluginDTO is the JSON representation of one community-registry plugin as
// presented to the dashboard: the manifest metadata a user needs to decide whether
// to install, the capability tier to warn on, and this host's install state.
type RegistryPluginDTO struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Description    string   `json:"description"`
	Author         string   `json:"author"`
	License        string   `json:"license"`
	Homepage       string   `json:"homepage"`
	Runtime        string   `json:"runtime"`
	Hooks          []string `json:"hooks"`
	Capabilities   []string `json:"capabilities"`
	CapabilityTier string   `json:"capability_tier"`
	// UI is present when the plugin adds a dashboard page, so the Marketplace
	// page can badge it before install.
	UI *RegistryUIDTO `json:"ui,omitempty"`

	Installed        bool   `json:"installed"`
	InstalledVersion string `json:"installed_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

// RegistryUIDTO is the dashboard-page metadata of a registry plugin.
type RegistryUIDTO struct {
	Title string `json:"title"`
	Icon  string `json:"icon"`
}

func toRegistryPluginDTO(e plugins.CatalogEntry) RegistryPluginDTO {
	var ui *RegistryUIDTO
	if e.UI != nil {
		ui = &RegistryUIDTO{Title: e.UI.Title, Icon: e.UI.Icon}
	}
	return RegistryPluginDTO{
		Name:             e.Name,
		Version:          e.Version,
		Description:      e.Description,
		Author:           e.Author,
		License:          e.License,
		Homepage:         e.Homepage,
		Runtime:          e.Runtime,
		Hooks:            e.Hooks,
		Capabilities:     e.Capabilities,
		CapabilityTier:   e.CapabilityTier,
		UI:               ui,
		Installed:        e.Installed,
		InstalledVersion: e.InstalledVersion,
		UpdateAvailable:  e.UpdateAvailable,
	}
}

func toRegistryPluginDTOs(in []plugins.CatalogEntry) []RegistryPluginDTO {
	out := make([]RegistryPluginDTO, len(in))
	for i, e := range in {
		out[i] = toRegistryPluginDTO(e)
	}
	return out
}

// listRegistryOutput is the catalog response. Available is false when the registry
// is not configured, mirroring listPluginsOutput.Enabled so the dashboard's
// Marketplace page can render a clean "unavailable" state instead of an error.
type listRegistryOutput struct {
	Available bool                `json:"available"`
	Plugins   []RegistryPluginDTO `json:"plugins"`
}

// --- REST handlers ---

func (s *Server) handleListPluginRegistry(w http.ResponseWriter, r *http.Request) {
	entries, err := s.pluginMgr.Catalog(r.Context())
	switch {
	case errors.Is(err, plugins.ErrDisabled), errors.Is(err, plugins.ErrRegistryDisabled):
		s.writeJSON(w, http.StatusOK, listRegistryOutput{Available: false, Plugins: []RegistryPluginDTO{}})
	case err != nil:
		// A catalog fetch hits the network; report a failure as a bad-gateway so the
		// dashboard distinguishes "registry unreachable" from a kasas bug.
		s.logger.Warn("fetch plugin registry failed", "error", err)
		s.writeError(w, http.StatusBadGateway, "could not reach the plugin registry: "+err.Error())
	default:
		s.writeJSON(w, http.StatusOK, listRegistryOutput{Available: true, Plugins: toRegistryPluginDTOs(entries)})
	}
}

func (s *Server) handleInstallPlugin(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	st, err := s.pluginMgr.Install(r.Context(), name)
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin system is disabled")
	case errors.Is(err, plugins.ErrRegistryDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin registry is disabled")
	case err != nil:
		// Install failures are typically external (not in registry, download or
		// integrity failure); surface the reason at the gateway boundary.
		s.logger.Warn("install plugin failed", "plugin", name, "error", err)
		s.writeError(w, http.StatusBadGateway, err.Error())
	default:
		s.writeJSON(w, http.StatusOK, toPluginDTO(st))
	}
}

// --- MCP handlers (registered in mcp.go) ---

func (s *Server) mcpBrowsePluginRegistry(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listRegistryOutput, error) {
	entries, err := s.pluginMgr.Catalog(ctx)
	if errors.Is(err, plugins.ErrDisabled) || errors.Is(err, plugins.ErrRegistryDisabled) {
		return &mcp.CallToolResult{}, listRegistryOutput{Available: false, Plugins: []RegistryPluginDTO{}}, nil
	}
	if err != nil {
		return nil, listRegistryOutput{}, err
	}
	return &mcp.CallToolResult{}, listRegistryOutput{Available: true, Plugins: toRegistryPluginDTOs(entries)}, nil
}

type installPluginInput struct {
	Name string `json:"name" jsonschema:"the registry name of the plugin to install"`
}

func (s *Server) mcpInstallPlugin(ctx context.Context, _ *mcp.CallToolRequest, in installPluginInput) (*mcp.CallToolResult, PluginDTO, error) {
	st, err := s.pluginMgr.Install(ctx, in.Name)
	if err != nil {
		return nil, PluginDTO{}, err
	}
	return &mcp.CallToolResult{}, toPluginDTO(st), nil
}
