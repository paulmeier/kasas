package dashboard

import (
	"context"
	"sort"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// marketplaceView is the Marketplace page: it browses the community plugin
// registry and lets an admin install (or update) a plugin into the plugins
// directory. Installing only downloads and integrity-verifies the plugin and
// registers it DISABLED; running it is a separate, deliberate step on the Plugins
// page — so this page never starts third-party code on its own.
type marketplaceView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	plugins   []registryPlugin
	available bool // whether the registry is configured/reachable (server-reported)
	loading   bool
	errMsg    string
	noticeMsg string // a transient success line (e.g. "Installed foo")
	busyName  string // plugin with an install in flight (disables its button)
}

func (v *marketplaceView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.fetchCatalog(ctx)
}

func (v *marketplaceView) fetchCatalog(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		ps, available, err := v.client.listPluginRegistry(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			sortRegistry(ps)
			v.plugins = ps
			v.available = available
			ctx.Update()
		})
	})
}

func (v *marketplaceView) onRefresh(ctx app.Context, _ app.Event) {
	v.noticeMsg = ""
	v.fetchCatalog(ctx)
}

func (v *marketplaceView) onInstall(ctx app.Context, p registryPlugin) {
	if v.busyName != "" {
		return
	}
	verb := "Install"
	if p.UpdateAvailable {
		verb = "Update"
	}
	msg := verb + " " + p.Name + " v" + p.Version + "?"
	if isWriteTier(p.CapabilityTier) {
		msg += "\n\nThis plugin can MODIFY your data (" + joinOrDash(p.Capabilities) + ")."
	}
	if isConnectedTier(p) {
		msg += "\n\n⚠️ CONNECTED-tier plugin: it requests NETWORK ACCESS (net:fetch)."
		if hosts := registryNetHosts(p); len(hosts) > 0 {
			msg += " It may reach only these hosts:\n  • " + strings.Join(hosts, "\n  • ")
		}
		msg += "\n\nYou review and approve any private/LAN access when you enable it on the Plugins page."
	}
	msg += "\n\nIt will be installed disabled; you enable it separately on the Plugins page."
	if !app.Window().Call("confirm", msg).Bool() {
		return
	}

	v.busyName = p.Name
	v.errMsg = ""
	v.noticeMsg = ""
	ctx.Update()

	name := p.Name
	ctx.Async(func() {
		_, err := v.client.installPlugin(context.Background(), name)
		ctx.Dispatch(func(ctx app.Context) {
			v.busyName = ""
			if err != nil {
				v.errMsg = "Failed to install " + name + ": " + err.Error()
				ctx.Update()
				return
			}
			v.markInstalled(name)
			v.noticeMsg = "Installed " + name + ". Enable it on the Plugins page to start running it."
			ctx.Update()
		})
	})
}

// markInstalled updates the local row after a successful install so the button
// flips to "Installed" without a full refetch.
func (v *marketplaceView) markInstalled(name string) {
	for i := range v.plugins {
		if v.plugins[i].Name == name {
			v.plugins[i].Installed = true
			v.plugins[i].InstalledVersion = v.plugins[i].Version
			v.plugins[i].UpdateAvailable = false
			return
		}
	}
}

// --- rendering ---

func (v *marketplaceView) Render() app.UI {
	return v.renderShell(navMarketplace,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Marketplace"),
			app.Span().Class("page-subtitle").Text("Browse and install community plugins, each gated and integrity-verified"),
		),
		v.renderError(),
		v.renderNotice(),
		v.renderToolbar(),
		v.renderList(),
	)
}

func (v *marketplaceView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *marketplaceView) renderNotice() app.UI {
	if v.noticeMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("status").Text(v.noticeMsg)
}

func (v *marketplaceView) renderToolbar() app.UI {
	return app.Div().Class("controls rules-toolbar").Body(
		app.Button().Class("btn").Text("Refresh").OnClick(v.onRefresh),
	)
}

