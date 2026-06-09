package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/testutil"
)

// --- manifest [ui] validation ---

func uiManifest(extra string) string {
	return `name="pager"
runtime="lua"
hooks=["OnPageRender","OnPageAction"]
capabilities=["ui:page"]
` + extra
}

func TestParseManifestUIValid(t *testing.T) {
	m, err := ParseManifest([]byte(uiManifest("[ui]\ntitle=\"Pager\"\nicon=\"chart\"")))
	require.NoError(t, err)
	require.NotNil(t, m.UI)
	assert.Equal(t, "Pager", m.UI.Title)
	assert.Equal(t, "chart", m.UI.Icon)
}

func TestParseManifestUIIconDefaults(t *testing.T) {
	m, err := ParseManifest([]byte(uiManifest("[ui]\ntitle=\"Pager\"")))
	require.NoError(t, err)
	assert.Equal(t, "puzzle", m.UI.Icon)
}

func TestParseManifestUIRejects(t *testing.T) {
	cases := map[string]struct {
		toml string
		want string
	}{
		"page hook without ui block": {
			toml: `name="x"` + "\n" + `runtime="lua"` + "\n" + `hooks=["OnPageRender"]`,
			want: "require a [ui] block",
		},
		"ui capability without ui block": {
			toml: `name="x"` + "\n" + `runtime="lua"` + "\n" + `hooks=["OnTransactionCreate"]` + "\n" + `capabilities=["ui:page"]`,
			want: "requires a [ui] block",
		},
		"ui block without render hook": {
			toml: `name="x"` + "\n" + `runtime="lua"` + "\n" + `hooks=["OnTransactionCreate"]` + "\n" + `capabilities=["ui:page"]` + "\n" + "[ui]\ntitle=\"X\"",
			want: "requires the OnPageRender hook",
		},
		"ui block without capability": {
			toml: `name="x"` + "\n" + `runtime="lua"` + "\n" + `hooks=["OnPageRender"]` + "\n" + "[ui]\ntitle=\"X\"",
			want: `requires the "ui:page" capability`,
		},
		"missing title": {
			toml: uiManifest("[ui]\nicon=\"chart\""),
			want: "ui.title is required",
		},
		"unknown icon": {
			toml: uiManifest("[ui]\ntitle=\"X\"\nicon=\"sparkles\""),
			want: "unknown ui.icon",
		},
		"title too long": {
			toml: uiManifest("[ui]\ntitle=\"" + strings.Repeat("x", maxUITitleLen+1) + "\""),
			want: "ui.title",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.toml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// --- page document validation ---

func TestValidatePageDocNormalizes(t *testing.T) {
	raw := json.RawMessage(`{
		"title": "My Page",
		"unknown_field": "dropped",
		"blocks": [
			{"type": "heading", "text": "Overview", "label": "stray"},
			{"type": "stat", "label": "Count", "value": 42, "hint": "this month"},
			{"type": "keyvalue", "items": [{"key": "k", "value": true}]},
			{"type": "table", "columns": ["A", "B"], "rows": [["1"], ["2", 3]]},
			{"type": "actions", "actions": [{"id": "refresh", "label": "Refresh", "style": "primary"}]},
			{"type": "divider"}
		]
	}`)
	out, err := ValidatePageDoc(raw)
	require.NoError(t, err)

	var doc PageDoc
	require.NoError(t, json.Unmarshal(out, &doc))
	assert.Equal(t, "My Page", doc.Title)
	require.Len(t, doc.Blocks, 6)
	assert.Equal(t, "", doc.Blocks[0].Label, "fields not used by the block type are cleared")
	assert.Equal(t, flexString("42"), doc.Blocks[1].Value, "numeric values normalize to strings")
	assert.Equal(t, flexString("true"), doc.Blocks[2].Items[0].Value)
	assert.Equal(t, flexString("3"), doc.Blocks[3].Rows[1][1])
	assert.NotContains(t, string(out), "unknown_field", "unknown fields are dropped by re-marshalling")
}

func TestValidatePageDocRejects(t *testing.T) {
	cases := map[string]string{
		"empty":            ``,
		"not json":         `nil`,
		"no blocks":        `{"blocks": []}`,
		"unknown type":     `{"blocks": [{"type": "iframe", "text": "x"}]}`,
		"heading no text":  `{"blocks": [{"type": "heading"}]}`,
		"stat no value":    `{"blocks": [{"type": "stat", "label": "x"}]}`,
		"table no columns": `{"blocks": [{"type": "table", "rows": [["a"]]}]}`,
		"row too wide":     `{"blocks": [{"type": "table", "columns": ["a"], "rows": [["1", "2"]]}]}`,
		"bad action id":    `{"blocks": [{"type": "actions", "actions": [{"id": "Bad ID!", "label": "x"}]}]}`,
		"bad action style": `{"blocks": [{"type": "actions", "actions": [{"id": "ok", "label": "x", "style": "rainbow"}]}]}`,
		"object value":     `{"blocks": [{"type": "stat", "label": "x", "value": {"nested": true}}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ValidatePageDoc(json.RawMessage(raw))
			assert.Error(t, err)
		})
	}
}

// --- runtime Render (Lua + JS) ---

const pagerLua = `
function OnPageRender(req)
  return {
    title = "Pager",
    blocks = {
      { type = "stat", label = "Plugin", value = req.plugin },
      { type = "text", text = "hello from lua" },
      { type = "actions", actions = { { id = "bump", label = "Bump" } } },
    },
  }
end

function OnPageAction(req)
  return {
    blocks = {
      { type = "text", text = "action=" .. req.action .. " n=" .. (req.params.n or "?") },
    },
  }
end
`

func loadPageInstance(t *testing.T, rt Runtime, entrypoint, src string) Instance {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, entrypoint), []byte(src), 0o644))
	m := Manifest{
		Name: "pager", Runtime: rt.Name(), Entrypoint: entrypoint,
		Hooks:        []Hook{HookPageRender, HookPageAction},
		Capabilities: []Capability{CapUIPage},
		Config:       map[string]any{},
		UI:           &UIManifest{Title: "Pager", Icon: "chart"},
	}
	inst, err := rt.Load(context.Background(), m, dir, newFakeHost())
	require.NoError(t, err)
	t.Cleanup(func() { _ = inst.Close() })
	return inst
}

func TestLuaRenderPage(t *testing.T) {
	inst := loadPageInstance(t, NewLuaRuntime(), "main.lua", pagerLua)

	raw, err := inst.Render(context.Background(), HookPageRender, PageRequest{Plugin: "pager"})
	require.NoError(t, err)
	norm, err := ValidatePageDoc(raw)
	require.NoError(t, err)

	var doc PageDoc
	require.NoError(t, json.Unmarshal(norm, &doc))
	assert.Equal(t, "Pager", doc.Title)
	require.Len(t, doc.Blocks, 3)
	assert.Equal(t, flexString("pager"), doc.Blocks[0].Value, "the request is passed to the hook")

	raw, err = inst.Render(context.Background(), HookPageAction,
		PageRequest{Plugin: "pager", Action: "bump", Params: map[string]string{"n": "7"}})
	require.NoError(t, err)
	_, err = ValidatePageDoc(raw)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "action=bump n=7")
}

func TestLuaRenderNilResultErrors(t *testing.T) {
	inst := loadPageInstance(t, NewLuaRuntime(), "main.lua",
		"function OnPageRender(req) end\nfunction OnPageAction(req) end")
	_, err := inst.Render(context.Background(), HookPageRender, PageRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned nil")
}

const pagerTS = `
interface PageRequest { plugin: string; action: string; params: Record<string, string>; }

function OnPageRender(req: PageRequest) {
  return {
    title: "Pager",
    blocks: [
      { type: "stat", label: "Plugin", value: req.plugin },
      { type: "table", columns: ["N"], rows: [[1], [2]] },
    ],
  };
}

function OnPageAction(req: PageRequest) {
  return { blocks: [{ type: "text", text: "action=" + req.action + " n=" + req.params.n }] };
}
`

func TestJSRenderPage(t *testing.T) {
	inst := loadPageInstance(t, NewJSRuntime(), "main.ts", pagerTS)

	raw, err := inst.Render(context.Background(), HookPageRender, PageRequest{Plugin: "pager"})
	require.NoError(t, err)
	norm, err := ValidatePageDoc(raw)
	require.NoError(t, err)

	var doc PageDoc
	require.NoError(t, json.Unmarshal(norm, &doc))
	assert.Equal(t, "Pager", doc.Title)
	require.Len(t, doc.Blocks, 2)
	assert.Equal(t, flexString("pager"), doc.Blocks[0].Value)
	assert.Equal(t, flexString("2"), doc.Blocks[1].Rows[1][0], "JS numbers normalize to strings")

	raw, err = inst.Render(context.Background(), HookPageAction,
		PageRequest{Plugin: "pager", Action: "bump", Params: map[string]string{"n": "7"}})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "action=bump n=7")
}

func TestJSRenderUndefinedResultErrors(t *testing.T) {
	inst := loadPageInstance(t, NewJSRuntime(), "main.js",
		"function OnPageRender(req) {}\nfunction OnPageAction(req) {}")
	_, err := inst.Render(context.Background(), HookPageRender, PageRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "returned nothing")
}

// --- manager Pages + RenderPage (through the worker queue) ---

const pagerManifest = `name="pager"
runtime="lua"
hooks=["OnPageRender","OnPageAction"]
capabilities=["ui:page"]

[ui]
title = "Pager"
icon  = "chart"
`

func TestManagerPagesAndRenderPage(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "pager", pagerManifest, pagerLua)

	store := testutil.NewStore(t)
	bus := events.NewBus()
	mgr := NewManager(Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})

	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	pg, ok := findByName(statuses, "pager")
	require.True(t, ok)

	// Disabled (not loaded): no sidebar entry, render is a 404-shaped error.
	assert.Empty(t, mgr.Pages())
	_, err = mgr.RenderPage(context.Background(), "pager", PageRequest{})
	assert.ErrorIs(t, err, ErrPluginNotFound)

	_, err = mgr.SetEnabled(context.Background(), pg.ID, true)
	require.NoError(t, err)
	defer mgr.unload("pager")

	pages := mgr.Pages()
	require.Len(t, pages, 1)
	assert.Equal(t, PageInfo{Name: "pager", Title: "Pager", Icon: "chart"}, pages[0])

	doc, err := mgr.RenderPage(context.Background(), "pager", PageRequest{})
	require.NoError(t, err)
	var page PageDoc
	require.NoError(t, json.Unmarshal(doc, &page))
	assert.Equal(t, "Pager", page.Title)
	require.Len(t, page.Blocks, 3)

	doc, err = mgr.RenderPage(context.Background(), "pager",
		PageRequest{Action: "bump", Params: map[string]string{"n": "3"}})
	require.NoError(t, err)
	assert.Contains(t, string(doc), "action=bump n=3")

	_, err = mgr.RenderPage(context.Background(), "missing", PageRequest{})
	assert.ErrorIs(t, err, ErrPluginNotFound)
}

func TestManagerRenderPageRequiresUI(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "plain",
		`name="plain"`+"\n"+`runtime="lua"`+"\n"+`hooks=["OnTransactionCreate"]`,
		`function OnTransactionCreate(txn) end`)

	store := testutil.NewStore(t)
	bus := events.NewBus()
	mgr := NewManager(Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})
	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	pl, ok := findByName(statuses, "plain")
	require.True(t, ok)
	_, err = mgr.SetEnabled(context.Background(), pl.ID, true)
	require.NoError(t, err)
	defer mgr.unload("plain")

	assert.Empty(t, mgr.Pages(), "a plugin without [ui] contributes no sidebar entry")
	_, err = mgr.RenderPage(context.Background(), "plain", PageRequest{})
	assert.ErrorIs(t, err, ErrNoPage)
}

func TestManagerRenderPageInvalidDocFails(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "pager", pagerManifest,
		`function OnPageRender(req) return { blocks = { { type = "iframe", text = "nope" } } } end
function OnPageAction(req) return OnPageRender(req) end`)

	store := testutil.NewStore(t)
	bus := events.NewBus()
	mgr := NewManager(Options{
		Store: store, Emitter: events.NewEmitter(bus), Bus: bus, Dir: dir,
		Runtimes:    map[string]Runtime{RuntimeLua: NewLuaRuntime()},
		HookTimeout: 2 * time.Second, Logger: testLogger(),
	})
	statuses, err := mgr.List(context.Background())
	require.NoError(t, err)
	pg, ok := findByName(statuses, "pager")
	require.True(t, ok)
	_, err = mgr.SetEnabled(context.Background(), pg.ID, true)
	require.NoError(t, err)
	defer mgr.unload("pager")

	_, err = mgr.RenderPage(context.Background(), "pager", PageRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown block type")
}
