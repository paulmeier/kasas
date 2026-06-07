package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// navItem identifies a top-level navigation destination, used to highlight the
// active entry in the sidebar.
type navItem int

const (
	navTransactions navItem = iota
	navAccounts
	navSearch
	navLabels
	navRules
	navEvents
	navWebhooks
	navPlugins
	navSettings
)

// collapsedStorageKey is where the sidebar's collapsed/expanded choice is
// persisted so it survives navigation and reloads.
const collapsedStorageKey = "kasas.sidebar.collapsed"

// tokenStorageKey is where the dashboard token is persisted in the browser so it
// is sent on every API request and survives reloads. loginInputID is the stable
// DOM id of the login screen's token field (read imperatively on submit).
const (
	tokenStorageKey = "kasas.dashboard.token"
	loginInputID    = "kasas-login-token"
)

// chrome is the shared page chrome (the collapsible sidebar + the build-version
// badge) embedded by every page view. go-app routes have no common parent
// component, so the shell is shared by embedding this struct rather than via a
// layout component. It owns the API client too, so the embedding view's promoted
// `client` field is wired up by loadChrome. It also owns the auth gate: every
// page renders through renderShell, which shows a login screen when a token is
// required but not present.
type chrome struct {
	client    *apiClient
	collapsed bool   // sidebar collapsed to the icon rail
	version   string // build version for the corner badge

	// Auth gate state, populated by loadAuth.
	token        string // dashboard token from local storage
	authChecked  bool   // the /api/v1/auth probe has returned
	authRequired bool   // a token is required to use the API
	authed       bool   // our token (if any) is accepted
	loggingIn    bool   // a login attempt is in flight
	loginErr     string // last login error, shown under the field
}

// loadChrome initializes the shared chrome: it creates the API client (promoted
// to the embedding view) using any stored token, restores the collapsed choice
// from local storage, fetches the build version, and probes the auth state. Call
// it first in each view's OnMount.
func (c *chrome) loadChrome(ctx app.Context) {
	if c.client == nil {
		// Absent key leaves the token empty (unauthenticated request).
		_ = ctx.LocalStorage().Get(tokenStorageKey, &c.token)
		c.client = newAPIClient(originURL(), c.token)
	}
	// Absent key leaves collapsed at its zero value (expanded).
	_ = ctx.LocalStorage().Get(collapsedStorageKey, &c.collapsed)
	c.loadVersion(ctx)
	c.loadAuth(ctx)
}

// loadAuth probes whether a token is required and whether ours is accepted. On a
// request error it assumes auth is off so the app still loads — the server is the
// real gate; this only decides whether to show the login screen.
func (c *chrome) loadAuth(ctx app.Context) {
	ctx.Async(func() {
		st, err := c.client.authStatus(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			c.authChecked = true
			if err != nil {
				c.authRequired = false
				c.authed = true
				ctx.Update()
				return
			}
			c.authRequired = st.AuthRequired
			c.authed = st.Authenticated
			ctx.Update()
		})
	})
}

// adoptToken makes the current page use a token without a reload: it rebuilds the
// API client, persists (or clears) the token, and updates the gate state. tok ==
// "" revokes. Used by the Settings security panel so generating a token keeps the
// page signed in (a reload would hide the one-time token it shows).
func (c *chrome) adoptToken(ctx app.Context, tok string) {
	c.token = tok
	c.client = newAPIClient(originURL(), tok)
	if tok == "" {
		ctx.LocalStorage().Del(tokenStorageKey)
	} else {
		_ = ctx.LocalStorage().Set(tokenStorageKey, tok)
	}
	c.authRequired = tok != ""
	c.authed = true
	c.authChecked = true
}

func (c *chrome) onLoginKeyDown(ctx app.Context, e app.Event) {
	if e.Get("key").String() == "Enter" {
		e.PreventDefault()
		c.submitLogin(ctx)
	}
}

