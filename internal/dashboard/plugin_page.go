package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// pluginPageView is the generic view behind every /ext/<plugin> route: it asks
// the API to render the plugin's page (the server invokes the plugin's
// OnPageRender hook and validates the result) and renders the returned
// declarative block list. Plugins never ship frontend code — every block is
// rendered through go-app's text-safe primitives, so plugin output can't carry
// markup or script into the dashboard.
type pluginPageView struct {
	app.Compo
	chrome

	name    string // plugin name, from the /ext/<name> URL
	doc     pageDoc
	loaded  bool
	loading bool
	errMsg  string
	acting  string // id of the in-flight action ("" when none)
}

func (v *pluginPageView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.adoptRoute(ctx)
}

// OnNav handles client-side navigation BETWEEN two plugin pages: both paths
// match the one /ext/ regexp route, so go-app may reuse this mounted component
// instead of creating a fresh one — re-read the URL and reload when it changed.
func (v *pluginPageView) OnNav(ctx app.Context) {
	if extPageName(ctx) != v.name {
		v.adoptRoute(ctx)
	}
}

// adoptRoute points the view at the plugin named by the current URL and loads
// its page.
func (v *pluginPageView) adoptRoute(ctx app.Context) {
	v.name = extPageName(ctx)
	v.activeExt = v.name // highlights this entry in the sidebar
	v.doc = pageDoc{}
	v.loaded = false
	v.errMsg = ""
	v.loadPage(ctx)
}

// extPageName extracts the plugin name from the current /ext/<name> path.
func extPageName(ctx app.Context) string {
	return strings.TrimPrefix(ctx.Page().URL().Path, "/ext/")
}

func (v *pluginPageView) loadPage(ctx app.Context) {
	v.loading = true
	name := v.name
	ctx.Async(func() {
		doc, err := v.client.pluginPageDoc(context.Background(), name)
		ctx.Dispatch(func(ctx app.Context) {
			if name != v.name {
				return // navigated away while the render was in flight
			}
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.doc = doc
			v.loaded = true
			v.errMsg = ""
			ctx.Update()
		})
	})
}

// onAction posts a page button press to the plugin's OnPageAction hook and
// adopts the refreshed page it returns.
func (v *pluginPageView) onAction(ctx app.Context, a pageAction) {
	if v.acting != "" {
		return
	}
	v.acting = a.ID
	v.errMsg = ""
	ctx.Update()
	name := v.name
	ctx.Async(func() {
		doc, err := v.client.pluginPageAction(context.Background(), name, a.ID, a.Params)
		ctx.Dispatch(func(ctx app.Context) {
			if name != v.name {
				return
			}
			v.acting = ""
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.doc = doc
			v.loaded = true
			ctx.Update()
		})
	})
}

func (v *pluginPageView) Render() app.UI {
	title := v.doc.Title
	if title == "" {
		title = v.pageTitleFromNav()
	}

	content := []app.UI{
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text(title),
			app.Span().Class("page-subtitle").Text("Provided by the "+v.name+" plugin"),
		),
	}
	switch {
	case v.errMsg != "":
		content = append(content, app.Div().Class("error").Text("Error: "+v.errMsg))
		if v.loaded {
			content = append(content, v.renderBlocks()...)
		}
	case v.loading && !v.loaded:
		content = append(content, app.Div().Class("status").Text("Loading…"))
	case v.loaded:
		content = append(content, v.renderBlocks()...)
	}
	return v.renderShell(navExtension, content...)
}

// pageTitleFromNav falls back to the sidebar title from the manifest (or the
// plugin name) while the page document hasn't loaded yet.
func (v *pluginPageView) pageTitleFromNav() string {
	for _, p := range v.extPages {
		if p.Name == v.name {
			return p.Title
		}
	}
	return v.name
}

func (v *pluginPageView) renderBlocks() []app.UI {
	out := make([]app.UI, 0, len(v.doc.Blocks))
	for _, b := range v.doc.Blocks {
		out = append(out, v.renderBlock(b))
	}
	return out
}

