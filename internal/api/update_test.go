package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/selfupdate"
	"github.com/paulmeier/kasas/internal/testutil"
)

// fakeChecker implements api.UpdateChecker without any network access.
type fakeChecker struct {
	status  selfupdate.Status
	release *selfupdate.Release
	relErr  error
}

func (f *fakeChecker) Status(context.Context) selfupdate.Status { return f.status }
func (f *fakeChecker) LatestRelease(context.Context) (*selfupdate.Release, error) {
	return f.release, f.relErr
}

func newUpdateServer(t *testing.T, checker api.UpdateChecker, allowApply bool) *httptest.Server {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	opts := api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	}
	if checker != nil {
		opts.UpdateChecker = checker
		opts.AllowApply = allowApply
	}
	srv := httptest.NewServer(api.New(opts).Router())
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateStatusEndpoint(t *testing.T) {
	checker := &fakeChecker{status: selfupdate.Status{
		Current: "v1.0.0", Latest: "v1.2.0", Available: true, URL: "https://example.test/r",
	}}
	srv := newUpdateServer(t, checker, true)

	resp, err := http.Get(srv.URL + "/api/v1/update")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, true, out["update_available"])
	assert.Equal(t, "v1.2.0", out["latest"])
	assert.Equal(t, true, out["can_apply"])
}

func TestUpdateEndpointHiddenWhenDisabled(t *testing.T) {
	srv := newUpdateServer(t, nil, false) // no checker → route not registered
	resp, err := http.Get(srv.URL + "/api/v1/update")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestApplyEndpointNotRegisteredWhenApplyDisabled(t *testing.T) {
	checker := &fakeChecker{status: selfupdate.Status{Current: "v1.0.0", Latest: "v1.2.0", Available: true}}
	srv := newUpdateServer(t, checker, false) // status on, apply off

	// GET still works (the banner can show), but POST is not allowed.
	g, err := http.Get(srv.URL + "/api/v1/update")
	require.NoError(t, err)
	g.Body.Close()
	assert.Equal(t, http.StatusOK, g.StatusCode)

	resp, err := http.Post(srv.URL+"/api/v1/update", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}

func TestApplyUpdateAlreadyLatest(t *testing.T) {
	// The server version "test" is unparseable, so any release is "not newer":
	// apply reports up-to-date without touching the binary.
	checker := &fakeChecker{release: &selfupdate.Release{Version: "v1.2.0"}}
	srv := newUpdateServer(t, checker, true)

	resp, err := http.Post(srv.URL+"/api/v1/update", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var out map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	assert.Equal(t, false, out["updated"])
}
