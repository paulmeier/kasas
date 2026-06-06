package dashboard

import (
	"context"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// navItem identifies a top-level navigation destination, used to highlight the
// active entry in the sidebar.
type navItem int

const (
	navDashboard navItem = iota
	navSearch
	navLabels
	navRules
	navSettings
)

// collapsedStorageKey is where the sidebar's collapsed/expanded choice is
// persisted so it survives navigation and reloads.
const collapsedStorageKey = "kasas.sidebar.collapsed"

// chrome is the shared page chrome (the collapsible sidebar + the build-version
// badge) embedded by every page view. go-app routes have no common parent
// component, so the shell is shared by embedding this struct rather than via a
// layout component. It owns the API client too, so the embedding view's promoted
// `client` field is wired up by loadChrome.
type chrome struct {
	client    *apiClient
	collapsed bool   // sidebar collapsed to the icon rail
	version   string // build version for the corner badge
}

// loadChrome initializes the shared chrome: it creates the API client (promoted
// to the embedding view), restores the collapsed choice from local storage, and
// fetches the build version for the badge. Call it first in each view's OnMount.
func (c *chrome) loadChrome(ctx app.Context) {
	if c.client == nil {
		c.client = newAPIClient(originURL())
	}
	// Absent key leaves collapsed at its zero value (expanded).
	_ = ctx.LocalStorage().Get(collapsedStorageKey, &c.collapsed)
	c.loadVersion(ctx)
}

// loadVersion fetches the build version for the corner badge. Best-effort: the
// badge stays hidden if the request fails.
func (c *chrome) loadVersion(ctx app.Context) {
	ctx.Async(func() {
		ver, err := c.client.buildVersion(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return
			}
			c.version = ver
			ctx.Update()
		})
	})
}

// toggleCollapsed flips the sidebar between expanded and the icon rail, persisting
// the choice so other pages and future loads honour it.
func (c *chrome) toggleCollapsed(ctx app.Context) {
	c.collapsed = !c.collapsed
	_ = ctx.LocalStorage().Set(collapsedStorageKey, c.collapsed)
	ctx.Update()
}

// renderShell wraps a page's content in the app shell: the sidebar on the left
// and the page content in a centered column on the right.
func (c *chrome) renderShell(active navItem, content ...app.UI) app.UI {
	return app.Div().Class("app-shell").Body(
		c.renderSidebar(active),
		app.Main().Class("content").Body(
			app.Div().Class("page").Body(content...),
		),
	)
}

func (c *chrome) renderSidebar(active navItem) app.UI {
	cls := "sidebar"
	if c.collapsed {
		cls += " collapsed"
	}
	collapseTitle := "Collapse sidebar"
	if c.collapsed {
		collapseTitle = "Expand sidebar"
	}
	return app.Nav().Class(cls).Body(
		app.A().Class("sidebar-head").Href("/").Body(
			app.Img().Class("logo").Src("/web/logo.png").Alt("kasas logo"),
			app.Span().Class("brand").Text("kasas"),
		),
		app.Div().Class("nav").Body(
			navLink("/", "Dashboard", iconDashboard(), active == navDashboard),
			navLink("/search", "Search", iconSearch(), active == navSearch),
			navLink("/labels", "Labels", iconLabels(), active == navLabels),
			navLink("/rules", "Rules", iconRules(), active == navRules),
			navLink("/settings", "Settings", iconSettings(), active == navSettings),
		),
		app.Div().Class("sidebar-foot").Body(
			app.Button().Type("button").Class("collapse-btn").Title(collapseTitle).
				OnClick(func(ctx app.Context, _ app.Event) { c.toggleCollapsed(ctx) }).
				Body(
					iconChevrons(),
					app.Span().Class("nav-label").Text("Collapse"),
				),
			c.renderVersion(),
		),
	)
}

// navLink is one sidebar entry: an internal link (go-app routes it client-side)
// with an icon and a label that the collapsed rail hides via CSS.
func navLink(href, label string, icon app.UI, active bool) app.UI {
	cls := "nav-item"
	if active {
		cls += " active"
	}
	return app.A().Class(cls).Href(href).Body(
		icon,
		app.Span().Class("nav-label").Text(label),
	)
}

// renderVersion shows the build version in the sidebar footer. The value is
// go-app's GOAPP_VERSION (binary version + a hash of the served UI, the
// service-worker cache key), so a changed badge confirms the browser is running a
// fresh build rather than a cached one.
func (c *chrome) renderVersion() app.UI {
	if c.version == "" {
		return app.Text("")
	}
	return app.Div().Class("version-badge").
		Title("kasas build version (service-worker cache key)").
		Text(c.version)
}

// Sidebar icons are inline SVGs (stroke = currentColor) so CSS controls their
// size and colour. They render via app.Raw, which needs a single root element —
// the <svg> — so each is one self-contained string.

func iconDashboard() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="9" rx="1"/><rect x="14" y="3" width="7" height="5" rx="1"/><rect x="14" y="12" width="7" height="9" rx="1"/><rect x="3" y="16" width="7" height="5" rx="1"/></svg>`)
}

func iconSearch() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/></svg>`)
}

func iconLabels() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.586 2.586A2 2 0 0 0 11.172 2H4a2 2 0 0 0-2 2v7.172a2 2 0 0 0 .586 1.414l8.704 8.704a2.426 2.426 0 0 0 3.42 0l6.586-6.586a2.426 2.426 0 0 0 0-3.42z"/><circle cx="7.5" cy="7.5" r="1.5"/></svg>`)
}

func iconRules() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="21" x2="14" y1="4" y2="4"/><line x1="10" x2="3" y1="4" y2="4"/><line x1="21" x2="12" y1="12" y2="12"/><line x1="8" x2="3" y1="12" y2="12"/><line x1="21" x2="16" y1="20" y2="20"/><line x1="12" x2="3" y1="20" y2="20"/><line x1="14" x2="14" y1="2" y2="6"/><line x1="8" x2="8" y1="10" y2="14"/><line x1="16" x2="16" y1="18" y2="22"/></svg>`)
}

func iconSettings() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`)
}

func iconChevrons() app.UI {
	return app.Raw(`<svg class="nav-icon collapse-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m11 17-5-5 5-5"/><path d="m18 17-5-5 5-5"/></svg>`)
}

func iconTrash() app.UI {
	return app.Raw(`<svg class="action-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/><path d="M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>`)
}
