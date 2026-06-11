package dashboard

import (
	"context"
	"sort"
	"strconv"
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

	plugins   []plugin
	enabled   bool // whether the plugin system is enabled (server-reported)
	loading   bool
	errMsg    string
	noticeMsg string // transient success line (e.g. after an uninstall)
	busyID    int64  // plugin with an action in flight (disables its controls)

	// net:fetch grant modal (ADR 0002): the plugin being enabled (nil when closed)
	// and the per-host "allow private/LAN access" checkbox state.
	grantPlugin  *plugin
	grantChecked map[string]bool

	// net:fetch egress log modal: the plugin whose recent egress is shown.
	egressPlugin  *plugin
	egress        []egressEntry
	egressLoading bool
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
	if p.Enabled {
		v.doDisable(ctx, p)
		return
	}
	if !app.Window().Call("confirm",
		"Enable "+p.Name+"? This loads and runs third-party code that can read and modify your transactions.").Bool() {
		return
	}
	// A net:fetch plugin declares the hosts it wants to reach; collect the
	// operator's private/LAN grants before enabling rather than enabling blind.
	if pluginHasNetFetch(p) && len(p.NetAllow) > 0 {
		v.openGrantModal(ctx, p)
		return
	}
	v.doEnable(ctx, p, nil)
}

