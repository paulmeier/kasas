package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/paulmeier/kasas/internal/settings"
)

// handleListSettings returns every editable setting with its effective value,
// override state, and whether a restart is pending. It always responds 200
// (with enabled=false when settings management is unavailable) so the
// dashboard's Settings page can distinguish a disabled state from a routing
// error. Secret values are never included.
func (s *Server) handleListSettings(w http.ResponseWriter, r *http.Request) {
	if s.settingsSvc == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"enabled": false, "restart_required": false, "settings": []settings.Status{},
		})
		return
	}
	list, restart, err := s.settingsSvc.List(r.Context())
	if err != nil {
		s.serverError(w, "list settings", err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "restart_required": restart, "settings": list,
	})
}

// setSettingRequest is the body of PUT /settings/{key}. Value is usually a JSON
// string ("true", "6h", ...); a non-string JSON value (a bool, number, or the
// csv.folders array) is accepted verbatim as its raw text for convenience.
type setSettingRequest struct {
	Value json.RawMessage `json:"value"`
}

func (r setSettingRequest) text() string {
	var sv string
	if err := json.Unmarshal(r.Value, &sv); err == nil {
		return sv
	}
	return strings.TrimSpace(string(r.Value))
}

// handleSetSetting validates and persists one setting override. The change is
// permanent (it survives restarts and wins over the config file / environment)
// and takes effect at the next restart.
func (s *Server) handleSetSetting(w http.ResponseWriter, r *http.Request) {
	if s.settingsSvc == nil {
		s.writeError(w, http.StatusServiceUnavailable, "settings management is not available")
		return
	}
	key := chi.URLParam(r, "key")

	var req setSettingRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)) // csv.folders JSON can be sizeable
	if err := dec.Decode(&req); err != nil || len(req.Value) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid request body: want {\"value\": ...}")
		return
	}

	st, restart, err := s.settingsSvc.Set(r.Context(), key, req.text())
	if errors.Is(err, settings.ErrUnknownKey) {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "could not set setting: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"setting": st, "restart_required": restart})
}

// handleResetSetting removes a stored override so the config file / environment
// value applies again at the next restart.
func (s *Server) handleResetSetting(w http.ResponseWriter, r *http.Request) {
	if s.settingsSvc == nil {
		s.writeError(w, http.StatusServiceUnavailable, "settings management is not available")
		return
	}
	key := chi.URLParam(r, "key")

	st, restart, err := s.settingsSvc.Reset(r.Context(), key)
	if errors.Is(err, settings.ErrUnknownKey) {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "could not reset setting: "+err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"setting": st, "restart_required": restart})
}

// handleRestart re-execs the running binary in place so pending setting changes
// take effect — the same mechanism a dashboard-triggered self-update uses. The
// response is written first; the re-exec happens a moment later so it reaches
// the client.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.restart == nil {
		s.writeError(w, http.StatusServiceUnavailable, "restart is not available in this run mode")
		return
	}
	s.logger.Info("restart requested via API")
	s.restart()
	s.writeJSON(w, http.StatusOK, map[string]any{"restarting": true})
}
