package api

import (
	"context"
	"net/http"
	"time"

	"github.com/paulmeier/kasas/internal/selfupdate"
)

// handleUpdateStatus reports whether a newer release is available. It backs the
// dashboard's update banner and is cheap (the checker caches GitHub results).
func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	st := s.updates.Status(ctx)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"current":          st.Current,
		"latest":           st.Latest,
		"update_available": st.Available,
		"release_url":      st.URL,
		"checked_at":       st.CheckedAt,
		"can_apply":        s.allowApply,
	})
}

// handleApplyUpdate downloads, verifies, and installs the latest release over
// the running binary, then (if a Restart hook is configured) re-execs into it.
// Guarded by update.allow_apply; the route is only registered when enabled.
func (s *Server) handleApplyUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.allowApply {
		s.writeError(w, http.StatusForbidden, "self-update via the API is disabled")
		return
	}

	// A download + checksum + replace can take a little while; don't tie it to
	// the request context, which the client may cancel as the server restarts.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	rel, err := s.updates.LatestRelease(ctx)
	if err != nil {
		s.serverError(w, "check latest release", err)
		return
	}
	if !rel.IsNewerThan(s.version) {
		s.writeJSON(w, http.StatusOK, map[string]any{
			"updated": false,
			"version": s.version,
			"message": "already on the latest release",
		})
		return
	}

	if err := selfupdate.Apply(ctx, rel, selfupdate.ApplyOptions{Logger: s.logger}); err != nil {
		s.logger.Error("dashboard self-update failed", "error", err)
		// Surface the reason (e.g. permission denied) to the UI; this is a
		// self-hosted, trusted-network tool.
		s.writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	s.logger.Info("self-update applied via dashboard", "version", rel.Version)

	restarting := s.restart != nil
	msg := "Updated to " + rel.Version + ". Restart kasas to run the new version."
	if restarting {
		msg = "Updated to " + rel.Version + ". Restarting…"
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"updated":    true,
		"version":    rel.Version,
		"restarting": restarting,
		"message":    msg,
	})
	if restarting {
		s.restart() // re-exec happens after a short delay so this response flushes
	}
}