func (v *pluginPageView) renderBlock(b pageBlock) app.UI {
	switch b.Type {
	case "heading":
		return app.H2().Class("ext-heading").Text(b.Text)
	case "text":
		return app.P().Class("ext-text").Text(b.Text)
	case "stat":
		return app.Div().Class("ext-stat card").Body(
			app.Span().Class("ext-stat-label").Text(b.Label),
			app.Span().Class("ext-stat-value").Text(b.Value),
			app.If(b.Hint != "", func() app.UI {
				return app.Span().Class("ext-stat-hint").Text(b.Hint)
			}),
		)
	case "keyvalue":
		return app.Dl().Class("ext-kv card").Body(
			app.Range(b.Items).Slice(func(i int) app.UI {
				it := b.Items[i]
				return app.Div().Class("ext-kv-row").Body(
					app.Dt().Class("ext-kv-key").Text(it.Key),
					app.Dd().Class("ext-kv-value").Text(it.Value),
				)
			}),
		)
	case "table":
		return v.renderTableBlock(b)
	case "actions":
		return app.Div().Class("ext-actions").Body(
			app.Range(b.Actions).Slice(func(i int) app.UI {
				return v.renderActionButton(b.Actions[i])
			}),
		)
	case "divider":
		return app.Hr().Class("ext-divider")
	default:
		// Unknown types are rejected server-side; render nothing defensively.
		return app.Text("")
	}
}

func (v *pluginPageView) renderTableBlock(b pageBlock) app.UI {
	return app.Table().Class("txns ext-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Range(b.Columns).Slice(func(i int) app.UI {
					return app.Th().Text(b.Columns[i])
				}),
			),
		),
		app.TBody().Body(
			app.Range(b.Rows).Slice(func(r int) app.UI {
				row := b.Rows[r]
				return app.Tr().Body(
					app.Range(b.Columns).Slice(func(c int) app.UI {
						cell := ""
						if c < len(row) {
							cell = row[c]
						}
						return app.Td().Text(cell)
					}),
				)
			}),
		),
	)
}

func (v *pluginPageView) renderActionButton(a pageAction) app.UI {
	cls := "btn"
	switch a.Style {
	case "primary":
		cls += " btn-primary"
	case "danger":
		cls += " btn-danger"
	}
	label := a.Label
	if v.acting == a.ID {
		label = "Working…"
	}
	return app.Button().Type("button").Class(cls).
		Text(label).
		Disabled(v.acting != "").
		OnClick(func(ctx app.Context, _ app.Event) { v.onAction(ctx, a) })
}

// extIcon resolves a manifest icon NAME to one of the dashboard's curated inline
// SVGs (mirrors plugins.knownUIIcons), defaulting to the puzzle glyph. Plugins
// pick by name only; the SVG bytes are always the dashboard's own.
func extIcon(name string) app.UI {
	if svg, ok := extIcons[name]; ok {
		return app.Raw(svg)
	}
	return iconPlugins()
}

var extIcons = map[string]string{
	"bell":     `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/></svg>`,
	"calendar": `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect width="18" height="18" x="3" y="4" rx="2"/><path d="M16 2v4"/><path d="M8 2v4"/><path d="M3 10h18"/></svg>`,
	"chart":    `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="M18 17V9"/><path d="M13 17V5"/><path d="M8 17v-3"/></svg>`,
	"coin":     `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="8"/><path d="M9.5 9.5c.5-1 1.5-1.5 2.5-1.5 1.5 0 2.5 1 2.5 2 0 2-5 2-5 4 0 1 1 2 2.5 2 1 0 2-.5 2.5-1.5"/><path d="M12 6v2"/><path d="M12 16v2"/></svg>`,
	"flag":     `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z"/><line x1="4" x2="4" y1="22" y2="15"/></svg>`,
	"gauge":    `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 14 4-4"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/></svg>`,
	"heart":    `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M19 14c1.49-1.46 3-3.21 3-5.5A5.5 5.5 0 0 0 16.5 3c-1.76 0-3 .5-4.5 2-1.5-1.5-2.74-2-4.5-2A5.5 5.5 0 0 0 2 8.5c0 2.3 1.5 4.05 3 5.5l7 7Z"/></svg>`,
	"list":     `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h.01"/><path d="M3 18h.01"/><path d="M3 6h.01"/><path d="M8 12h13"/><path d="M8 18h13"/><path d="M8 6h13"/></svg>`,
	"puzzle":   `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 19.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0-.474-1.68L1.756 12.7a2.414 2.414 0 0 1 0-3.414L3.44 7.6a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1 .474-1.68L9.343 1.7a2.414 2.414 0 0 1 3.414 0z"/></svg>`,
	"star":     `<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M11.525 2.295a.53.53 0 0 1 .95 0l2.31 4.679a2.12 2.12 0 0 0 1.595 1.16l5.166.756a.53.53 0 0 1 .294.904l-3.736 3.638a2.12 2.12 0 0 0-.611 1.878l.882 5.14a.53.53 0 0 1-.771.56l-4.618-2.428a2.12 2.12 0 0 0-1.973 0L6.396 21.01a.53.53 0 0 1-.77-.56l.881-5.139a2.12 2.12 0 0 0-.611-1.879L2.16 9.795a.53.53 0 0 1 .294-.906l5.165-.755a2.12 2.12 0 0 0 1.597-1.16z"/></svg>`,
}