// doEnable enables a plugin, optionally passing the operator's net:fetch
// private-host grants. A nil grants slice leaves any stored grants unchanged.
func (v *pluginsView) doEnable(ctx app.Context, p plugin, grants []string) {
	v.busyID = p.ID
	v.errMsg = ""
	v.grantPlugin = nil // close the grant modal if this came from it
	ctx.Update()

	id := p.ID
	ctx.Async(func() {
		saved, err := v.client.enablePlugin(context.Background(), id, grants)
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

func (v *pluginsView) doDisable(ctx app.Context, p plugin) {
	v.busyID = p.ID
	v.errMsg = ""
	ctx.Update()

	id := p.ID
	ctx.Async(func() {
		saved, err := v.client.disablePlugin(context.Background(), id)
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

// --- net:fetch grant modal ---

func (v *pluginsView) openGrantModal(ctx app.Context, p plugin) {
	checked := make(map[string]bool, len(p.NetGrants))
	for _, h := range p.NetGrants {
		checked[h] = true
	}
	pc := p
	v.grantPlugin = &pc
	v.grantChecked = checked
	ctx.Update()
}

func (v *pluginsView) closeGrantModal(ctx app.Context, _ app.Event) {
	v.grantPlugin = nil
	ctx.Update()
}

func (v *pluginsView) onConfirmGrant(ctx app.Context, _ app.Event) {
	p := *v.grantPlugin
	// Send the full set of granted hosts (every ticked host); an unticked host gets
	// no private grant, so it is reachable only if it resolves to a public address.
	grants := []string{}
	for _, h := range p.NetAllow {
		if v.grantChecked[h] {
			grants = append(grants, h)
		}
	}
	v.doEnable(ctx, p, grants)
}

// --- net:fetch egress log modal ---

func (v *pluginsView) onViewEgress(ctx app.Context, p plugin) {
	pc := p
	v.egressPlugin = &pc
	v.egress = nil
	v.egressLoading = true
	ctx.Update()

	id := p.ID
	ctx.Async(func() {
		entries, _, err := v.client.pluginEgress(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.egressLoading = false
			if err != nil {
				v.errMsg = "Failed to load egress log: " + err.Error()
				v.egressPlugin = nil
				ctx.Update()
				return
			}
			v.egress = entries
			ctx.Update()
		})
	})
}

func (v *pluginsView) closeEgressModal(ctx app.Context, _ app.Event) {
	v.egressPlugin = nil
	ctx.Update()
}

// pluginHasNetFetch reports whether the plugin requests the net:fetch capability.
func pluginHasNetFetch(p plugin) bool {
	for _, c := range p.Capabilities {
		if c == "net:fetch" {
			return true
		}
	}
	return false
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

func (v *pluginsView) onUninstall(ctx app.Context, p plugin) {
	if v.busyID != 0 {
		return
	}
	if !app.Window().Call("confirm",
		"Uninstall "+p.Name+"? This runs the plugin's cleanup hook, then permanently removes its files and registration. To reinstall later you'd add it again from the Marketplace or by hand.").Bool() {
		return
	}
	v.busyID = p.ID
	v.errMsg = ""
	v.noticeMsg = ""
	ctx.Update()

	id := p.ID
	name := p.Name
	ctx.Async(func() {
		res, err := v.client.uninstallPlugin(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.busyID = 0
			if err != nil {
				v.errMsg = "Failed to uninstall " + name + ": " + err.Error()
				ctx.Update()
				return
			}
			v.removePlugin(id)
			if res.HookError != "" {
				// The plugin is gone, but its own cleanup reported a problem — surface it.
				v.noticeMsg = "Uninstalled " + name + ", but its cleanup hook reported: " + res.HookError
			} else {
				v.noticeMsg = "Uninstalled " + name + "."
			}
			ctx.Update()
		})
	})
}

func (v *pluginsView) onRefresh(ctx app.Context, _ app.Event) {
	v.noticeMsg = ""
	v.fetchPlugins(ctx)
}

// removePlugin drops a plugin from the local list after it is uninstalled.
func (v *pluginsView) removePlugin(id int64) {
	out := v.plugins[:0]
	for _, p := range v.plugins {
		if p.ID != id {
			out = append(out, p)
		}
	}
	v.plugins = out
}

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
		v.renderNotice(),
		v.renderToolbar(),
		v.renderList(),
		v.renderGrantModal(),
		v.renderEgressModal(),
	)
}

func (v *pluginsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *pluginsView) renderNotice() app.UI {
	if v.noticeMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("status").Text(v.noticeMsg)
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
		app.Td().Body(renderPluginCapabilities(p)),
		app.Td().Body(renderPluginStatus(p)),
		app.Td().Class("right").Body(
			app.Input().Type("checkbox").Class("rule-enabled").
				Checked(p.Enabled).
				Disabled(v.busyID == p.ID || (!p.Enabled && !p.OnDisk)).
				OnChange(func(ctx app.Context, _ app.Event) { v.onToggleEnabled(ctx, p) }),
		),
		app.Td().Class("right rule-actions").Body(
			app.If(pluginHasNetFetch(p), func() app.UI {
				return app.Button().Type("button").Class("btn btn-small").
					Title("Recent outbound requests (net:fetch egress log)").Text("Network").
					Disabled(v.busyID == p.ID).
					OnClick(func(ctx app.Context, _ app.Event) { v.onViewEgress(ctx, p) })
			}),
			app.Button().Type("button").Class("btn btn-small").Title("Reload from disk").Text("Reload").
				Disabled(v.busyID == p.ID || !p.OnDisk).
				OnClick(func(ctx app.Context, _ app.Event) { v.onReload(ctx, p) }),
			app.Button().Type("button").Class("btn btn-small btn-danger").
				Title("Run the plugin's cleanup hook, then remove it entirely").Text("Uninstall").
				Disabled(v.busyID == p.ID).
				OnClick(func(ctx app.Context, _ app.Event) { v.onUninstall(ctx, p) }),
		),
	)
}

// renderPluginCapabilities renders a plugin's granted capabilities, and — for a
// net:fetch plugin — the declared egress hosts beneath them, so the operator sees
// exactly what it can reach without opening a modal.
func renderPluginCapabilities(p plugin) app.UI {
	return app.Div().Body(
		renderCapabilityBadges(p.Granted),
		app.If(pluginHasNetFetch(p) && len(p.NetAllow) > 0, func() app.UI {
			return app.Div().Class("net-allow-hosts").Body(
				app.Range(p.NetAllow).Slice(func(i int) app.UI {
					host := p.NetAllow[i]
					granted := containsString(p.NetGrants, host)
					title := "Egress allowed to this host"
					if granted {
						title = "Egress allowed; granted private/LAN access"
					}
					badge := app.Span().Class("badge net-host").Title(title).Text(host)
					if granted {
						badge = app.Span().Class("badge net-host net-host-granted").Title(title).Text("🔓 " + host)
					}
					return badge
				}),
			)
		}),
	)
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
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

// renderGrantModal collects the operator's net:fetch private-host grants before a
// net:fetch plugin is enabled. It lists the plugin's declared egress hosts; the
// operator ticks the ones on their private network/LAN (a private address is
// blocked unless explicitly granted), and untouched hosts are reachable only if
// they resolve to a public address.
func (v *pluginsView) renderGrantModal() app.UI {
	if v.grantPlugin == nil {
		return app.Text("")
	}
	p := *v.grantPlugin
	return app.Div().Class("modal-overlay").OnClick(v.closeGrantModal).Body(
		app.Div().Class("modal editor-modal").
			OnClick(func(ctx app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.H3().Class("modal-title").Text("Network access — "+p.Name),
					app.Button().Class("modal-close").Text("×").OnClick(v.closeGrantModal),
				),
				app.Div().Class("modal-body").Body(
					app.P().Class("form-hint").Text(
						"This plugin can reach only the hosts below — its declared allowlist. Public hosts work as-is. "+
							"Tick a host ONLY if it lives on your private network / LAN (e.g. a 192.168.x.x address or a *.lan name): "+
							"reaching a private address is blocked unless you grant it here, for this plugin."),
					app.Div().Class("net-grant-list").Body(
						app.Range(p.NetAllow).Slice(func(i int) app.UI {
							host := p.NetAllow[i]
							return app.Label().Class("net-grant-row").Body(
								app.Input().Type("checkbox").
									Checked(v.grantChecked[host]).
									OnChange(func(ctx app.Context, _ app.Event) {
										v.grantChecked[host] = !v.grantChecked[host]
										ctx.Update()
									}),
								app.Span().Class("net-grant-host").Text(host),
								app.Span().Class("net-grant-note").Text("allow private/LAN access"),
							)
						}),
					),
					app.Div().Class("form-actions").Body(
						app.Button().Class("btn").Text("Cancel").OnClick(v.closeGrantModal),
						app.Button().Class("btn btn-primary").Text("Enable").
							Disabled(v.busyID == p.ID).OnClick(v.onConfirmGrant),
					),
				),
			),
	)
}

// renderEgressModal shows a plugin's recent net:fetch egress log — every outbound
// request the host performed on the plugin's behalf, allowed or refused.
func (v *pluginsView) renderEgressModal() app.UI {
	if v.egressPlugin == nil {
		return app.Text("")
	}
	p := *v.egressPlugin
	return app.Div().Class("modal-overlay").OnClick(v.closeEgressModal).Body(
		app.Div().Class("modal history-modal").
			OnClick(func(ctx app.Context, e app.Event) { e.Call("stopPropagation") }).
			Body(
				app.Div().Class("modal-header").Body(
					app.H2().Class("modal-title").Text("Network activity — "+p.Name),
					app.Button().Type("button").Class("modal-close").Title("Close").Text("×").OnClick(v.closeEgressModal),
				),
				app.Div().Class("modal-body").Body(v.renderEgressContent()),
			),
	)
}

func (v *pluginsView) renderEgressContent() app.UI {
	if v.egressLoading {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.egress) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-hint").Text("No outbound requests recorded yet. The host logs every kasas.fetch the plugin makes here (most recent first)."),
		)
	}
	return app.Table().Class("txns").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("When"),
				app.Th().Text("Method"),
				app.Th().Text("Host"),
				app.Th().Class("right").Text("Status"),
				app.Th().Class("right").Text("Bytes"),
				app.Th().Text("Result"),
			),
		),
		app.TBody().Body(
			app.Range(v.egress).Slice(func(i int) app.UI {
				e := v.egress[i]
				result := "ok"
				cls := "status-pill connected"
				if e.Error != "" {
					result = e.Error
					cls = "status-pill disconnected"
				}
				return app.Tr().Body(
					app.Td().Text(e.Time.Format("15:04:05")),
					app.Td().Text(e.Method),
					app.Td().Title(e.URL).Text(e.Host),
					app.Td().Class("right").Text(egressStatusText(e)),
					app.Td().Class("right").Text(strconv.FormatInt(e.Bytes, 10)),
					app.Td().Body(app.Span().Class(cls).Title(result).Text(truncateText(result, 60))),
				)
			}),
		),
	)
}

func egressStatusText(e egressEntry) string {
	if e.Status == 0 {
		return "—"
	}
	return strconv.Itoa(e.Status)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// joinOrDash renders a string list as a comma-separated string, or an em dash when
// empty.
func joinOrDash(ss []string) string {
	if len(ss) == 0 {
		return "—"
	}
	return strings.Join(ss, ", ")
}
