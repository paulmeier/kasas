package api_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/api"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/testutil"
)

// pagerLua backs a plugin with a dashboard page: a render hook and an action
// hook that echoes its input.
const pagerLua = `
function OnPageRender(req)
  return {
    title = "Pager",
    blocks = {
      { type = "stat", label = "Status", value = "ok" },
      { type = "actions", actions = { { id = "bump", label = "Bump" } } },
    },
  }
end

function OnPageAction(req)
  return { blocks = { { type = "text", text = "ran " .. req.action } } }
end
`

const pagerManifest = `name="pager"
runtime="lua"
hooks=["OnPageRender","OnPageAction"]
capabilities=["ui:page"]

[ui]
title = "Pager"
icon  = "chart"
`

// newPluginPageServer is newPluginTestServer with a page-bearing plugin, with
// the plugin enabled (pages only exist for loaded plugins).
func newPluginPageServer(t *testing.T) *httptest.Server {
	t.Helper()
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)

	dir := t.TempDir()
	pdir := filepath.Join(dir, "pager")
	require.NoError(t, os.MkdirAll(pdir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "plugin.toml"), []byte(pagerManifest), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(pdir, "main.lua"), []byte(pagerLua), 0o644))

	bus := events.NewBus()
	mgr := plugins.NewManager(plugins.Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes: map[string]plugins.Runtime{plugins.RuntimeLua: plugins.NewLuaRuntime()},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s := api.New(api.Options{
		Store:         store,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:       "test",
		PluginManager: mgr,
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	var list struct {
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	require.Len(t, list.Plugins, 1)
	id := strconv.FormatInt(list.Plugins[0].ID, 10)
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/"+id+"/enable", nil, nil))
	return srv
}

func TestPluginPagesListRenderAction(t *testing.T) {
	srv := newPluginPageServer(t)

	var pages struct {
		Pages []api.PluginPageInfoDTO `json:"pages"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/pages", &pages))
	require.Len(t, pages.Pages, 1)
	assert.Equal(t, api.PluginPageInfoDTO{Name: "pager", Title: "Pager", Icon: "chart"}, pages.Pages[0])

	var rendered struct {
		Name string          `json:"name"`
		Page json.RawMessage `json:"page"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/pages/pager", &rendered))
	assert.Equal(t, "pager", rendered.Name)
	var doc struct {
		Title  string `json:"title"`
		Blocks []struct {
			Type string `json:"type"`
		} `json:"blocks"`
	}
	require.NoError(t, json.Unmarshal(rendered.Page, &doc))
	assert.Equal(t, "Pager", doc.Title)
	require.Len(t, doc.Blocks, 2)
	assert.Equal(t, "stat", doc.Blocks[0].Type)

	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/pages/pager/action",
		map[string]any{"id": "bump"}, &rendered))
	assert.Contains(t, string(rendered.Page), "ran bump")

	// An empty/missing action id is a 400.
	assert.Equal(t, http.StatusBadRequest, postJSON(t, srv, "/api/v1/plugins/pages/pager/action",
		map[string]any{}, nil))

	// Unknown plugin: 404.
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/plugins/pages/nope", nil))
}

func TestPluginPagesDisabledSystem(t *testing.T) {
	// No plugin manager wired: the page list is empty (sidebar shows built-ins
	// only) and rendering reports the disabled system.
	store := db.NewSQLiteStore(testutil.NewDB(t))
	testutil.Seed(t, store)
	s := api.New(api.Options{
		Store:   store,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	srv := httptest.NewServer(s.Router())
	t.Cleanup(srv.Close)

	var pages struct {
		Pages []api.PluginPageInfoDTO `json:"pages"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/pages", &pages))
	assert.Empty(t, pages.Pages)

	assert.Equal(t, http.StatusServiceUnavailable, getJSON(t, srv, "/api/v1/plugins/pages/pager", nil))
}

func TestPluginPageDisabledPluginIs404(t *testing.T) {
	srv := newPluginPageServer(t)

	var list struct {
		Plugins []api.PluginDTO `json:"plugins"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins", &list))
	id := strconv.FormatInt(list.Plugins[0].ID, 10)
	require.Equal(t, http.StatusOK, postJSON(t, srv, "/api/v1/plugins/"+id+"/disable", nil, nil))

	var pages struct {
		Pages []api.PluginPageInfoDTO `json:"pages"`
	}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/api/v1/plugins/pages", &pages))
	assert.Empty(t, pages.Pages, "a disabled plugin's page leaves the sidebar")
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/api/v1/plugins/pages/pager", nil))
}
