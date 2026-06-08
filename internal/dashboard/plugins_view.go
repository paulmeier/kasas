package dashboard

import (
	"context"
	"sort"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// pluginsView is the Plugins page: it lists the plugins discovered in the plugins
// directory with their declared hooks, granted capabilities, and last-run health,
// and lets the operator enable/disable and reload them. Plugins are installed on
// disk (not created in the UI), so there is no create form — enabling one loads
// and runs third-party code, so the toggle confirms first.
type pluginsView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	plugins []plugin
	enabled bool // whether the plugin system is enabled (server-reported)
	loading bool
	errMsg  string
	busyID  int64 // plugin with an action in flight (disables its controls)
}

func (v *pluginsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.fetchPlugins(ctx)
}

func (v *pluginsView) fetchPlugins(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		ps, enabled, err := v.client.listPlugins(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.plugins = ps
			v.enabled = enabled
			ctx.Update()
		})
	})
}

// --- row actions ---

func (v *pluginsView) onToggleEnabled(ctx app.Context, p plugin) {
	if v.busyID != 0 {
		return
	}
	enable := !p.Enabled
	if enable && !app.Window().Call("confirm",
		"Enable "+p.Name+"? This loads and runs third-party code that can read and modify your transactions.").Bool() {
		return
	}
	v.busyID = p.ID
	v.errMsg = ""
	ctx.Update()

	id := p.ID
	ctx.Async(func() {
		var (
			saved plugin
			err   error
		)
		if enable {
			saved, err = v.client.enablePlugin(context.Background(), id)
		} else {
			saved, err = v.client.disablePlugin(context.Background(), id)
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.busyID = 0
			if err != nil {
				v.errMsg = "Failed to update plugin: " + err.Error()
				ctx.Update()
				return
			}
			v.upsertPlugin(saved)
			ctx.Update()
		})
	})
}

func (v *pluginsView) onReload(ctx app.Context, p plugin) {
	if v.busyID != 0 {
		return
	}
	v.busyID = p.ID
	v.errMsg = ""
	ctx.Update()

	id := p.ID
	ctx.Async(func() {
		saved, err := v.client.reloadPlugin(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.busyID = 0
			if err != nil {
				v.errMsg = "Failed to reload plugin: " + err.Error()
				ctx.Update()
				return
			}
			v.upsertPlugin(saved)
			ctx.Update()
		})
	})
}

func (v *pluginsView) onRefresh(ctx app.Context, _ app.Event) { v.fetchPlugins(ctx) }

func (v *pluginsView) upsertPlugin(p plugin) {
	for i := range v.plugins {
		if v.plugins[i].ID == p.ID {
			v.plugins[i] = p
			return
		}
	}
	v.plugins = append(v.plugins, p)
	sort.Slice(v.plugins, func(i, j int) bool { return v.plugins[i].Name < v.plugins[j].Name })
}

// --- rendering ---

func (v *pluginsView) Render() app.UI {
	return v.renderShell(navPlugins,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Plugins"),
			app.Span().Class("page-subtitle").Text("Extend kasas with sandboxed plugins that react to ledger events"),
		),
		v.renderError(),
		v.renderToolbar(),
		v.renderList(),
	)
}

func (v *pluginsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *pluginsView) renderToolbar() app.UI {
	return app.Div().Class("controls rules-toolbar").Body(
		app.Button().Class("btn").Text("Refresh").OnClick(v.onRefresh),
	)
}

func (v *pluginsView) renderList() app.UI {
	if v.loading && len(v.plugins) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.plugins) > 0 {
		return v.renderTable()
	}
	// No plugins to show: a fetch error is already in the banner above (don't render
	// a misleading empty state under it); otherwise distinguish a disabled system
	// from an enabled-but-empty one.
	if v.errMsg != "" {
		return app.Text("")
	}
	if !v.enabled {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("The plugin system is disabled."),
			app.P().Class("empty-hint").Text(
				"Set plugins.enabled = true (and events.enabled = true, which plugins consume) in your kasas config, then restart to load and run plugins."),
		)
	}
	return app.Div().Class("empty-state").Body(
		app.P().Class("empty-title").Text("No plugins installed."),
		app.P().Class("empty-hint").Text(
			"Drop a plugin directory (a plugin.toml plus its source) into the plugins folder, then click Refresh. Enable a plugin to start running its hooks."),
	)
}

func (v *pluginsView) renderTable() app.UI {
	return app.Table().Class("txns rules-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Plugin"),
				app.Th().Text("Hooks"),
				app.Th().Text("Capabilities"),
				app.Th().Text("Status"),
				app.Th().Class("right").Text("Enabled"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.plugins).Slice(func(i int) app.UI {
				return v.renderPluginRow(v.plugins[i])
			}),
		),
	)
}

func (v *pluginsView) renderPluginRow(p plugin) app.UI {
	return app.Tr().Body(
		app.Td().Body(
			app.Div().Class("plugin-name").Text(p.Name),
			app.Div().Class("plugin-meta").Text(pluginMetaText(p)),
		),
		app.Td().Text(joinOrDash(p.Hooks)),
		app.Td().Body(renderCapabilityBadges(p.Granted)),
		app.Td().Body(renderPluginStatus(p)),
		app.Td().Class("right").Body(
			app.Input().Type("checkbox").Class("rule-enabled").
				Checked(p.Enabled).
				Disabled(v.busyID == p.ID || (!p.Enabled && !p.OnDisk)).
				OnChange(func(ctx app.Context, _ app.Event) { v.onToggleEnabled(ctx, p) }),
		),
		app.Td().Class("right rule-actions").Body(
			app.Button().Type("button").Class("btn btn-small").Title("Reload from disk").Text("Reload").
				Disabled(v.busyID == p.ID || !p.OnDisk).
				OnClick(func(ctx app.Context, _ app.Event) { v.onReload(ctx, p) }),
		),
	)
}

// pluginMetaText is the small line under a plugin's name: runtime, version, and an
// optional description.
func pluginMetaText(p plugin) string {
	parts := make([]string, 0, 3)
	if p.Runtime != "" {
		parts = append(parts, p.Runtime)
	}
	if p.Version != "" {
		parts = append(parts, "v"+p.Version)
	}
	meta := strings.Join(parts, " · ")
	if p.Description != "" {
		if meta != "" {
			meta += " — "
		}
		meta += p.Description
	}
	return meta
}

func renderCapabilityBadges(caps []string) app.UI {
	if len(caps) == 0 {
		return app.Span().Class("badge muted").Text("none")
	}
	return app.Div().Class("cap-badges").Body(
		app.Range(caps).Slice(func(i int) app.UI {
			return app.Span().Class("badge").Text(caps[i])
		}),
	)
}

// renderPluginStatus renders a plugin's state as a colored pill, with the last
// error as the hover title when it failed.
func renderPluginStatus(p plugin) app.UI {
	switch p.State {
	case "loaded":
		return app.Span().Class("status-pill connected").Title("Loaded and running").Text("Running")
	case "error":
		title := "Plugin error"
		if p.LastError != "" {
			title = p.LastError
		}
		return app.Span().Class("status-pill disconnected").Title(title).Text("Error")
	case "missing":
		return app.Span().Class("badge muted").Title("Registered but no longer present on disk").Text("Missing")
	default: // disabled
		return app.Span().Class("badge muted").Text("Disabled")
	}
}

// joinOrDash renders a string list as a comma-separated string, or an em dash when
// empty.
func joinOrDash(ss []string) string {
	if len(ss) == 0 {
		return "—"
	}
	return strings.Join(ss, ", ")
}