func (c *chrome) onLoginSubmit(ctx app.Context, _ app.Event) { c.submitLogin(ctx) }

// submitLogin validates the entered token against /api/v1/auth before persisting
// it, so a wrong token reports an error instead of reloading back into the login
// screen. On success it stores the token and reloads so every view refetches with
// it.
func (c *chrome) submitLogin(ctx app.Context) {
	if c.loggingIn {
		return
	}
	tok := domInputValue(loginInputID)
	if tok == "" {
		c.loginErr = "Enter your dashboard token."
		ctx.Update()
		return
	}
	c.loggingIn = true
	c.loginErr = ""
	ctx.Update()

	ctx.Async(func() {
		st, err := newAPIClient(originURL(), tok).authStatus(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			c.loggingIn = false
			if err != nil {
				c.loginErr = "Could not reach kasas: " + err.Error()
				ctx.Update()
				return
			}
			if !st.Authenticated {
				c.loginErr = "Invalid token."
				ctx.Update()
				return
			}
			_ = ctx.LocalStorage().Set(tokenStorageKey, tok)
			app.Window().Get("location").Call("reload")
		})
	})
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
// and the page content in a centered column on the right. It also enforces the
// auth gate: a loading screen until the auth probe returns, then the login screen
// when a token is required but ours is not accepted.
//
// The ordering keeps existing PrintHTML tests working: they build views without
// mounting, so client is nil and the gate is skipped (the normal shell renders).
func (c *chrome) renderShell(active navItem, content ...app.UI) app.UI {
	if c.client != nil && !c.authChecked {
		return renderAuthLoading()
	}
	if c.authChecked && c.authRequired && !c.authed {
		return c.renderLogin()
	}

	column := make([]app.UI, 0, len(content)+1)
	if c.authChecked && !c.authRequired {
		column = append(column, renderUnsecuredBanner())
	}
	column = append(column, content...)

	return app.Div().Class("app-shell").Body(
		c.renderSidebar(active),
		app.Main().Class("content").Body(
			app.Div().Class("page").Body(column...),
		),
	)
}

// renderAuthLoading is shown briefly while the auth probe is in flight, so the
// dashboard never flashes before redirecting to the login screen.
func renderAuthLoading() app.UI {
	return app.Div().Class("auth-loading").Body(
		app.Span().Class("auth-loading-text").Text("Loading…"),
	)
}

// renderLogin is the full-page token entry shown when a dashboard token is
// required but the browser has none (or an invalid one).
func (c *chrome) renderLogin() app.UI {
	body := []app.UI{
		app.Img().Class("login-logo").Src("/web/logo.png").Alt("kasas logo"),
		app.H1().Class("login-title").Text("kasas"),
		app.P().Class("login-help").Text("Enter your dashboard token to continue."),
		app.Input().
			ID(loginInputID).
			Class("settings-input login-input").
			Type("password").
			Placeholder("Dashboard token").
			AutoComplete(false).
			OnKeyDown(c.onLoginKeyDown),
		app.Button().
			Class("btn btn-primary login-btn").
			Text(unlockLabel(c.loggingIn)).
			Disabled(c.loggingIn).
			OnClick(c.onLoginSubmit),
	}
	if c.loginErr != "" {
		body = append(body, app.Div().Class("login-err").Text(c.loginErr))
	}
	return app.Div().Class("login-screen").Body(
		app.Div().Class("card login-card").Body(body...),
	)
}

func unlockLabel(busy bool) string {
	if busy {
		return "Unlocking…"
	}
	return "Unlock"
}

// renderUnsecuredBanner warns, on every page, that no token is set. It links to
// the Settings page where the user can generate one.
func renderUnsecuredBanner() app.UI {
	return app.Div().Class("unsecured-banner").Body(
		app.Span().Class("unsecured-text").Body(
			app.Text("kasas is "),
			app.Span().Class("unsecured-strong").Text("not secured"),
			app.Text(": anyone who can reach it can view your data and change settings. "),
		),
		app.A().Class("unsecured-link").Href("/settings").Text("Secure it →"),
	)
}

