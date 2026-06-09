package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/plugins"
)

// Plugin dashboard pages: a plugin with a [ui] manifest block (and the ui:page
// grant) contributes a sidebar entry and a declaratively rendered page. The
// dashboard lists the entries via GET /plugins/pages, renders one via
// GET /plugins/pages/{name}, and posts button presses to
// POST /plugins/pages/{name}/action. The page document is produced by the plugin
// VM and validated/normalized by plugins.ValidatePageDoc before it gets here, so
// the handler can pass it through as raw JSON.

// PluginPageInfoDTO is one sidebar entry contributed by a plugin.
type PluginPageInfoDTO struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Icon  string `json:"icon"`
}

type listPluginPagesOutput struct {
	Pages []PluginPageInfoDTO `json:"pages"`
}

// pluginPageResponse wraps the validated page document.
type pluginPageResponse struct {
	Name string          `json:"name"`
	Page json.RawMessage `json:"page"`
}

// pageActionInput is the POST body of a page action: the id of the pressed
// button (declared by a previous render) and its params.
type pageActionInput struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params"`
}

func (s *Server) handleListPluginPages(w http.ResponseWriter, _ *http.Request) {
	// A nil manager (plugin system disabled) simply contributes no pages; the
	// sidebar shows the built-in entries only.
	infos := s.pluginMgr.Pages()
	out := listPluginPagesOutput{Pages: make([]PluginPageInfoDTO, len(infos))}
	for i, p := range infos {
		out.Pages[i] = PluginPageInfoDTO{Name: p.Name, Title: p.Title, Icon: p.Icon}
	}
	s.writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRenderPluginPage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	doc, err := s.pluginMgr.RenderPage(r.Context(), name, plugins.PageRequest{})
	if err != nil {
		s.writePluginPageError(w, name, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pluginPageResponse{Name: name, Page: doc})
}

func (s *Server) handlePluginPageAction(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var in pageActionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ID == "" {
		s.writeError(w, http.StatusBadRequest, "body must be JSON with a non-empty action id")
		return
	}
	doc, err := s.pluginMgr.RenderPage(r.Context(), name, plugins.PageRequest{Action: in.ID, Params: in.Params})
	if err != nil {
		s.writePluginPageError(w, name, err)
		return
	}
	s.writeJSON(w, http.StatusOK, pluginPageResponse{Name: name, Page: doc})
}

// writePluginPageError maps a page render/action failure: unknown or page-less
// plugins are 404s, a disabled plugin system is a 503, and anything else (a hook
// error, an invalid page document, a timeout) is a 502 — the upstream "plugin"
// failed, not kasas.
func (s *Server) writePluginPageError(w http.ResponseWriter, name string, err error) {
	switch {
	case errors.Is(err, plugins.ErrDisabled):
		s.writeError(w, http.StatusServiceUnavailable, "plugin system is disabled")
	case errors.Is(err, plugins.ErrPluginNotFound), errors.Is(err, plugins.ErrNoPage):
		s.writeError(w, http.StatusNotFound, "plugin page not found")
	default:
		s.logger.Warn("plugin page failed", "plugin", name, "error", err)
		s.writeError(w, http.StatusBadGateway, "plugin page failed: "+err.Error())
	}
}