func (v *marketplaceView) renderList() app.UI {
	if v.loading && len(v.plugins) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.plugins) > 0 {
		return v.renderTierGroups()
	}
	if v.errMsg != "" {
		return app.Text("")
	}
	if !v.available {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("The plugin marketplace is unavailable."),
			app.P().Class("empty-hint").Text(
				"Enable it with plugins.registry.enabled = true (and plugins.enabled = true) in your kasas config. It points at the official kasas-plugins registry by default."),
		)
	}
	return app.Div().Class("empty-state").Body(
		app.P().Class("empty-title").Text("No plugins in the registry yet."),
		app.P().Class("empty-hint").Text("Check back later, or contribute one to the kasas-plugins repository."),
	)
}

// renderTierGroups groups the catalog by trust tier (ADR 0003) — verified first,
// then connected, then any unlisted — so the escalating signal is visible at a
// glance. Each non-empty tier gets a labelled section with a short risk note.
func (v *marketplaceView) renderTierGroups() app.UI {
	return app.Div().Body(
		app.Range(tierOrder).Slice(func(i int) app.UI {
			tier := tierOrder[i]
			group := pluginsInTier(v.plugins, tier)
			if len(group) == 0 {
				return app.Text("")
			}
			return app.Div().Class("tier-group").Body(
				app.Div().Class("tier-group-header").Body(
					renderTierBadge(tier),
					app.Span().Class("tier-group-note").Text(tierNote(tier)),
				),
				v.renderTierTable(group),
			)
		}),
	)
}

func (v *marketplaceView) renderTierTable(group []registryPlugin) app.UI {
	return app.Table().Class("txns rules-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Plugin"),
				app.Th().Text("Hooks"),
				app.Th().Text("Capabilities"),
				app.Th().Text("Risk"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(group).Slice(func(i int) app.UI {
				return v.renderRow(group[i])
			}),
		),
	)
}

func (v *marketplaceView) renderRow(p registryPlugin) app.UI {
	return app.Tr().Body(
		app.Td().Body(
			app.Div().Class("plugin-name").Body(
				app.Text(p.Name),
				renderPageBadge(p.UI),
			),
			app.Div().Class("plugin-meta").Text(registryMetaText(p)),
		),
		app.Td().Text(joinOrDash(p.Hooks)),
		app.Td().Body(
			renderCapabilityBadges(p.Capabilities),
			renderRegistryNetHosts(p),
		),
		app.Td().Body(renderCapabilityTier(p.CapabilityTier)),
		app.Td().Class("right rule-actions").Body(v.renderInstallButton(p)),
	)
}

func (v *marketplaceView) renderInstallButton(p registryPlugin) app.UI {
	if p.Installed && !p.UpdateAvailable {
		return app.Span().Class("badge").Title("Installed v" + p.InstalledVersion + " — manage it on the Plugins page").Text("Installed")
	}
	label := "Install"
	if p.UpdateAvailable {
		label = "Update"
	}
	return app.Button().Type("button").Class("btn btn-small").
		Title("Download, verify, and install (disabled) into the plugins directory").
		Text(label).
		Disabled(v.busyName == p.Name).
		OnClick(func(ctx app.Context, _ app.Event) { v.onInstall(ctx, p) })
}

// registryMetaText is the small line under a plugin's name: runtime, version,
// author, and description, mirroring pluginMetaText on the Plugins page.
func registryMetaText(p registryPlugin) string {
	meta := p.Runtime
	if p.Version != "" {
		if meta != "" {
			meta += " · "
		}
		meta += "v" + p.Version
	}
	if p.Author != "" {
		meta += " · by " + p.Author
	}
	if p.Description != "" {
		if meta != "" {
			meta += " — "
		}
		meta += p.Description
	}
	return meta
}

