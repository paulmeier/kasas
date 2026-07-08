package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/dbmigrate"
)

// handleMigratePostgres copies the running SQLite ledger into the Postgres
// database named by the request's DSN, preserving every row and id. It backs the
// dashboard's "Migrate to Postgres" action. Admin-only (a configured token); the
// source database is only read, so the running instance keeps serving SQLite —
// the operator switches to Postgres by updating database.driver/database.dsn and
// restarting.
func (s *Server) handleMigratePostgres(w http.ResponseWriter, r *http.Request) {
	if s.sqliteDB == nil {
		s.writeError(w, http.StatusBadRequest,
			"database migration is only available when kasas is running on SQLite")
		return
	}

	var body struct {
		DSN string `json:"dsn"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)) // 16 KiB is plenty
	if err := dec.Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dsn := strings.TrimSpace(body.DSN)
	if dsn == "" {
		s.writeError(w, http.StatusBadRequest, "dsn is required")
		return
	}

	// A full copy can take a while on a large ledger; don't tie it to the request
	// context, which the browser may cancel before it finishes.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	report, err := dbmigrate.MigrateToPostgres(ctx, s.sqliteDB, dsn, s.logger)
	if err != nil {
		s.logger.Error("dashboard postgres migration failed", "error", err)
		// Surface the reason (unreachable host, non-empty target, ...) to the UI;
		// this is a self-hosted, trusted-network tool.
		s.writeError(w, http.StatusInternalServerError, "migration failed: "+err.Error())
		return
	}
	s.logger.Info("postgres migration completed via dashboard", "rows", report.Total, "tables", len(report.Tables))

	s.writeJSON(w, http.StatusOK, map[string]any{
		"migrated":   true,
		"total_rows": report.Total,
		"tables":     report.Tables,
		"message": "Copied " + strconv.FormatInt(report.Total, 10) + " rows into Postgres. " +
			"Set database.driver=postgres and database.dsn, then restart kasas to use it.",
	})
}