// domInputValue reads and trims the value of an <input> by id, returning "" when
// the element is absent (e.g. during a host/test render). Used to read the
// uncontrolled token fields imperatively.
func domInputValue(id string) string {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return ""
	}
	el := doc.Call("getElementById", id)
	if !el.Truthy() {
		return ""
	}
	return strings.TrimSpace(el.Get("value").String())
}

// clearDomInput empties an <input> by id imperatively (go-app drops empty value
// attributes, so a controlled Value("") cannot clear it).
func clearDomInput(id string) {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	el := doc.Call("getElementById", id)
	if el.Truthy() {
		el.Set("value", "")
	}
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
			navLink("/", "Transactions", iconTransactions(), active == navTransactions),
			navLink("/accounts", "Accounts", iconAccounts(), active == navAccounts),
			navLink("/search", "Search", iconSearch(), active == navSearch),
			navLink("/labels", "Labels", iconLabels(), active == navLabels),
			navLink("/rules", "Rules", iconRules(), active == navRules),
			navLink("/events", "Events", iconEvents(), active == navEvents),
			navLink("/webhooks", "Webhooks", iconWebhooks(), active == navWebhooks),
			navLink("/plugins", "Plugins", iconPlugins(), active == navPlugins),
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

func iconTransactions() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="8" x2="21" y1="6" y2="6"/><line x1="8" x2="21" y1="12" y2="12"/><line x1="8" x2="21" y1="18" y2="18"/><line x1="3" x2="3.01" y1="6" y2="6"/><line x1="3" x2="3.01" y1="12" y2="12"/><line x1="3" x2="3.01" y1="18" y2="18"/></svg>`)
}

func iconAccounts() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12V7H5a2 2 0 0 1 0-4h14v4"/><path d="M3 5v14a2 2 0 0 0 2 2h16v-5"/><path d="M18 12a2 2 0 0 0 0 4h4v-4Z"/></svg>`)
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

func iconEvents() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>`)
}

func iconWebhooks() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2"/><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"/><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8"/></svg>`)
}

func iconPlugins() app.UI {
	return app.Raw(`<svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15.39 4.39a1 1 0 0 0 1.68-.474 2.5 2.5 0 1 1 3.014 3.015 1 1 0 0 0-.474 1.68l1.683 1.682a2.414 2.414 0 0 1 0 3.414L19.61 19.39a1 1 0 0 1-1.68-.474 2.5 2.5 0 1 0-3.014 3.015 1 1 0 0 1 .474 1.68l-1.683 1.682a2.414 2.414 0 0 1-3.414 0L8.61 19.61a1 1 0 0 0-1.68.474 2.5 2.5 0 1 1-3.014-3.015 1 1 0 0 0-.474-1.68L1.756 12.7a2.414 2.414 0 0 1 0-3.414L3.44 7.6a1 1 0 0 1 1.68.474 2.5 2.5 0 1 0 3.014-3.015 1 1 0 0 1-.474-1.68L9.343 1.7a2.414 2.414 0 0 1 3.414 0z"/></svg>`)
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

// iconHistory is the per-row "view history" clock (a clock with a counter-clockwise
// arrow), distinct from the trash action icon.
func iconHistory() app.UI {
	return app.Raw(`<svg class="action-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 3v5h5"/><path d="M3.05 13A9 9 0 1 0 6 5.3L3 8"/><path d="M12 7v5l4 2"/></svg>`)
}

// iconProvenance is the per-row "view provenance" control: a branch/lineage glyph
// (two nodes joined), distinct from the history clock.
func iconProvenance() app.UI {
	return app.Raw(`<svg class="action-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>`)
}

// iconRelationships is the per-row "view relationships" control: a link glyph (two
// interlocking links), for the edges connecting one transaction to another.
func iconRelationships() app.UI {
	return app.Raw(`<svg class="action-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/></svg>`)
}