// renderCapabilityTier renders the risk tier as a pill: a write-capable plugin can
// modify the ledger and is flagged; a read-only one is muted.
func renderCapabilityTier(tier string) app.UI {
	if isWriteTier(tier) {
		return app.Span().Class("status-pill disconnected").Title("Can modify your data (labels/extensions)").Text("write")
	}
	return app.Span().Class("badge muted").Title("Read-only: cannot modify your data").Text("read-only")
}

func isWriteTier(tier string) bool { return tier == "write" }

// Trust tiers (ADR 0003). tierOrder is the escalating display order; an empty or
// unknown tier from an older index is normalized to "verified" (the default,
// statically-sealed posture).
const (
	tierVerified  = "verified"
	tierConnected = "connected"
	tierUnlisted  = "unlisted"
)

var tierOrder = []string{tierVerified, tierConnected, tierUnlisted}

// trustTierOf returns the plugin's trust tier, defaulting an absent value (an older
// registry index predating ADR 0003) to verified.
func trustTierOf(p registryPlugin) string {
	switch p.Tier {
	case tierConnected, tierUnlisted:
		return p.Tier
	default:
		return tierVerified
	}
}

func isConnectedTier(p registryPlugin) bool { return trustTierOf(p) == tierConnected }

// pluginsInTier returns the plugins whose trust tier is tier, preserving order.
func pluginsInTier(ps []registryPlugin, tier string) []registryPlugin {
	out := make([]registryPlugin, 0, len(ps))
	for _, p := range ps {
		if trustTierOf(p) == tier {
			out = append(out, p)
		}
	}
	return out
}

// registryNetHosts returns a Connected plugin's declared egress hosts (nil otherwise).
func registryNetHosts(p registryPlugin) []string {
	if p.Net == nil {
		return nil
	}
	return p.Net.Allow
}

// renderTierBadge renders the trust tier as a pill, escalating verified → connected
// → unlisted, so a user sees how far they are extending trust at a glance.
func renderTierBadge(tier string) app.UI {
	switch tier {
	case tierConnected:
		return app.Span().Class("status-pill disconnected").
			Title("Connected: can make outbound network requests (net:fetch) to its declared hosts").Text("connected")
	case tierUnlisted:
		return app.Span().Class("status-pill disconnected").
			Title("Unlisted: requests a capability outside the reviewed set — install only code you trust").Text("unlisted")
	default:
		return app.Span().Class("status-pill connected").
			Title("Verified: sealed — this plugin cannot reach the network or disk").Text("verified")
	}
}

// tierNote is the short risk line shown beside a tier group heading.
func tierNote(tier string) string {
	switch tier {
	case tierConnected:
		return "Can reach the network (net:fetch) — only the hosts it declares, which you review on enable."
	case tierUnlisted:
		return "Requests an unreviewed capability surface — install only code you trust."
	default:
		return "Sealed: these plugins cannot reach the network or disk."
	}
}

// renderRegistryNetHosts shows a Connected plugin's declared egress hosts beneath
// its capabilities, so the operator sees exactly what it may reach before install
// (mirrors the Plugins page's net-host badges).
func renderRegistryNetHosts(p registryPlugin) app.UI {
	hosts := registryNetHosts(p)
	if !isConnectedTier(p) || len(hosts) == 0 {
		return app.Text("")
	}
	return app.Div().Class("net-allow-hosts").Body(
		app.Range(hosts).Slice(func(i int) app.UI {
			return app.Span().Class("badge net-host").
				Title("Outbound requests allowed to this host").Text(hosts[i])
		}),
	)
}

// renderPageBadge marks a plugin that adds its own dashboard page (a sidebar
// entry at /ext/<name> once installed and enabled). nil means no page.
func renderPageBadge(ui *pluginPage) app.UI {
	if ui == nil {
		return app.Text("")
	}
	return app.Span().Class("badge page-badge").
		Title("Adds a \"" + ui.Title + "\" page to the dashboard sidebar (once enabled)").
		Text("dashboard page")
}

// ensure deterministic display order even if the server's order ever changes.
func sortRegistry(ps []registryPlugin) {
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
}
